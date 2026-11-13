// Package mark is the upward pass of a Membership Query: from every place
// that produces the error X (the Subject), it pushes a Mark, "this value or
// memory location may hold X", forward through the whole program. Where the
// downward pass (package summary) is a lower bound that gives up at
// Unresolved places, the upward pass is an upper bound: when it is unsure it
// keeps the Mark, so what it does not reach cannot hold X.
//
// # What a Mark means
//
// A marked value may be X itself, or a Wrap whose Wrap Chain reaches X.
// Only values that can be errors are marked: interface values and values of
// concrete types that implement error. What a struct, slice, map, channel
// or pointer holds is not a Mark on that value but on a memory location:
//
//   - a local variable (an Alloc) and the closures that capture it;
//   - a package-level variable;
//   - a field of a struct type, program-wide (every value of the type
//     shares it);
//   - the memory of a type T reached through pointers and slice or array
//     elements, program-wide (type-based aliasing), which also stands for
//     every variable and field whose address is taken;
//   - the keys and the values of a map type, the elements of channels of a
//     type, the value of a panic, and the contents of sync/atomic values.
//
// A type that implements error and whose Unwrap method can return a marked
// value, or that has an Unwrap, Is or As method and holds a marked value in
// a field, is a wrapper of X: every value of that type is marked, as every
// value of a Concrete Type Subject is.
//
// # Calls
//
// Marks carry tags, so that a helper does not leak a Mark to all its
// callers: Free (true in any calling context) or Param(i) (true when
// parameter i of the enclosing function holds X). A marked argument seeds
// the callee's parameter with Param(i); a result marked Param(i) marks the
// call only where argument i is marked, and a result marked Free marks
// every call. Memory is context-insensitive: a Mark stored to a location is
// Free. The callees of a call come from the Callees given to Run.
//
// The world is closed: only the functions of the loaded program run, and a
// parameter holds X only if some call in the program passes it.
//
// # Known limits
//
//   - Reflection, unsafe, cgo and assembly can move X where the pass cannot
//     see. Modelled are only: errors.As (its target is marked), the
//     contents of sync/atomic.Value and atomic.Pointer, a value passed to
//     reflect.ValueOf (it marks every result of reflect.Value.Interface),
//     and a function without a body (its results are marked when an
//     argument is).
//   - A type with a custom Is or As method can match X without holding it.
//   - Guards are ignored upward by design; the downward pass decides them.
//   - Type-keyed locations are coarse: expect a false "maybe", never a
//     false "no".
//   - A value stored into the Sentinel's variable by the program is followed
//     forward from the store only, not back to its other copies.
//   - The bodies of generic functions are not scanned, only their
//     instances.
package mark

import (
	"context"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// Subject is the error X of a Membership Query. Exactly one field is set.
type Subject struct {
	// Sentinel is the package-level variable of a Sentinel Subject.
	Sentinel *ssa.Global
	// Type is the type of a Concrete Type Subject.
	Type types.Type
}

// String names the Subject as output does: <pkgpath>.<Name> for a
// Sentinel, the type with full package paths for a Concrete Type.
func (s Subject) String() string {
	if s.Sentinel != nil {
		if s.Sentinel.Pkg != nil {
			return s.Sentinel.Pkg.Pkg.Path() + "." + s.Sentinel.Name()
		}
		return s.Sentinel.Name()
	}
	if s.Type != nil {
		return types.TypeString(s.Type, nil)
	}
	return "<no subject>"
}

// Callees gives the call edges the upward pass follows. They must be an
// upper bound: a missing edge can make the pass miss a Mark.
type Callees interface {
	// Callees returns the functions that may run at site, in a
	// deterministic order.
	Callees(site ssa.CallInstruction) []*ssa.Function
	// Callers returns the call sites whose Callees include fn, in any
	// order.
	Callers(fn *ssa.Function) []ssa.CallInstruction
}

// Stats counts what one upward pass did.
type Stats struct {
	// Functions is how many functions with bodies the pass scanned.
	Functions int
	// MarkedFunctions is how many of them have a marked value.
	MarkedFunctions int
	// Values is how many values carry a Mark.
	Values int
	// Locations is how many memory locations carry a Mark.
	Locations int
}

// Result is the outcome of the upward pass for one Subject.
type Result struct {
	a *analysis
}

// Run runs the upward pass for subj over every function of prog that has
// a body, following the call edges of res.
func Run(ctx context.Context, prog *ssa.Program, res Callees, subj Subject) (*Result, error) {
	a := newAnalysis(prog, res, subj)
	if err := a.run(ctx); err != nil {
		return nil, err
	}
	return &Result{a: a}, nil
}

// Marked reports whether v may hold the Subject: v carries a Mark (for a
// tuple, any of its components does), its static type is the Subject's
// type or a wrapper of the Subject, or v is an address or a composite
// value whose memory carries a Mark. A value the pass cannot place is
// reported as marked.
func (r *Result) Marked(v ssa.Value) bool {
	a := r.a
	if v == nil {
		return true
	}
	if fn := parentOf(v); fn != nil {
		if _, ok := a.fnIndex[fn]; !ok {
			// A function the pass did not scan is not known.
			return true
		}
	}
	t := v.Type()
	if tup, ok := t.(*types.Tuple); ok {
		for i := range tup.Len() {
			ct := tup.At(i).Type()
			if a.explicit(key{v, int32(i)}) || a.implicitQuery(ct) || (!a.markable(ct) && a.holdsAny(ct)) {
				return true
			}
		}
		return false
	}
	if a.explicit(key{v, 0}) || a.implicitQuery(t) {
		return true
	}
	if a.markable(t) {
		return false
	}
	switch v.(type) {
	case *ssa.Alloc, *ssa.FreeVar, *ssa.Global, *ssa.FieldAddr, *ssa.IndexAddr:
		locs, n := a.addrLocs(v)
		for _, l := range locs[:n] {
			if a.locMarked(l) {
				return true
			}
		}
	}
	return a.holdsAny(t)
}

// ResultMarked reports whether result index of fn may hold the Subject, in
// any calling context or when a caller of the program passes the Subject.
func (r *Result) ResultMarked(fn *ssa.Function, index int) bool {
	a := r.a
	if fn == nil || index < 0 || index >= fn.Signature.Results().Len() {
		return true
	}
	if rs := a.rets[fn]; index < len(rs) && rs[index].t != 0 {
		return true
	}
	if a.implicitQuery(fn.Signature.Results().At(index).Type()) {
		return true
	}
	// A function the pass did not scan is not known.
	_, scanned := a.fnIndex[fn]
	return !scanned
}

// SiteResultMarked reports whether result index of the call at site may
// hold the Subject.
func (r *Result) SiteResultMarked(site ssa.CallInstruction, index int) bool {
	a := r.a
	if site == nil {
		return true
	}
	res := site.Common().Signature().Results()
	if index < 0 || index >= res.Len() {
		return true
	}
	if a.implicitQuery(res.At(index).Type()) {
		return true
	}
	if _, ok := a.fnIndex[site.Parent()]; !ok {
		return true
	}
	if v := site.Value(); v != nil {
		k := key{v, 0}
		if res.Len() > 1 {
			k.idx = int32(index)
		}
		return a.explicit(k)
	}
	return a.siteResultFromCallees(site, index)
}

// Stats returns the counts of the pass.
func (r *Result) Stats() Stats {
	a := r.a
	st := Stats{Functions: len(a.fns)}
	fns := make(map[*ssa.Function]bool)
	for k, in := range a.marks {
		if in.t == 0 {
			continue
		}
		st.Values++
		if fn := parentOf(k.v); fn != nil {
			fns[fn] = true
		}
	}
	st.MarkedFunctions = len(fns)
	for _, li := range a.locs {
		if li.marked {
			st.Locations++
		}
	}
	return st
}

func parentOf(v ssa.Value) *ssa.Function {
	switch v := v.(type) {
	case ssa.Instruction:
		return v.Parent()
	case *ssa.Parameter:
		return v.Parent()
	case *ssa.FreeVar:
		return v.Parent()
	}
	return nil
}
