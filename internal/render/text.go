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
}

// noSpan is printed in place of a span that is not known.
const noSpan = "-"

// Text writes in as text:
//
//	<target-span>: Error Set of <target>: <N> entries[, <U> unresolved]
//	<entry-span>: <label>[ [typed nil]][ [chain truncated]][ [+K more paths]]
//		wrap chain (origin → target):
//			<wrap-span>: <wrap description>
//		path (origin → target):
//			<hop-span>: in <function>
//			<hop-span>: in <caller>, call to <callee>
//
// Sections that would be empty are left out.
func Text(w io.Writer, in Input) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: Error Set of %s: %d entries", in.TargetSpan, in.TargetName, len(in.Entries))
	if u := errset.CountUnresolved(in.Table, in.Entries); u > 0 {
		fmt.Fprintf(&b, ", %d unresolved", u)
	}
	b.WriteByte('\n')
	for _, e := range in.Entries {
		leaf := in.Table.Get(e.Leaf)
		fmt.Fprintf(&b, "%s: %s", in.locate(leaf.Site), Label(in.Table, e.Leaf))
		if leaf.Kind == errset.Concrete && leaf.TypedNil {
			b.WriteString(" [typed nil]")
		}
		if e.Truncated {
			b.WriteString(" [chain truncated]")
		}
		if e.MorePaths > 0 {
			fmt.Fprintf(&b, " [+%d more paths]", e.MorePaths)
		}
		b.WriteByte('\n')
		if len(e.Chain) > 0 {
			b.WriteString("\twrap chain (origin → target):\n")
			for _, wid := range e.Chain {
				fmt.Fprintf(&b, "\t\t%s: %s\n", in.locate(in.Table.Get(wid).Site), in.Table.WrapDescription(wid))
			}
		}
		if len(e.Path) > 0 {
			b.WriteString("\tpath (origin → target):\n")
			for _, h := range e.Path {
				fmt.Fprintf(&b, "\t\t%s: in %s", in.locate(h.Site), h.Func)
				if h.Callee != "" {
					fmt.Fprintf(&b, ", call to %s", h.Callee)
				}
				b.WriteByte('\n')
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
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
