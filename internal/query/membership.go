package query

import (
	"context"
	"go/types"
	"sort"
	"time"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/errset"
	"github.com/KilledKenny/errtrace/internal/flow"
	"github.com/KilledKenny/errtrace/internal/load"
	"github.com/KilledKenny/errtrace/internal/mark"
	"github.com/KilledKenny/errtrace/internal/target"
)

// Verdict is the answer of a Membership Query.
type Verdict int

const (
	// No: the Subject is not in the Target's Error Set.
	No Verdict = iota
	// Yes: an entry of the Error Set is the Subject, with a Propagation
	// Path as witness.
	Yes
	// Maybe: the Subject is only in possibly handled entries, or it could
	// come through an Unresolved entry (a Carrier).
	Maybe
)

// String returns "no", "yes" or "maybe".
func (v Verdict) String() string {
	switch v {
	case Yes:
		return "yes"
	case Maybe:
		return "maybe"
	}
	return "no"
}

// Carrier is an Unresolved entry whose place is marked by the upward pass,
// so that the Subject could come through it.
type Carrier struct {
	// Entry is the Unresolved entry.
	Entry errset.Entry
	// Trace is how the Mark reached the entry's place, from an origin of
	// the Subject; it is empty for an entry that is a Carrier only
	// because its place cannot be mapped to a value.
	Trace []mark.Step
}

// MembershipOptions configures a Membership Query.
type MembershipOptions struct {
	// Options configures the downward Query.
	Options
	// Subject is the error asked about.
	Subject mark.Subject
	// Upper gives the call edges of the upward pass (resolve.Upper).
	Upper mark.Callees
	// WholeProgram says that the Target is reachable from the roots of
	// the program, so that the Candidates of the downward Query are
	// complete. If it is not, an Error Set without Carriers does not
	// prove that the Subject is absent.
	WholeProgram bool
}

// MembershipResult is the outcome of a Membership Query.
type MembershipResult struct {
	// Result is the Error Set of the downward Query.
	*Result
	// Subject is the error asked about.
	Subject mark.Subject
	// Verdict is the answer.
	Verdict Verdict
	// Matches are the entries whose leaf is the Subject, in output order.
	// With a Maybe Verdict they are all possibly handled.
	Matches []errset.Entry
	// Carriers are the Unresolved entries the Subject could come through,
	// nearest the Target first. They are set when there are no Matches.
	Carriers []Carrier
	// Holes is how many entries of the Error Set are Unresolved or have a
	// truncated Wrap Chain: the entries that can be Carriers.
	Holes int
	// Reached reports that the upward pass marked the Target's error
	// result.
	Reached bool
	// Incomplete reports a Maybe Verdict without Carriers: the Subject
	// reaches the Target, and the Target is not reachable from the roots
	// of the program, so the Candidates the Error Set was built from may
	// be incomplete.
	Incomplete bool
	// MarkRan reports whether the upward pass ran; MarkTime is how long
	// it took and MarkStats what it did.
	MarkRan   bool
	MarkTime  time.Duration
	MarkStats mark.Stats
}

// Membership answers whether opts.Subject is in the Error Set of tgt. It
// runs the downward Query; if no entry settles the answer, it runs the
// upward pass (package mark) and combines the two:
//
//   - an entry that is the Subject and is not possibly handled: Yes;
//   - only possibly handled entries that are the Subject: Maybe;
//   - the upward pass does not reach the Target's error result: No,
//     whatever Unresolved entries the Error Set has;
//   - it does, and some Unresolved entries are Carriers: Maybe;
//   - it does, and none is a Carrier: No (the downward Query was complete
//     where the Subject could arrive), unless the program is not whole
//     (see MembershipOptions.WholeProgram): then Maybe.
func Membership(ctx context.Context, prog *load.Program, tgt *target.Target, opts MembershipOptions) (*MembershipResult, error) {
	res, err := Run(ctx, prog, tgt, opts.Options)
	if err != nil {
		return nil, err
	}
	m := &MembershipResult{Result: res, Subject: opts.Subject}
	match := matcher(opts.Subject)
	handled := 0
	for _, e := range res.Entries {
		v := res.Table.Get(e.Leaf)
		if v.Kind == errset.Unresolved || e.Truncated {
			m.Holes++
		}
		if !match(v) {
			continue
		}
		m.Matches = append(m.Matches, e)
		if len(e.PossiblyHandled) > 0 {
			handled++
		}
	}
	if len(m.Matches) > 0 {
		m.Verdict = Yes
		if handled == len(m.Matches) {
			m.Verdict = Maybe
		}
		return m, nil
	}

	start := time.Now()
	marks, err := mark.Run(ctx, prog.SSA, opts.Upper, opts.Subject)
	if err != nil {
		return nil, err
	}
	m.MarkRan, m.MarkTime, m.MarkStats = true, time.Since(start), marks.Stats()
	m.Reached = targetMarked(marks, tgt)
	if !m.Reached {
		return m, nil
	}
	for _, e := range res.Entries {
		if ok, trace := carrier(marks, res.Table, e); ok {
			m.Carriers = append(m.Carriers, Carrier{Entry: e, Trace: trace})
		}
	}
	sort.SliceStable(m.Carriers, func(i, j int) bool {
		return len(m.Carriers[i].Entry.Path) < len(m.Carriers[j].Entry.Path)
	})
	switch {
	case len(m.Carriers) > 0:
		m.Verdict = Maybe
	case !opts.WholeProgram:
		m.Verdict = Maybe
		m.Incomplete = true
	}
	return m, nil
}

// matcher returns the test of whether a leaf value is the Subject: a
// Sentinel with the same canonical variable, or a Concrete Type (or a
// Sentinel whose value has a known type) identical to the Subject's type.
func matcher(subj mark.Subject) func(errset.Value) bool {
	if g := subj.Sentinel; g != nil {
		obj, _ := g.Object().(*types.Var)
		canon := obj
		if c := flow.NewSentinels().Info(g).Canon; c != nil {
			if cv, ok := c.Object().(*types.Var); ok {
				canon = cv
			}
		}
		return func(v errset.Value) bool {
			if v.Kind != errset.Sentinel || obj == nil {
				return false
			}
			vc := v.Canon
			if vc == nil {
				vc = v.Var
			}
			return v.Var == obj || vc == canon
		}
	}
	return func(v errset.Value) bool {
		switch v.Kind {
		case errset.Concrete, errset.Sentinel:
			return v.Type != nil && types.Identical(v.Type, subj.Type)
		}
		return false
	}
}

// targetMarked reports whether the upward pass marked an error result of
// the Target: of its functions, or of its call site.
func targetMarked(marks *mark.Result, tgt *target.Target) bool {
	if len(tgt.Funcs) == 0 {
		// Nothing to look at (a generic Target without instances).
		return true
	}
	for _, fn := range tgt.Funcs {
		switch tgt.Kind {
		case target.Declaration:
			for _, idx := range flow.ErrorResults(fn.Signature) {
				if marks.ResultMarked(fn, idx) {
					return true
				}
			}
		case target.CallSite:
			site := tgt.SiteByFunc[fn]
			if site == nil {
				continue
			}
			for _, idx := range flow.ErrorResults(site.Common().Signature()) {
				if marks.SiteResultMarked(site, idx) {
					return true
				}
			}
		default:
			return true
		}
	}
	return false
}

// carrier reports whether the entry e is a Carrier: an Unresolved entry
// whose place is marked, with the trace of the Mark. An entry whose place
// cannot be mapped to a value (a fixpoint limit, a generic Target without
// instances, a truncated Wrap Chain) is a Carrier.
func carrier(marks *mark.Result, tbl *errset.Table, e errset.Entry) (bool, []mark.Step) {
	v := tbl.Get(e.Leaf)
	if v.Kind != errset.Unresolved {
		return e.Truncated, nil
	}
	if e.Truncated {
		return true, nil
	}
	switch v.Unresolved {
	case errset.FixpointLimit, errset.NoInstances:
		return true, nil
	}
	site, ok := v.Site.(ssa.Value)
	if !ok {
		// A go or defer call, or no site: not a value.
		return true, nil
	}
	if !marks.Marked(site) {
		return false, nil
	}
	return true, marks.Trace(site)
}
