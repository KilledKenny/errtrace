// Package render prints an Error Set: the text form (gopls-style span lines,
// each followed by an indented tree with the Wrap Chain and the Propagation
// Path).
package render

import (
	"fmt"
	"go/types"
	"io"
	"strconv"
	"strings"

	"github.com/killedKenny/errtrace/internal/errset"
	"github.com/killedKenny/errtrace/internal/span"
)

// Locator gives the span of a value's or Propagation Path hop's Site (see
// errset.Value.Site and errset.Hop.Site).
type Locator interface {
	Locate(site any) (span.Span, bool)
}

// Input is everything the renderer needs to print an Error Set.
type Input struct {
	// TargetSpan is the span of the Target.
	TargetSpan span.Span
	// TargetName is how the header names the Target.
	TargetName string
	// Table holds the values the entries refer to.
	Table *errset.Table
	// Entries are the Error Set's entries, in output order.
	Entries []errset.Entry
	// Locator gives the spans of sites.
	Locator Locator
	// Fanout groups the entries whose Propagation Paths pass, as their
	// dynamic hop nearest the Target, through the same call site with at
	// least Fanout Candidates, if there are at least Fanout of them.
	// Zero (or less) never groups.
	Fanout int
}

// noSpan is printed in place of a span that is not known.
const noSpan = "-"

// Text writes in as text:
//
//	<target-span>: Error Set of <target>: <N> entries[, <U> unresolved][, <H> possibly handled]
//	<entry-span>: <label>[ [typed nil]][ [chain truncated]][ [+K more paths]]
//		wrap chain (origin → target):
//			<wrap-span>: <wrap description>
//		possibly handled:
//			<guard-span>: <reason>
//		path (origin → target):
//			<hop-span>: in <function>
//			<hop-span>: in <caller>, call to <callee>[ (dynamic <method>: <k> of <n> candidates)]
//	<site-span>: group of <M> entries via dynamic call to <method> (<n> candidates) in <function>
//		<entry-span>: <label> (candidate <callee>)
//
// Sections that would be empty are left out. Grouped entries (see
// Input.Fanout) come after the others, one block per call site, and show
// only their label and Candidate.
func Text(w io.Writer, in Input) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: Error Set of %s: %d entries", in.TargetSpan, in.TargetName, len(in.Entries))
	if u := errset.CountUnresolved(in.Table, in.Entries); u > 0 {
		fmt.Fprintf(&b, ", %d unresolved", u)
	}
	if h := errset.CountPossiblyHandled(in.Entries); h > 0 {
		fmt.Fprintf(&b, ", %d possibly handled", h)
	}
	b.WriteByte('\n')
	groups, grouped := groupEntries(in.Entries, in.Fanout)
	for i, e := range in.Entries {
		if grouped[i] {
			continue
		}
		in.entry(&b, e)
	}
	for _, g := range groups {
		h := g.hop
		fmt.Fprintf(&b, "%s: group of %d entries via dynamic call to %s (%d candidates) in %s\n",
			in.locate(h.Site), len(g.entries), h.Method, h.Candidates, h.Func)
		for _, i := range g.entries {
			e := in.Entries[i]
			fmt.Fprintf(&b, "\t%s: %s (candidate %s)\n", in.locate(in.Table.Get(e.Leaf).Site), Label(in.Table, e.Leaf), groupHop(e.Path).Callee)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// entry writes one ungrouped entry with its subtree.
func (in Input) entry(b *strings.Builder, e errset.Entry) {
	leaf := in.Table.Get(e.Leaf)
	fmt.Fprintf(b, "%s: %s", in.locate(leaf.Site), Label(in.Table, e.Leaf))
	if leaf.Kind == errset.Concrete && leaf.TypedNil {
		b.WriteString(" [typed nil]")
	}
	if e.Truncated {
		b.WriteString(" [chain truncated]")
	}
	if e.MorePaths > 0 {
		fmt.Fprintf(b, " [+%d more paths]", e.MorePaths)
	}
	b.WriteByte('\n')
	if len(e.Chain) > 0 {
		b.WriteString("\twrap chain (origin → target):\n")
		for _, wid := range e.Chain {
			fmt.Fprintf(b, "\t\t%s: %s\n", in.locate(in.Table.Get(wid).Site), in.Table.WrapDescription(wid))
		}
	}
	if len(e.PossiblyHandled) > 0 {
		b.WriteString("\tpossibly handled:\n")
		for _, n := range e.PossiblyHandled {
			fmt.Fprintf(b, "\t\t%s: %s\n", in.locate(n.Site), n.Reason)
		}
	}
	if len(e.Path) > 0 {
		b.WriteString("\tpath (origin → target):\n")
		for _, h := range e.Path {
			fmt.Fprintf(b, "\t\t%s: in %s", in.locate(h.Site), h.Func)
			if h.Callee != "" {
				fmt.Fprintf(b, ", call to %s", h.Callee)
			}
			if h.Dynamic {
				fmt.Fprintf(b, " (dynamic %s: %d of %d candidates)", h.Method, h.Candidates, h.CandidatesBefore)
			}
			b.WriteByte('\n')
		}
	}
}

// group is the entries grouped under one dynamic call site.
type group struct {
	// hop is the site's hop in the first entry's path.
	hop errset.Hop
	// entries are the indices of the grouped entries, in order.
	entries []int
}

// groupHop returns the dynamic hop of path nearest the Target, or the zero
// Hop.
func groupHop(path errset.Path) errset.Hop {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].Dynamic {
			return path[i]
		}
	}
	return errset.Hop{}
}

// groupEntries groups the entries whose dynamic hop nearest the Target is
// at the same call site with at least fanout Candidates, where at least
// fanout entries share the site. It returns the groups in the order of
// their first entries, and which entries are grouped.
func groupEntries(entries []errset.Entry, fanout int) ([]group, []bool) {
	grouped := make([]bool, len(entries))
	if fanout <= 0 {
		return nil, grouped
	}
	var all []*group
	bySite := make(map[any]*group)
	for i, e := range entries {
		h := groupHop(e.Path)
		if !h.Dynamic || h.Site == nil || h.Candidates < fanout {
			continue
		}
		g := bySite[h.Site]
		if g == nil {
			g = &group{hop: h}
			bySite[h.Site] = g
			all = append(all, g)
		}
		g.entries = append(g.entries, i)
	}
	var out []group
	for _, g := range all {
		if len(g.entries) < fanout {
			continue
		}
		for _, i := range g.entries {
			grouped[i] = true
		}
		out = append(out, *g)
	}
	return out, grouped
}

func (in Input) locate(site any) string {
	if in.Locator != nil {
		if s, ok := in.Locator.Locate(site); ok {
			return s.String()
		}
	}
	return noSpan
}

// Label returns how an entry's leaf is named: sentinel <pkgpath>.<Name>,
// concrete <type>, opaque errors.New "<msg>", opaque, or
// unresolved(<kind>): <detail>.
func Label(t *errset.Table, id errset.ValueID) string {
	v := t.Get(id)
	switch v.Kind {
	case errset.Sentinel:
		if v.Var.Pkg() != nil {
			return "sentinel " + v.Var.Pkg().Path() + "." + v.Var.Name()
		}
		return "sentinel " + v.Var.Name()
	case errset.Concrete:
		return "concrete " + types.TypeString(v.Type, nil)
	case errset.Opaque:
		s := "opaque"
		if v.Constructor != "" {
			s += " " + v.Constructor
			if v.HasMessage {
				s += " " + strconv.Quote(v.Message)
			}
		}
		return s
	case errset.Unresolved:
		return fmt.Sprintf("unresolved(%s): %s", v.Unresolved, v.Detail)
	}
	return t.String(id)
}
