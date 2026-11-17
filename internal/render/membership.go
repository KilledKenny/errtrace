package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/KilledKenny/errtrace/internal/errset"
)

// TraceStep is one step of the trace of a Mark: where the upward pass made
// or moved it.
type TraceStep struct {
	// Site is where the step happens (see Locator); nil if it has no
	// place.
	Site any
	// Desc says what happens to the Mark there.
	Desc string
	// Wide reports a step from which the Mark is on every value of a
	// type or on shared memory: the pivot that Carriers are grouped by.
	Wide bool
}

// CarrierEntry is a Carrier: an entry the Subject of a Membership Query
// could come through, with the trace of its Mark.
type CarrierEntry struct {
	// Entry is the entry.
	Entry errset.Entry
	// Unresolved reports whether the entry's leaf is Unresolved.
	Unresolved bool
	// Trace are the steps from an origin of the Subject to the entry's
	// place; it may be empty.
	Trace []TraceStep
	// Wrap is the Wrap of the entry's chain that makes it a Carrier,
	// when its leaf alone does not hold the Subject; HasWrap says there
	// is one.
	Wrap    errset.ValueID
	HasWrap bool
}

// ReflectStore is a store through reflection that the Subject of a
// Membership Query reaches and the upward pass did not follow.
type ReflectStore struct {
	// Site is the call that stores, for printing its span.
	Site any
	// Function names the function the call is in.
	Function string
}

// maxNotFollowed is how many stores the "not followed" section lists
// unless Full is set.
const maxNotFollowed = 5

// MembershipInput is everything the renderer needs to print the Verdict
// of a Membership Query.
type MembershipInput struct {
	// Input gives the Target, the Table and the Locator; its Entries and
	// Fanout are not used.
	Input
	// Subject names the error asked about: <pkgpath>.<Name> or a type.
	Subject string
	// Verdict is "yes", "no" or "maybe".
	Verdict string
	// Matches are the entries that are the Subject.
	Matches []errset.Entry
	// Carriers are the Carriers of a maybe Verdict without Matches.
	Carriers []CarrierEntry
	// TargetTrace is how the Mark reached the Target's error result; it
	// is printed when there is no Carrier.
	TargetTrace []TraceStep
	// NotFollowed are the stores through reflection that the Subject
	// reaches and the upward pass did not follow; they are printed after
	// a no and after a maybe with Carriers.
	NotFollowed []ReflectStore
	// Full prints every Carrier with its whole trace instead of grouping
	// the Carriers that share a pivot, and every store of NotFollowed.
	Full bool
}

// Membership writes the Verdict of a Membership Query as text. The first
// line is one of
//
//	<target-span>: <X> in Error Set of <target>: yes
//	<target-span>: <X> in Error Set of <target>: no
//	<target-span>: <X> in Error Set of <target>: maybe: <K> entries could carry it (<U> unresolved)
//	<target-span>: <X> in Error Set of <target>: maybe: 1 entry could carry it (<U> unresolved)
//	<target-span>: <X> in Error Set of <target>: maybe: possibly handled
//
// After a yes, and after a maybe that is possibly handled, come the
// entries that are X, as Text prints entries (never grouped). After the
// other maybe come the K Carriers, nearest the Target first, each as Text
// prints the entry plus the trace of its Mark, when there is one:
//
//	<entry-span>: <label>
//		path (origin → target):
//			...
//		marked via (origin of <X> → here):
//			<span>: <step description>
//
// Carriers whose traces pass the same pivot (the first step from which the
// Mark is on a whole type or on shared memory, such as "value of type T,
// which may wrap X" or "loaded from field f of T") are printed together,
// after the others, smallest group first, unless Full is set: the steps
// that all their traces start with once (at least those up to the pivot
// of the first), then one line per entry. A pivot is the same by what it
// says, wherever it happens: the header has the span of the group's
// first.
//
//	<pivot-span>: group of <M> entries via: <pivot step description>
//		marked via (origin of <X> → here):
//			<span>: <step description>
//		<entry-span>: <label>[ [typed nil]][ [chain truncated]][ [+K more paths]][ under <wrap description>][ (<N> entries)]
//		...
//
// An entry line is as the first line Text prints for the entry. "under"
// names the Wrap of the entry's chain that makes it a Carrier, when its
// leaf alone would not be one. Entries of a group whose lines are the
// same are printed once, with their number.
//
// A maybe with 0 Carriers means that X can reach the Target (the upward
// pass, an upper bound, says so) while no entry of the Error Set shows
// how; it is followed by
//
//	<TAB><X> may reach the target, but no entry of the Error Set shows how
//	<TAB>marked via (origin of <X> → target):
//	<TAB><TAB><span>: <step description>
//
// A no is the header alone: X cannot reach the Target, whatever
// Unresolved entries its Error Set has.
//
// The upward pass does not follow a store through reflection whose
// destination it cannot tell (or, into fresh memory, whose destination it
// loses sight of). If X reaches such stores, a no, and a maybe that is not
// possibly handled, end with them (the first 5, all with Full), in the
// order of the pass:
//
//	<TAB>not followed: <X> reaches <N> reflective store[s] whose destination is unknown or only partly followed:
//	<TAB><TAB><span>: in <function>
//	<TAB><TAB>… and <K> more
//
// The Verdict does not cover what those stores write.
func Membership(w io.Writer, in MembershipInput) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s in Error Set of %s: %s", in.TargetSpan, in.Subject, in.TargetName, in.Verdict)
	switch {
	case in.Verdict == "maybe" && len(in.Matches) > 0:
		b.WriteString(": possibly handled\n")
		for _, e := range in.Matches {
			in.entry(&b, e)
		}
	case in.Verdict == "maybe":
		unresolved := 0
		for _, c := range in.Carriers {
			if c.Unresolved {
				unresolved++
			}
		}
		entries := "entries"
		if len(in.Carriers) == 1 {
			entries = "entry"
		}
		fmt.Fprintf(&b, ": %d %s could carry it (%d unresolved)\n", len(in.Carriers), entries, unresolved)
		if len(in.Carriers) == 0 {
			fmt.Fprintf(&b, "\t%s may reach the target, but no entry of the Error Set shows how\n", in.Subject)
			if len(in.TargetTrace) > 0 {
				fmt.Fprintf(&b, "\tmarked via (origin of %s → target):\n", in.Subject)
				in.steps(&b, in.TargetTrace)
			}
		}
		groups, grouped := in.pivotGroups()
		for i, c := range in.Carriers {
			if grouped[i] {
				continue
			}
			in.entry(&b, c.Entry)
			if len(c.Trace) == 0 {
				continue
			}
			fmt.Fprintf(&b, "\tmarked via (origin of %s → here):\n", in.Subject)
			in.steps(&b, c.Trace)
		}
		for _, g := range groups {
			first := in.Carriers[g.members[0]]
			pivot := first.Trace[g.pivot]
			fmt.Fprintf(&b, "%s: group of %d entries via: %s\n", in.locate(pivot.Site), len(g.members), pivot.Desc)
			fmt.Fprintf(&b, "\tmarked via (origin of %s → here):\n", in.Subject)
			in.steps(&b, first.Trace[:in.shared(g)])
			in.members(&b, g)
		}
		in.notFollowed(&b)
	case in.Verdict == "no":
		b.WriteByte('\n')
		in.notFollowed(&b)
	default:
		b.WriteByte('\n')
		for _, e := range in.Matches {
			in.entry(&b, e)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// members writes the entry lines of the group g: one line per entry, the
// entries whose lines are the same once, with their number.
func (in MembershipInput) members(b *strings.Builder, g pivotGroup) {
	var lines []string
	count := make(map[string]int)
	for _, i := range g.members {
		c := in.Carriers[i]
		line := in.head(c.Entry)
		if c.HasWrap {
			line += " under " + in.Table.WrapDescription(c.Wrap)
		}
		if count[line] == 0 {
			lines = append(lines, line)
		}
		count[line]++
	}
	for _, line := range lines {
		b.WriteByte('\t')
		b.WriteString(line)
		if n := count[line]; n > 1 {
			fmt.Fprintf(b, " (%d entries)", n)
		}
		b.WriteByte('\n')
	}
}

// notFollowed writes the section of the stores through reflection that
// the upward pass did not follow, if there are any.
func (in MembershipInput) notFollowed(b *strings.Builder) {
	n := len(in.NotFollowed)
	if n == 0 {
		return
	}
	stores := "stores"
	if n == 1 {
		stores = "store"
	}
	fmt.Fprintf(b, "\tnot followed: %s reaches %d reflective %s whose destination is unknown or only partly followed:\n", in.Subject, n, stores)
	shown := n
	if !in.Full && shown > maxNotFollowed {
		shown = maxNotFollowed
	}
	for _, rs := range in.NotFollowed[:shown] {
		fmt.Fprintf(b, "\t\t%s: in %s\n", in.locate(rs.Site), rs.Function)
	}
	if shown < n {
		fmt.Fprintf(b, "\t\t… and %d more\n", n-shown)
	}
}

// steps writes the steps of a trace, two tabs deep.
func (in MembershipInput) steps(b *strings.Builder, trace []TraceStep) {
	for _, s := range trace {
		fmt.Fprintf(b, "\t\t%s: %s\n", in.locate(s.Site), s.Desc)
	}
}

// shared returns the number of steps that the traces of all the Carriers
// of g start with: at least those up to the pivot.
func (in MembershipInput) shared(g pivotGroup) int {
	first := in.Carriers[g.members[0]].Trace
	n := len(first)
	for _, i := range g.members[1:] {
		t := in.Carriers[i].Trace
		k := 0
		for k < n && k < len(t) && t[k].Desc == first[k].Desc && in.locate(t[k].Site) == in.locate(first[k].Site) {
			k++
		}
		n = k
	}
	return max(n, g.pivot+1)
}

// pivotGroup is the Carriers whose traces pass the same pivot.
type pivotGroup struct {
	// members are the indices of the Carriers, in order; pivot is the
	// index of the pivot step in the first member's trace.
	members []int
	pivot   int
}

// pivotGroups groups the Carriers by the pivot of their trace: its first
// wide step, by its description. It returns the groups of two or more, smallest first (then in
// the order of their first Carriers), and which Carriers are grouped.
// With Full nothing is grouped.
func (in MembershipInput) pivotGroups() ([]pivotGroup, []bool) {
	grouped := make([]bool, len(in.Carriers))
	if in.Full {
		return nil, grouped
	}
	var all []*pivotGroup
	byKey := make(map[string]*pivotGroup)
	for i, c := range in.Carriers {
		for j, s := range c.Trace {
			if !s.Wide {
				continue
			}
			// By what the pivot says: the same type or memory is one
			// pivot, wherever the step happens.
			key := s.Desc
			g := byKey[key]
			if g == nil {
				g = &pivotGroup{pivot: j}
				byKey[key] = g
				all = append(all, g)
			}
			g.members = append(g.members, i)
			break
		}
	}
	var out []pivotGroup
	for _, g := range all {
		if len(g.members) < 2 {
			continue
		}
		for _, i := range g.members {
			grouped[i] = true
		}
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].members) < len(out[j].members) })
	return out, grouped
}
