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
	// FloodOnly says that the Subject does not reach the store itself:
	// it stores what it reads from memory that the other stores are
	// taken to write. Such stores come after the others.
	FloodOnly bool
}

// maxNotFollowed is how many stores the "not followed" section lists, in
// both of its parts together, unless Full is set.
const maxNotFollowed = 5

// MembershipInput is everything the renderer needs to print the Verdict
// of a Membership Query.
type MembershipInput struct {
	// Input gives the Target, the Table and the Locator; its Entries and
	// Fanout are not used.
	Input
	// Subject names the error asked about: <pkgpath>.<Name> or a type.
	Subject string
	// Verdict is "yes", "no" or "maybe". A "no" with NotFollowed is
	// printed as "no, apart from reflection".
	Verdict string
	// Matches are the entries that are the Subject.
	Matches []errset.Entry
	// Carriers are the Carriers of a maybe Verdict without Matches.
	Carriers []CarrierEntry
	// TargetTrace is how the Mark reached the Target's error result; it
	// is printed when there is no Carrier.
	TargetTrace []TraceStep
	// NotFollowed are the stores through reflection that the Subject
	// reaches, that the upward pass did not follow and that can matter
	// to the Target; they are printed after a no and after a maybe with
	// Carriers.
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
//	<target-span>: <X> in Error Set of <target>: no, apart from reflection
//	<target-span>: <X> in Error Set of <target>: maybe: <K> entries could carry it (<U> unresolved)
//	<target-span>: <X> in Error Set of <target>: maybe: 1 entry could carry it (<U> unresolved)
//	<target-span>: <X> in Error Set of <target>: maybe: no entry of the Error Set shows how
//	<target-span>: <X> in Error Set of <target>: maybe: possibly handled
//	<target-span>: <X> in Error Set of <target>: maybe: only through a field location
//	<target-span>: <X> in Error Set of <target>: maybe: possibly handled, or only through a field location
//
// After a yes, and after a maybe whose entries are possibly handled or
// known only through a field location (tagged [via field ...]), come the
// entries that are X, as Text prints entries (never grouped). After the
// maybe with K Carriers come those Carriers, nearest the Target first,
// each as Text prints the entry plus the trace of its Mark, when there is
// one:
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
// that the traces of all of them start with, once (there may be none),
// then one line per entry. No entry of a group is shown with a step its
// own trace does not have; Full prints every trace whole. A pivot is the
// same by what it says, wherever it happens: the header has the span of
// the group's first, and, if the pivots of the group are at P > 1
// different places, says so.
//
//	<pivot-span>: group of <M> entries via: <pivot step description>[ (at <P> places)]
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
// A maybe without Carriers means that X can reach the Target (the upward
// pass, an upper bound, says so) while no entry of the Error Set shows
// how; it is followed by how the Mark reached the Target, when the pass
// can tell:
//
//	<TAB>marked via (origin of <X> → target):
//	<TAB><TAB><span>: <step description>
//
// A no is the header alone: X cannot reach the Target, whatever
// Unresolved entries its Error Set has.
//
// The upward pass does not follow a store through reflection whose
// destination it cannot tell (or, into fresh memory, whose destination it
// loses sight of). It records the store if the value stored carries a
// Mark. A maybe that is not possibly handled ends with the recorded
// stores. After a no they are listed only if they can matter to the
// Target: the upward pass is run again with each of them taken to write
// every memory location a store through reflection could write, and
// every read through reflection taken to read what they wrote, and if
// that reaches the Target the no becomes "no, apart from reflection" and
// is followed by every recorded store, the N stores that X reaches, and
// then, if there are any, by the M stores that only that second pass
// records: X does not reach them, they store what they read from memory
// that the recorded stores are taken to write (a store of what another
// store wrote). If it does not reach the Target, the no is plain and
// nothing is listed. The stores of each part come in the order of the
// pass; of all of them, the first 5 are printed (all with Full), and a
// part that is cut short ends with how many more it has:
//
//	<TAB>not followed: <X> reaches <N> reflective store[s] whose destination is unknown or only partly followed:
//	<TAB><TAB><span>: in <function>
//	<TAB><TAB>… and <K> more
//	<TAB>also, once those are taken to write anywhere, <M> more:
//	<TAB><TAB><span>: in <function>
//	<TAB><TAB>… and <K> more
//
// The Verdict does not cover what those stores write.
func Membership(w io.Writer, in MembershipInput) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s in Error Set of %s: %s", in.TargetSpan, in.Subject, in.TargetName, in.Verdict)
	switch {
	case in.Verdict == "maybe" && len(in.Matches) > 0:
		b.WriteString(": " + uncertain(in.Matches) + "\n")
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
		if len(in.Carriers) == 0 {
			b.WriteString(": no entry of the Error Set shows how\n")
			if len(in.TargetTrace) > 0 {
				fmt.Fprintf(&b, "\tmarked via (origin of %s → target):\n", in.Subject)
				in.steps(&b, in.TargetTrace)
			}
		} else {
			fmt.Fprintf(&b, ": %d %s could carry it (%d unresolved)\n", len(in.Carriers), entries, unresolved)
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
			pivot := first.Trace[g.pivots[0]]
			fmt.Fprintf(&b, "%s: group of %d entries via: %s", in.locate(pivot.Site), len(g.members), pivot.Desc)
			if places := in.places(g); places > 1 {
				fmt.Fprintf(&b, " (at %d places)", places)
			}
			b.WriteByte('\n')
			if n := in.shared(g); n > 0 {
				fmt.Fprintf(&b, "\tmarked via (origin of %s → here):\n", in.Subject)
				in.steps(&b, first.Trace[:n])
			}
			in.members(&b, g)
		}
		in.notFollowed(&b)
	case in.Verdict == "no":
		if len(in.NotFollowed) > 0 {
			b.WriteString(", apart from reflection")
		}
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
	if len(in.NotFollowed) == 0 {
		return
	}
	// The stores the Subject reaches, then those it does not.
	var reached, flooded []ReflectStore
	for _, rs := range in.NotFollowed {
		if rs.FloodOnly {
			flooded = append(flooded, rs)
		} else {
			reached = append(reached, rs)
		}
	}
	left := len(in.NotFollowed)
	if !in.Full && left > maxNotFollowed {
		left = maxNotFollowed
	}
	list := func(part []ReflectStore) {
		shown := min(len(part), left)
		left -= shown
		for _, rs := range part[:shown] {
			fmt.Fprintf(b, "\t\t%s: in %s\n", in.locate(rs.Site), rs.Function)
		}
		if shown < len(part) {
			fmt.Fprintf(b, "\t\t… and %d more\n", len(part)-shown)
		}
	}
	stores := "stores"
	if len(reached) == 1 {
		stores = "store"
	}
	fmt.Fprintf(b, "\tnot followed: %s reaches %d reflective %s whose destination is unknown or only partly followed:\n", in.Subject, len(reached), stores)
	list(reached)
	if len(flooded) > 0 {
		fmt.Fprintf(b, "\talso, once those are taken to write anywhere, %d more:\n", len(flooded))
		list(flooded)
	}
}

// steps writes the steps of a trace, two tabs deep.
func (in MembershipInput) steps(b *strings.Builder, trace []TraceStep) {
	for _, s := range trace {
		fmt.Fprintf(b, "\t\t%s: %s\n", in.locate(s.Site), s.Desc)
	}
}

// shared returns the number of steps that the traces of all the Carriers
// of g start with: the same description at the same place.
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
	return n
}

// places returns at how many different places the pivots of the Carriers
// of g are.
func (in MembershipInput) places(g pivotGroup) int {
	seen := make(map[string]bool)
	for j, i := range g.members {
		seen[in.locate(in.Carriers[i].Trace[g.pivots[j]].Site)] = true
	}
	return len(seen)
}

// pivotGroup is the Carriers whose traces pass the same pivot.
type pivotGroup struct {
	// members are the indices of the Carriers, in order; pivots are the
	// indices of the pivot step in the trace of each.
	members []int
	pivots  []int
}

// pivotGroups groups the Carriers by the pivot of their trace: its first
// wide step, by its description. It returns the groups of two or more,
// smallest first (then in the order of their first Carriers), and which
// Carriers are grouped. With Full nothing is grouped.
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
				g = &pivotGroup{}
				byKey[key] = g
				all = append(all, g)
			}
			g.members = append(g.members, i)
			g.pivots = append(g.pivots, j)
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

// uncertain says why the entries that are the Subject do not make a yes:
// each is possibly handled, or is known only through a field location.
func uncertain(matches []errset.Entry) string {
	handled, via := false, false
	for _, e := range matches {
		if e.ViaLocation != "" {
			via = true
		} else if len(e.PossiblyHandled) > 0 {
			handled = true
		}
	}
	switch {
	case handled && via:
		return "possibly handled, or only through a field location"
	case via:
		return "only through a field location"
	}
	return "possibly handled"
}
