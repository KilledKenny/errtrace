// Package mark is the upward pass of a Membership Query: from every place
// that produces the error X (the Subject), it pushes a Mark, "this value or
// memory location may hold X", forward through the whole program. Where the
// downward pass (package summary) is a lower bound that gives up at
// Unresolved places, the upward pass is an upper bound: when it is unsure it
// keeps the Mark, so what it does not reach cannot hold X.
//
// # What a Mark means
//
// A marked value may be X itself, or an error that errors.Is (for a
// Sentinel) or errors.As (for a Concrete Type) finds X in. Only values that
// can be errors are marked: interface values, values of concrete types that
// implement error, and reflect.Values (which carry what they hold). What a
// struct, slice, map, channel or pointer holds is not a Mark on that value
// but on a memory location:
//
//   - a local variable (an Alloc) and the closures that capture it;
//   - a package-level variable;
//   - a field of a struct type, program-wide (every value of the type
//     shares it);
//   - the memory of a type T reached through pointers and slice or array
//     elements, program-wide (type-based aliasing), which also stands for
//     every variable and field whose address is taken;
//   - the keys and the values of a map type, the elements of channels of a
//     type, the value of the panics raised in a function, and the contents
//     of sync/atomic values;
//   - the argument array of a variadic call that can run one function
//     only, which is the memory of that function's slice parameter alone
//     (and of the parameters it is passed on to) as long as the function
//     only reads it.
//
// # Seeds
//
// For a Sentinel: every load of its variable, of the variable it is an
// alias of, and of the variables it is stored to; every value stored into
// it; and every value of a type that may be equal to its value without
// being it (a comparable non-pointer type, a nil pointer). For a Concrete
// Type: every value of that type.
//
// A type that can be an error is a wrapper of X, and every value of it is
// marked, when
//
//   - its Unwrap method can return a marked value;
//   - its Is method (for a Sentinel) or As method (for a Concrete Type)
//     may report X: it compares its target with a marked value, asks
//     errors.Is or errors.As about one, asserts the target to X's type, or
//     passes the target where the pass cannot see what is done with it
//     (then the type wraps every X);
//   - it has an Unwrap, Is or As method and holds a marked value in a
//     field (through structs nested by value) or in a slice, array, map,
//     channel or pointer field.
//
// # Calls
//
// Marks carry tags, so that a helper does not leak a Mark to all its
// callers: Free (true in any calling context) or Param(i) (true when
// parameter i of the enclosing function holds X). A marked argument seeds
// the callee's parameter with Param(i); a result marked Param(i) marks the
// call only where argument i is marked, and a result marked Free marks
// every call. Memory is context-insensitive: a Mark stored to a location is
// Free. The result of an Unwrap method is marked where its receiver is (a
// receiver that wraps X is always marked).
//
// The callees of a call come from the Callees given to Run, less the
// functions whose value is never taken at a call of a function value.
//
// A recover sees a panic only if the function that defers the recovering
// function reaches the function that panics over those call edges.
//
// # Guards and provenance
//
// One guard is applied, because it is the common proof that X does not
// come back: a Mark does not pass a use of v that is dominated by the
// false edge of errors.Is(v, X), for a Sentinel X whose variable is never
// reassigned. A comparison (v != X) excludes nothing: v may wrap X.
//
// The guard proves that v did not hold X when it was checked. That stays
// true only for a Mark v holds by value: v is X itself, or an immutable
// construction over such values (fmt.Errorf, errors.Join). A Mark held by
// type (v has a type that wraps or reports X, or comes from memory or code
// the pass does not see) may become true after the check, when the
// wrapper's content changes. Every Mark is in one or both of these two
// planes, and the guard drops the by-value plane only.
//
// # Which functions are scanned
//
// With Callees that say which functions the roots reach (a whole
// program), only what can run is scanned: those functions, the functions
// they refer to, and the callees of their calls (and every exported method
// if they call through reflect.Value). Otherwise every function is.
//
// Static calls of fmt.Errorf, errors.Join, errors.New and errtrace.Wrap are
// modelled (flow.Intrinsic) and their bodies are not scanned, so that
// fmt.Errorf("%v", err) does not mark anything; nor are the bodies of
// package reflect: a call of it gives back what it is passed, in the
// reflect.Values and empty interfaces it returns. (When the Subject is a
// type of one of those packages, the bodies are followed instead.)
//
// # Closed and open worlds
//
// By default the world is closed: only the functions of the loaded program
// run, and a parameter holds X only if some call in the program passes it.
// A dynamic call that no function of the program matches calls nil.
//
// With Options.Open, code outside the program may call the functions of
// the open packages and set their exported variables and fields. Then:
//
//   - the parameters of every function of an open package that is
//     exported, is a method or has its value taken are seeded: a parameter
//     that can be an error (or a function, or any interface) is marked, and
//     the memory that a composite parameter can hold marked values in is
//     marked (markHeld);
//   - function values are marked too, and a dynamic call whose function
//     value or receiver is marked may run code that is not loaded: its
//     results are marked;
//   - the loads of exported fields, and of exported variables, of the open
//     packages are marked. An exported variable named like a Sentinel
//     (Err..., EOF) that the program only initializes is the one exception:
//     it is taken not to be reassigned from outside, or every Sentinel of a
//     library could be every other.
//
// # Reflection
//
// Package reflect is modelled, coarsely: a reflect.Value carries the Mark
// of what it holds, a call of package reflect gives back what it is
// passed, a composite passed to it counts if it holds a marked value, a
// store through it (Set, SetMapIndex, Send, Append, Copy) marks the memory
// of the composite the destination was made from (or, if the pass cannot
// tell, all memory that values converted to interfaces can hold), and if
// the program calls through reflect.Value (Call, CallSlice, Method,
// MethodByName) the results, parameters and panics of the exported methods
// and of the functions whose value is taken (of the types the called
// Values were made from, when the pass can tell) are connected to those
// calls.
//
// # Known limits
//
//   - unsafe, cgo, assembly and //go:linkname can move X where the pass
//     cannot see. Modelled are only errors.As (its target is marked), the
//     contents of sync/atomic.Value and atomic.Pointer, and a function
//     without a body (its results are marked when an argument is). An
//     address converted to unsafe.Pointer is not followed.
//   - An Is or As method that decides from something other than its target
//     and the values it compares it with (a global flag, the error text)
//     is taken at its word only where the target is compared or passed on.
//   - An Is method that does not look at its target at all, compares it
//     with a constant, or can return true where no branch on the target
//     led, reports every error; so does one that hands the target to code
//     the pass cannot see.
//   - Type-keyed locations and whole-type wrappers are coarse: expect a
//     false "maybe", never a false "no".
//   - A value stored into the Sentinel's variable by the program is followed
//     forward from the store only, not back to its other copies.
//   - A value converted to another named type and back is followed only as
//     far as its Mark goes: a type assertion to a type that is neither the
//     Subject's nor a possible wrapper drops it.
//   - An internal package is not open (the CLI decides): the code that may
//     import it is taken to be loaded.
//   - The bodies of generic functions are not scanned, only their
//     instances.
package mark

import (
	"context"
	"fmt"
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

// Options configures the upward pass.
type Options struct {
	// Open are the packages of a program that is not whole (it was
	// loaded without a main package): code outside the loaded program
	// calls their exported functions and methods and writes their
	// exported variables and fields. The pass then treats as possibly
	// the Subject: the parameters of those functions (with their Param
	// tag, as if a caller passed it), the loads of exported fields of
	// their struct types, and the loads of their exported variables that
	// the program does not only initialize. Nil means a closed world.
	Open []*ssa.Package
}

// Run runs the upward pass for subj over the functions of prog,
// following the call edges of res, in a closed world.
func Run(ctx context.Context, prog *ssa.Program, res Callees, subj Subject) (*Result, error) {
	return RunWith(ctx, prog, res, subj, Options{})
}

// RunWith runs the upward pass for subj over the functions of prog,
// following the call edges of res. If res has a method
// Reachable(*ssa.Function) bool, only the functions that can run are
// scanned: those it reports reachable and what they reach over the call
// edges of res and through function values (and, if they call through
// reflect.Value, every exported method).
func RunWith(ctx context.Context, prog *ssa.Program, res Callees, subj Subject, opts Options) (*Result, error) {
	a := newAnalysis(prog, res, subj)
	if len(opts.Open) > 0 {
		a.open = make(map[*ssa.Package]bool)
		for _, p := range opts.Open {
			a.open[p] = true
		}
	}
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

// FromOutside reports whether v, the function value or receiver of a
// dynamic call, may come from code outside an open program (see
// Options.Open): it is a parameter or captured variable, an exported
// variable the program does not only initialize, an exported field, or
// something the pass does not place. It is false in a closed world, and
// for the unexported variables and fields that only loaded code sets.
func (r *Result) FromOutside(v ssa.Value) bool {
	a := r.a
	if a.open == nil {
		return false
	}
	switch x := v.(type) {
	case *ssa.UnOp:
		switch addr := x.X.(type) {
		case *ssa.Global:
			return a.openAddr(addr)
		case *ssa.FieldAddr:
			return a.openAddr(addr)
		}
	case *ssa.Field:
		if st := structOf(x.X.Type()); st != nil && x.Field < st.NumFields() {
			return a.openField(x.X.Type(), st, x.Field)
		}
	case *ssa.MakeClosure, *ssa.Function:
		return false
	}
	return true
}

// GlobalMarked reports whether the package-level variable g may hold the
// Subject (or, for a variable of a composite type, hold it in its
// memory).
func (r *Result) GlobalMarked(g *ssa.Global) bool {
	a := r.a
	if g == nil || a.openAddr(g) {
		return true
	}
	locs, n := a.addrLocs(g)
	for _, l := range locs[:n] {
		if a.locMarked(l) {
			return true
		}
	}
	elem := pointerElem(g.Type())
	if elem == nil {
		return true
	}
	if a.implicitQuery(elem) {
		return true
	}
	return !a.markable(elem) && a.holdsAny(elem)
}

// TraceGlobal returns how the Mark reached the variable g (see Trace).
func (r *Result) TraceGlobal(g *ssa.Global) []Step {
	a := r.a
	if g == nil {
		return nil
	}
	locs, n := a.addrLocs(g)
	for _, l := range locs[:n] {
		if a.locMarked(l) {
			return a.trace(ref{kind: refLoc, l: l})
		}
	}
	return nil
}

// TypeMarked reports whether every value of the concrete type t is
// marked: t is the Subject's type, a type whose values may equal the
// Subject, or a wrapper of the Subject.
func (r *Result) TypeMarked(t types.Type) bool {
	return t != nil && r.a.implicitQuery(t)
}

// TraceType returns why every value of type t is marked (see
// TypeMarked): the steps to the Mark that makes t a wrapper of the
// Subject, and a step for the type itself.
func (r *Result) TraceType(t types.Type) []Step {
	a := r.a
	if t == nil || !a.implicitQuery(t) {
		return nil
	}
	last := Step{Desc: fmt.Sprintf("a value of type %s is or may wrap %s", typeString(t), a.subj), Wide: true}
	if a.typeSeed(t) || !a.potentialWrapper(t) {
		return []Step{last}
	}
	a.heldBy = ref{}
	if !a.isWrapper(t) {
		return []Step{last}
	}
	return append(a.trace(a.heldBy), last)
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
		if li.marked != 0 {
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
