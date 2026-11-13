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
	// No: the Subject is not in the Target's Error Set: the upward pass,
	// an upper bound, does not reach the Target's error result.
	No Verdict = iota
	// Yes: an entry of the Error Set is the Subject, with a Propagation
	// Path as witness.
	Yes
	// Maybe: the Subject is only in possibly handled entries, or the
	// upward pass reaches the Target and the Error Set does not witness
	// it: it could come through a Carrier.
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

// Carrier is an entry of the Error Set that the Subject could come
// through: an Unresolved entry whose place is marked by the upward pass,
// or a resolved entry whose leaf (or a Wrap of its chain) may hold the
// Subject without being it: a Sentinel whose variable holds a Wrap of the
// Subject, a value of a type that wraps it or reports it from an Is
// method, a value that may be equal to it.
type Carrier struct {
	// Entry is the entry.
	Entry errset.Entry
	// Unresolved reports whether the entry's leaf is Unresolved.
	Unresolved bool
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
	// Open are the loaded packages of a program without a main package
	// (see mark.Options.Open): code outside the program calls them. An
	// entry that depends on the caller is then always a Carrier. Nil
	// means the program is whole.
	Open []*ssa.Package
}

// MembershipResult is the outcome of a Membership Query.
type MembershipResult struct {
	// Result is the Error Set of the downward Query.
	*Result
	// Subject is the error asked about.
	Subject mark.Subject
	// Verdict is the answer.
	Verdict Verdict
	// Matches are the entries that are the Subject, in output order. With
	// a Maybe Verdict they are all possibly handled.
	Matches []errset.Entry
	// Carriers are the entries the Subject could come through, nearest
	// the Target first. They are set when there are no Matches.
	Carriers []Carrier
	// Reached reports that the upward pass marked the Target's error
	// result.
	Reached bool
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
//   - an entry that is the Subject (its leaf is the Sentinel or has the
//     Concrete Type, or, for a Concrete Type, a Wrap of its chain has it)
//     and is not possibly handled: Yes;
//   - only possibly handled entries that are the Subject: Maybe;
//   - the upward pass does not reach the Target's error result: No,
//     whatever Unresolved entries the Error Set has (but Maybe if the
//     program is open and an entry depends on the caller);
//   - it does: Maybe, with the Carriers. The Error Set is no proof that
//     the Subject is absent: there may be no Carrier at all.
func Membership(ctx context.Context, prog *load.Program, tgt *target.Target, opts MembershipOptions) (*MembershipResult, error) {
	res, err := Run(ctx, prog, tgt, opts.Options)
	if err != nil {
		return nil, err
	}
	m := &MembershipResult{Result: res, Subject: opts.Subject}
	match := matcher(opts.Subject)
	handled := 0
	for _, e := range res.Entries {
		if !match(res.Table, e) {
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
	marks, err := mark.RunWith(ctx, prog.SSA, opts.Upper, opts.Subject, mark.Options{Open: opts.Open})
	if err != nil {
		return nil, err
	}
	m.MarkRan, m.MarkTime, m.MarkStats = true, time.Since(start), marks.Stats()
	m.Reached = targetMarked(marks, tgt)
	c := carriers{marks: marks, tbl: res.Table, prog: prog.SSA, open: len(opts.Open) > 0, reached: m.Reached}
	for _, e := range res.Entries {
		if ok, trace := c.carrier(e); ok {
			m.Carriers = append(m.Carriers, Carrier{
				Entry:      e,
				Unresolved: res.Table.Get(e.Leaf).Kind == errset.Unresolved,
				Trace:      trace,
			})
		}
	}
	sort.SliceStable(m.Carriers, func(i, j int) bool {
		return len(m.Carriers[i].Entry.Path) < len(m.Carriers[j].Entry.Path)
	})
	if m.Reached || len(m.Carriers) > 0 {
		m.Verdict = Maybe
	}
	return m, nil
}

// matcher returns the test of whether an entry is the Subject: its leaf
// is a Sentinel with the same canonical variable; or its leaf is a
// Concrete Type (or a Sentinel whose value has a known type) identical to
// the Subject's type, or a Wrap of its chain has that type (errors.As
// finds a wrapper as it finds a leaf).
func matcher(subj mark.Subject) func(*errset.Table, errset.Entry) bool {
	if g := subj.Sentinel; g != nil {
		obj, _ := g.Object().(*types.Var)
		canon := obj
		if c := flow.NewSentinels().Info(g).Canon; c != nil {
			if cv, ok := c.Object().(*types.Var); ok {
				canon = cv
			}
		}
		return func(tbl *errset.Table, e errset.Entry) bool {
			v := tbl.Get(e.Leaf)
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
	return func(tbl *errset.Table, e errset.Entry) bool {
		v := tbl.Get(e.Leaf)
		switch v.Kind {
		case errset.Concrete, errset.Sentinel:
			if v.Type != nil && types.Identical(v.Type, subj.Type) {
				return true
			}
		}
		for _, wid := range e.Chain {
			if w := tbl.Get(wid); w.Type != nil && types.Identical(w.Type, subj.Type) {
				return true
			}
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

// carriers decides which entries of an Error Set are Carriers.
type carriers struct {
	marks *mark.Result
	tbl   *errset.Table
	prog  *ssa.Program
	// open: the program is open (see MembershipOptions.Open).
	open bool
	// reached: the upward pass marked the Target's error result.
	reached bool
}

// carrier reports whether the entry e is a Carrier, with the trace of the
// Mark that makes it one. If the upward pass did not reach the Target,
// only an entry that depends on the caller of an open program is one.
func (c carriers) carrier(e errset.Entry) (bool, []mark.Step) {
	v := c.tbl.Get(e.Leaf)
	if c.open && v.Kind == errset.Unresolved && v.Unresolved == errset.DependsOnCaller {
		// A caller outside the program may pass the Subject.
		if site, ok := v.Site.(ssa.Value); ok {
			return true, c.marks.Trace(site)
		}
		return true, nil
	}
	if !c.reached {
		return false, nil
	}
	if e.Truncated {
		// What lies below the cut is not known.
		return true, nil
	}
	if v.Kind == errset.Unresolved {
		switch v.Unresolved {
		case errset.FixpointLimit, errset.NoInstances:
			return true, nil
		}
		site, ok := v.Site.(ssa.Value)
		if !ok {
			// A go or defer call, or no site: not a value.
			return true, nil
		}
		if !c.marks.Marked(site) {
			return false, nil
		}
		return true, c.marks.Trace(site)
	}
	// A resolved entry: its leaf may hold the Subject without being it.
	switch v.Kind {
	case errset.Sentinel:
		if g := c.global(v.Var); g == nil {
			return true, nil
		} else if c.marks.GlobalMarked(g) {
			return true, c.marks.TraceGlobal(g)
		}
	case errset.Concrete:
		if v.Type != nil && c.marks.TypeMarked(v.Type) {
			return true, c.marks.TraceType(v.Type)
		}
	}
	switch site := v.Site.(type) {
	case *ssa.Function, *ssa.Global, *ssa.Const, *ssa.Builtin:
	case ssa.Value:
		if c.marks.Marked(site) {
			return true, c.marks.Trace(site)
		}
	}
	for _, wid := range e.Chain {
		if w := c.tbl.Get(wid); w.Type != nil && c.marks.TypeMarked(w.Type) {
			return true, c.marks.TraceType(w.Type)
		}
	}
	return false, nil
}

// global returns the SSA variable of the package-level variable v.
func (c carriers) global(v *types.Var) *ssa.Global {
	if v == nil || v.Pkg() == nil {
		return nil
	}
	pkg := c.prog.Package(v.Pkg())
	if pkg == nil {
		return nil
	}
	return pkg.Var(v.Name())
}
