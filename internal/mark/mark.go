// Package mark is the upward pass of a Membership Query: from every place
// that produces the error X (the Subject), it pushes a Mark, "this value or
// memory location may hold X", forward through the whole program. Where the
// downward pass (package summary) is a lower bound that gives up at
// Unresolved places, the upward pass is an upper bound: when it is unsure it
// keeps the Mark, so what it does not reach cannot hold X.
//
// That is a proof for the ways a value travels that do not pass through
// package reflect. Reflection is followed where the pass can (see
// Reflection) and otherwise not: a "no" is no proof about a value that
// goes through package reflect in a way the pass does not model. Some of
// what it does not follow it can name (the stores listed as "not
// followed"); the rest it cannot (see Known limits).
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
// being a copy of it that the Marks follow: a comparable non-pointer
// type, a pointer type whose value may be nil, and a pointer type of
// which the program may hold the Sentinel's own pointer elsewhere. The
// last is so unless the Sentinel's value is a pointer to an allocation
// whose every use, from the allocation to the store into the variable,
// only fills it or passes it on towards the variable (var ErrX =
// errors.New("x"), var ErrX error = &T{...}); a function that stores,
// passes or captures the pointer before returning it (a registry) makes
// every pointer of the type a seed. So does a conversion, anywhere in
// the scanned program, of a pointer of another type to the type of the
// Sentinel's pointer ((*T)(p) for a p of type *plain, of a named pointer
// type, of unsafe.Pointer, or a slice converted to an array pointer):
// the Sentinel's pointer may have been converted to that other type,
// where a Mark does not follow it or a type assertion drops it, and come
// back. For a Concrete
// Type: every value of that type. Such a value is marked where it is
// converted to an interface, and where package reflect makes a value of a
// type it is told at run time (reflect.New(t), reflect.Zero(t),
// v.Convert(t)), unless the pass can tell that type
// (reflect.TypeOf(T{}), reflect.TypeFor[T]()) and no such type is in it.
//
// A type that can be an error is a wrapper of X, and every value of it is
// marked, when
//
//   - its Unwrap method can return a marked value;
//   - its Is method (for a Sentinel) or As method (for a Concrete Type)
//     can answer true when its target is X. The pass runs the method with
//     only that known: it follows every branch but those whose condition
//     cannot hold for X, and the method matches if it can then return
//     anything but false. A condition cannot hold for X only if it is one
//     of these, taken on the edge named: target == V (true edge) or
//     target != V (false edge) for a V that carries no Mark and is of a
//     type that Marks exist for (an interface, a type that can be an
//     error); errors.Is(V, target) or errors.As(V, target) (true edge)
//     for such a V; the ok of an assertion of the target to a type that
//     X's value cannot have (true edge) or is known to have (false
//     edge); and these through !, && and ||, and as the result of a
//     static call that is passed the target (followed four calls deep).
//     Here "the target" is the parameter, its conversions between
//     interface types, its assertions that panic if they fail, and such
//     an asserted value converted back to an interface. Everything else
//     can be true and can be false: the false edge of target == V, a
//     field of the receiver, a flag, a call the pass does not follow,
//     target == nil, a comparison of a phi or of the value of a comma-ok
//     assertion, a comparison of the target converted to another
//     concrete type (int(target.(Code)) == e.code, whatever the other
//     side is: a value equal to a conversion of X is not X, and carries
//     no Mark for it), a comparison with a constant (the pass does not
//     evaluate them), and every comparison of the target of an As method
//     (it is a pointer, not X). A method with more to look at than the
//     pass is willing to (maxTargetSteps conditions) matches;
//   - its Is or As method (the one that matters for X) is not scanned:
//     it has no body, or it is not the method errors.Is and errors.As
//     call (it has other parameters). Then the type wraps every X;
//   - one of its Unwrap, Is or As methods is not scanned (it has no
//     body), and it holds a marked value in a field (through structs
//     nested by value) or in a slice, array, map, channel or pointer
//     field.
//
// A type is a wrapper by its own methods. The types that conversions
// join (type Code int, int and syscall.Errno) share memory locations,
// not methods: each is asked for itself, and a Mark on a value goes with
// it through a conversion to the methods of its new type.
//
// A marked value in a field that no Unwrap method returns and no Is or As
// method reports does not make the type a wrapper: errors.Is and errors.As
// only see what those methods give them.
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
// receiver that wraps X is always marked). A slice that a method named
// Unwrap returns from a marked receiver (Unwrap() []error) holds what
// the receiver holds: the values loaded from it where it is returned are
// marked, and if it goes anywhere else, the memory of its element type
// is. (The operands of errors.Join and of a fmt.Errorf with several %w
// are in no memory the pass saw a store to.)
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
// wrapper's content changes. The guard drops the by-value plane only.
//
// The Sentinel itself is held by value only if its value is fixed: every
// variable that holds it is only initialized, with a value made by
// errors.New, by fmt.Errorf with a constant format over fixed %w
// operands, or of a comparable type that can hold nothing that carries a
// Mark (no interface, error, function or reflect.Value in it or behind
// its pointers). A Sentinel such as var ErrX error = &W{} (a wrapper the
// program may fill after a check) is held by type: no guard applies to
// it.
//
// A third plane, held, is for an interface value or reflect.Value that
// may be a composite (a struct, a slice, a pointer) with X somewhere in
// memory that no store of the program wrote: it came out of a store
// through reflection or from outside the program. Only such a value makes
// a type assertion to a composite type mark the memory of that type; an
// error that travels in an any is not taken for a slice. Every Mark is in
// one or more of the three planes.
//
// A held Mark of which more is known is boxed: the value is a pointer
// to, or a slice of, memory that holds a marked value itself (what
// Value.Addr and reflect.Append make of a Value that holds one). A type
// assertion of it to a pointer or slice type marks the memory of the
// element type, and nothing deeper: a marked value has nothing new
// inside. Around a composite that is held, a box is held.
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
//     (Err..., err..., EOF) that the program only initializes is the one
//     exception: it is taken not to be reassigned from outside, or every
//     Sentinel of a library could be every other. This is an assumption,
//     not a proof: a client that reassigns an exported Sentinel
//     (lib.ErrNotFound = io.EOF) is not modelled. (The CLI warns when the
//     loaded program itself assigns the Subject outside its
//     initialization.)
//
// Which packages are open is for the caller to say. The CLI opens every
// loaded package that is not a main package, with one exception: an
// internal package (one with an "internal" path element) of the main
// module is closed when the default pattern ./... is run from the root of
// that module, because then every package that may import it is loaded.
// With any other patterns, from a directory below the module's root, or
// for an internal package of another module, it is open like any other
// package.
//
// # Reflection
//
// The bodies of package reflect are not scanned; its calls are modelled:
//
//   - a reflect.Value (and a *reflect.MapIter) carries the Mark of what it
//     holds, and a call of package reflect gives back what it is passed:
//     reflect.ValueOf(x), v.Elem(), v.Field(i), v.Interface(), and
//     reflect.TypeAssert[T](v), whose result is what the Value holds as a
//     T (a type assertion of it). A composite passed to it counts if it
//     holds a marked value. What Value.Addr and reflect.Append return is
//     a pointer to, or a slice with, what they were passed: a box around
//     a marked value, held around a held composite. The function that a
//     range over Value.Seq or Seq2 calls is passed what the Value holds;
//   - calls through reflection (Value.Call, CallSlice, Method,
//     MethodByName, and so text/template): the results, parameters and
//     panics of the exported methods and of the functions whose value is
//     taken (of the types the called Values were made from, when the pass
//     can tell) are connected to those calls;
//   - a store through reflection (Value.Set, SetMapIndex, Send, SetIterKey,
//     SetIterValue, reflect.Append, AppendSlice, Copy) of a marked value is
//     followed where the pass can tell its destination, by walking back
//     from the destination Value (through the calls of package reflect,
//     phis, the results of static calls, the parameters of functions all
//     of whose callers it sees, and private local struct variables). If
//     the Value was made by reflect.ValueOf(x) from a composite x of a
//     known static type, the memory that type can hold is marked. If it
//     names fresh memory (reflect.New, MakeSlice, MakeMap, MakeChan), the
//     reflect.Value of that memory is marked, and with it what is read
//     back from that same Value through reflection; the rest of the
//     program is not. A read of fresh memory through its type, where it
//     is followed, marks all that type can hold, also if the type is an
//     error itself (a *MyErr with an error in a field);
//   - a store through reflection whose destination the pass cannot tell
//     (the Value comes out of memory, out of a dynamic call, through a
//     pointer that was stored in fresh memory, across an interface value
//     inside a known composite, or from further away than the walk goes)
//     is not followed: nothing is marked. Nor is a store into fresh
//     memory followed all the way if it puts the value inside a composite
//     (a field, an element) and the Value then leaves the calls of
//     package reflect: a read of that memory through its type
//     (v.Interface().(*T).F) is seen only next to the store, and one
//     through reflect.TypeAssert[T] or a pointer made by Value.Addr only
//     as far as the type asserted is concerned. Both are
//     reported instead: Result.ReflectStores lists these stores, and the
//     Verdict names them ("not followed"). A "no" does not cover what
//     they write: it is printed as "no, apart from reflection".
//
// Which stores are listed. A store through reflection is recorded if the
// value stored carries a Mark that comes from X and the pass did not
// follow the store (all the way). A Mark comes from X unless the value
// has it only because package reflect made it of a type chosen at run
// time (reflect.New(t), see Seeds): such a value is not X on its way
// somewhere, and wherever scanned code converts it to an interface it is
// marked by its type again. So the stores of a decoder (encoding/json,
// fmt.Sscan) into what it allocates are not recorded for every Concrete
// Type Subject. (The pass runs a second time without those seeds to tell
// the two apart.) Of the recorded stores, a Verdict lists those that can
// matter to its Target:
//
//   - the stores in a function that a Target function reaches, or that
//     reaches a Target function, over the call edges of the pass (a
//     reference to a function, the callees of a dynamic call, and from a
//     function that calls through reflection to the functions such a
//     call may run), through scanned functions;
//   - every recorded store, if a Target function or a scanned function it
//     reaches reads a value that can carry a Mark from memory a store
//     through reflection can write: through a pointer, from a field, an
//     element, a map or a channel (anything but a local or package-level
//     variable whose address is never taken), or by a call of package
//     reflect or a load of a sync/atomic value.
//
// A store that is neither is not listed. That is a bound and not a proof:
// what it writes can still come to the Target in an argument, read by a
// function the Target does not reach.
//
// What is not modelled at all, and so neither followed nor listed (a "no"
// can be wrong here without saying so), for example:
//
//   - a method called through Value.Call or Value.Method whose result
//     is in no memory the pass saw a store to: the []error of an
//     Unwrap() []error of errors.Join called through reflection;
//   - a value read out of existing memory through reflection and handed
//     out by Value.Interface(), when it is marked only by its type (a type
//     seed, a wrapper type) and the composite it is read from goes into
//     reflection in another function: reflect.ValueOf(a).Field(0)
//     .Interface() in a helper that is passed a struct with a field of
//     the Subject's type. The conversion to an interface happens inside
//     package reflect, where no seed is applied. So too a value of a
//     wrapper type that reflection makes (reflect.New of a type with an
//     Is method), and the result of a function called by Value.Call;
//   - a typed pointer made by Value.Addr to a field of program memory
//     (v.Field(0).Addr().Interface().(*error)) and stored through
//     afterwards: the field is not joined with the memory of its type.
//
// # Known limits
//
//   - unsafe, cgo, assembly and //go:linkname can move X where the pass
//     cannot see. Modelled are only errors.As (its target is marked), the
//     contents of sync/atomic.Value and atomic.Pointer, and a function
//     without a body (its results are marked when an argument is). An
//     address converted to unsafe.Pointer is not followed.
//   - An Is or As method that the pass cannot see at all is the one gap
//     of the wrapper rule: a method reached only through reflection or
//     from code that is not loaded, whose type is never converted to an
//     interface in the program. Every method it scans is decided as
//     described under Seeds: a type is no wrapper of X only where each
//     way to a true answer is closed by a look at the target that X
//     fails. That look trusts the Marks: a value V that carries none,
//     of a type that can carry one, is not X. (An Is or As method the
//     program never calls through errors.Is or errors.As is not scanned;
//     its type then wraps every X.)
//   - Type-keyed locations and whole-type wrappers are coarse: expect a
//     false "maybe", never a false "no".
//   - A value stored into the Sentinel's variable by the program is
//     followed forward from the store only. Its other copies are covered
//     by type: a pointer the program may hold elsewhere makes every
//     pointer of its type a seed (see Seeds). That rests on the walk back
//     from the variable to the allocation (typeWalk): through
//     conversions, phis and the results of static calls, twelve steps
//     deep; where it gives up, every error type is a seed.
//   - A Mark does not go with a value through a conversion to a type that
//     cannot be an error ((*plain)(p), int(code)), and a type assertion of
//     a marked interface value to a type that is neither a type X's value
//     may have nor a possible wrapper drops it (the value is then not X).
//     What is converted back is covered by type: a value type that may
//     equal X, a wrapper type and the Concrete Type are marked wherever
//     they are converted to an interface, and a conversion to the type of
//     a pointer Sentinel makes every pointer of that type a seed (see
//     Seeds). The Verdict there is maybe, not no: func Target() error {
//     return (*T)((*plain)(ErrX.(*T))) }.
//   - Reflection is best effort (see Reflection): a "no" is a proof only
//     for flows that do not pass through package reflect. Stores through
//     reflection with an unknown destination are reported, not followed,
//     and only those that can matter to the Target are listed; reads and
//     calls through reflection that the pass does not model are not
//     reported at all. A store through a known composite
//     marks what its static type can hold; what an interface value inside
//     it points to is only found where that value is later asserted to a
//     composite type.
//   - Not modelled, and not reported: a send through reflect.Select, an
//     iterator of Value.Seq that is not called where it is made,
//     reflect.Append into the spare capacity of an array that another
//     slice shows, and the unsafe ways into a Value (NewAt, Pointer,
//     UnsafeAddr).
//   - The callers of a function are the calls in scanned functions: a
//     call from the body of an intrinsic or of package reflect is not one
//     of them when a destination is traced through a parameter.
//   - An internal package of the main module is closed when the whole
//     module is loaded (the CLI decides, see above); files left out by
//     build constraints, test files and nested modules that could import
//     it are not seen.
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
	// ReflectStores is how many stores through reflection the Subject
	// reaches that the pass did not follow, anywhere in the program
	// (Result.ReflectStores lists those near a Target).
	ReflectStores int
}

// Result is the outcome of the upward pass for one Subject.
type Result struct {
	a *analysis
	// unfollowed are the stores through reflection that the Subject
	// reaches and the pass did not follow (see ReflectStores).
	unfollowed map[ssa.CallInstruction]bool
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
	r := &Result{a: a, unfollowed: a.unfollowed}
	if len(a.unfollowed) > 0 && a.madeSeeded {
		// Which of those stores does the Subject itself reach? A value
		// that is marked only because reflection made it of a type
		// chosen at run time (it may be equal to the Subject, or have
		// the Subject's type) is not the Subject on its way somewhere:
		// wherever it is converted to an interface in scanned code it
		// is marked by its type again. The stores it reaches (those of
		// every decoder) are not reported. They are told apart by
		// running the pass once more without those seeds.
		b := newAnalysis(prog, res, subj)
		b.open = a.open
		b.noMadeSeeds = true
		if err := b.run(ctx); err != nil {
			return nil, err
		}
		r.unfollowed = b.unfollowed
	}
	return r, nil
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
	st := Stats{Functions: len(a.fns), ReflectStores: len(r.unfollowed)}
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
