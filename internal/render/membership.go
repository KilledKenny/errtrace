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

// CarrierEntry is a Carrier: an Unresolved entry the Subject of a
// Membership Query could come through, with the trace of its Mark.
type CarrierEntry struct {
	// Entry is the Unresolved entry.
	Entry errset.Entry
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
	// Matches are the entries whose leaf is the Subject.
	Matches []errset.Entry
	// Carriers are the Carriers of a maybe Verdict without Matches.
	Carriers []CarrierEntry
	// Holes is how many entries of the Error Set could be Carriers.
	Holes int
	// Reached says that the upward pass marked the Target's error result.
	Reached bool
	// Incomplete says that the Verdict is maybe because the Target is not
	// reachable from the roots of the program.
	Incomplete bool
}

// Membership writes the Verdict of a Membership Query as text. The first
// line is one of
//
//	<target-span>: <X> in Error Set of <target>: yes
//	<target-span>: <X> in Error Set of <target>: no
//	<target-span>: <X> in Error Set of <target>: maybe: <K> of <N> unresolved could carry it
//	<target-span>: <X> in Error Set of <target>: maybe: possibly handled
//
// After a yes, and after a maybe that is possibly handled, come the
// entries whose leaf is X, as Text prints entries (never grouped). After
// the other maybe come the Carriers, nearest the Target first, each as
// Text prints an Unresolved entry plus the trace of its Mark, when there
// is one:
//
//	<entry-span>: unresolved(<kind>): <detail>
//		path (origin → target):
//			...
//		marked via (origin of <X> → here):
//			<span>: <step description>
//
// A no is the header alone, or, when the upward pass reached the Target
// but no entry is a Carrier, the header and
//
//	<TAB><X> reaches the target only through values the Error Set resolved without it
//
// A maybe with 0 Carriers is the answer for a Target that is not
// reachable from main or init, in place of that second kind of no; it is
// followed by
//
//	<TAB><X> reaches the target, which is not reachable from main/init: the Candidates of its dynamic calls may be incomplete
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
		fmt.Fprintf(&b, ": %d of %d unresolved could carry it\n", len(in.Carriers), in.Holes)
		if in.Incomplete {
			fmt.Fprintf(&b, "\t%s reaches the target, which is not reachable from main/init: the Candidates of its dynamic calls may be incomplete\n", in.Subject)
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
	case in.Verdict == "yes":
		b.WriteByte('\n')
		for _, e := range in.Matches {
			in.entry(&b, e)
		}
	default:
		b.WriteByte('\n')
		if in.Reached {
			fmt.Fprintf(&b, "\t%s reaches the target only through values the Error Set resolved without it\n", in.Subject)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
