package render

import (
	"fmt"
	"io"
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
}

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
}

// Membership writes the Verdict of a Membership Query as text. The first
// line is one of
//
//	<target-span>: <X> in Error Set of <target>: yes
//	<target-span>: <X> in Error Set of <target>: no
//	<target-span>: <X> in Error Set of <target>: maybe: <K> entries could carry it (<U> unresolved)
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
// A maybe with 0 Carriers means that X can reach the Target (the upward
// pass, an upper bound, says so) while no entry of the Error Set shows
// how; it is followed by
//
//	<TAB><X> may reach the target, but no entry of the Error Set shows how
//
// A no is the header alone: X cannot reach the Target, whatever
// Unresolved entries its Error Set has.
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
		fmt.Fprintf(&b, ": %d entries could carry it (%d unresolved)\n", len(in.Carriers), unresolved)
		if len(in.Carriers) == 0 {
			fmt.Fprintf(&b, "\t%s may reach the target, but no entry of the Error Set shows how\n", in.Subject)
		}
		for _, c := range in.Carriers {
			in.entry(&b, c.Entry)
			if len(c.Trace) == 0 {
				continue
			}
			fmt.Fprintf(&b, "\tmarked via (origin of %s → here):\n", in.Subject)
			for _, s := range c.Trace {
				fmt.Fprintf(&b, "\t\t%s: %s\n", in.locate(s.Site), s.Desc)
			}
		}
	default:
		b.WriteByte('\n')
		for _, e := range in.Matches {
			in.entry(&b, e)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
