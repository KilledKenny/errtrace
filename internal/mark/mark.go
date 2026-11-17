// Package mark is the upward pass of a Membership Query: from every place
// that produces the error X (the Subject), it pushes a Mark, "this value or
// memory location may hold X", forward through the whole program. Where the
// downward pass (package summary) is a lower bound that gives up at
// Unresolved places, the upward pass is an upper bound: when it is unsure it
// keeps the Mark, so what it does not reach cannot hold X.
//
// What that proves: in a closed world (see Closed and open worlds), for
// a Target that the program's main reaches, a plain "no" is a proof for
// every way a value travels that does not pass through package reflect
// (nor through unsafe, cgo, assembly or //go:linkname, see Known limits).
//
// Reflection is best effort. It is followed where the pass has a model
// (see Reflection) and otherwise not: a "no" is no proof about a value
// that goes through package reflect in a way the pass does not model.
// One thing holds even there. A store through reflection that the pass
// recorded as not followed (it cannot tell where the store writes) is
// never behind a plain "no" if it could matter: a "no" is plain only if
// no such store is recorded, or if a second, flooding pass, in which
// every recorded store writes every memory location that a store through
// reflection could write, every read through reflection may read what
// was written, and every dynamic call may run any function or method of
// the program that fits it (what was written may be its receiver or
// function value), does not reach the Target either. Otherwise the
// Verdict is "no, apart from reflection", with the stores listed as "not
// followed". What the pass neither models nor records it cannot name
// (see the end of Reflection, and Known limits).
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
//     (and of the parameters it is passed on to) as long as the slice
//     stays there: the function, and every function the slice is passed
//     on to, however far, only reads it, stores into its elements,
//     passes it to errors.Join or fmt.Errorf as their operands, or
//     passes it on to a slice parameter of a function the pass follows.
//     Where one of them lets it escape (copies into it, appends to it,
//     stores or returns it, captures it, hands it to a call the pass does
//     not follow), the elements are memory of their type as well, for
//     all of them. So they are for a parameter of a function that a call
//     through reflection may run (see Reflection), and for the
//     parameters it is passed on to: such a call may pass a slice that
//     is other memory.
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
// The intrinsics call methods on their operands, in bodies the pass does
// not scan (see Which functions are scanned): fmt.Errorf formats every
// operand, wrapped or not (Error, String, Format, GoString), and the
// error that it, errors.Join or errtrace.Wrap returns passes the calls
// of its own Error, Format, Unwrap, Is and As on to what it holds. So a
// marked operand of an intrinsic (also one read from a slice, and of an
// intrinsic called through a function value) is the receiver of every
// scanned method of one of those seven names that a value of the
// operand's static type may have: the receiver is marked as by a type
// assertion of the operand to its type. (String only for a type without
// an Error method: package fmt calls Error where there are both.) Where
// the format of a fmt.Errorf is a constant and the operands are those of
// the call, the verbs say which methods, as package fmt decides it: none
// under %T and %p; Format under every other verb; GoString under %#v,
// and then neither Error nor String; Error or String under %v, %s, %x,
// %X and %q; and Unwrap, Is and As only for an operand at a %w, which
// the error holds (fmt.Errorf("%d", err) calls no Error method). An
// operand that no verb formats, and every operand of a format with a
// malformed directive, is taken to have all seven called. A
// method that keeps its receiver (func (e *E) Error() string { last = e;
// ... }) so puts the Subject where it stores it. The receiver and the
// panics are connected (see below); what such a method returns is not,
// and what the operand holds inside is not looked at (see the end of
// Reflection).
//
// The callees of a call come from the Callees given to Run, less the
// functions whose value is never taken at a call of a function value.
//
// A recover sees a panic only if the function that defers the recovering
// function reaches the function that panics over those call edges. One
// edge is added to them, for the methods that package fmt calls inside
// fmt.Errorf: it recovers a panic of a Format, Error, String or GoString
// method of an operand and prints the value of that panic, which calls
// those methods on it, and a panic of that second call leaves
// fmt.Errorf (func (p *P) Error() string { panic(p) }). The value of
// the first panic may be of any type. So a function with a call that may
// run fmt.Errorf on operands (not in a go statement) counts, for panics,
// as calling every scanned method of those four names: a recover that
// covers that call sees their panics, and those of what they reach.
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
// fmt.Errorf("%v", err) marks nothing but the receivers of the methods
// it calls on err (see Calls); nor are the bodies of
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
//     holds a marked value, where the call of package reflect itself is
//     passed a composite of a static type (reflect.ValueOf(s) for a
//     struct s, reflect.ValueOf(&s)); a composite that comes to the call
//     in an interface value does not (see the end of this section). What
//     Value.Addr and reflect.Append return is
//     a pointer to, or a slice with, what they were passed: a box around
//     a marked value, held around a held composite. The function that a
//     range over Value.Seq or Seq2 calls is passed what the Value holds;
//   - calls through reflection (Value.Call, CallSlice, Method,
//     MethodByName, and so text/template): the results, parameters and
//     panics of the exported methods and of the functions whose value is
//     taken (of the types the called Values were made from, when the pass
//     can tell) are connected to those calls. A parameter of a slice
//     type is connected by its elements (Value.Call makes the slice of a
//     variadic parameter from its arguments), and is read as memory of
//     its element type as well: it is never the memory of that function
//     alone (see What a Mark means). A parameter of another composite
//     type is connected only through the memory of its type. Excepted
//     are the methods
//     whose bodies the pass does not scan but models, the Store, Swap,
//     CompareAndSwap and Load of the sync/atomic values and the
//     intrinsics: one of those called through reflection
//     (reflect.ValueOf(&av).MethodByName("Store").Call(args)) is not
//     connected, and nothing is recorded;
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
//     recorded instead: Result.ReflectStores lists these stores, and the
//     Verdict names them ("not followed") if what they write can reach
//     the Target (see below). A "no" does not cover what they write
//     then: it is printed as "no, apart from reflection".
//
// Which stores are listed. A store through reflection is recorded if the
// value stored carries a Mark and the pass did not follow the store (all
// the way), whatever the Mark comes from. That includes a value that is
// marked only because package reflect made it of a type chosen at run
// time (reflect.New(t), see Seeds): the destination may be an interface
// variable, and then package reflect converts the value to an interface
// (Value.Set into an any or error), where no seed of the scanned code
// marks it again; typed code reads it back with a plain load. So for a
// Concrete Type Subject, and for a Sentinel whose value may be equal to
// another value of its type, the stores of every decoder (encoding/json,
// encoding/gob) into what it allocates are recorded: the pass cannot
// tell the types they make, nor the type of the variables they fill.
//
// Whether the recorded stores can matter to a Target is decided by a
// flooding pass (Result.Flood), which the Membership Query runs when the
// pass does not reach the Target and at least one store is recorded. It
// is the pass again, with one change: every store through reflection
// that it does not follow (all the way) writes every memory location
// that a store through reflection could write, with the Marks of the
// value it stores (by value, by type) and held, and every call of
// package reflect that is passed a reflect.Value (or a *reflect.MapIter,
// or a slice of them) hands out what was written: v.Interface(),
// v.Elem(), v.Field(i), v.Index(i), v.MapIndex(k), v.Recv(),
// it.Value(), reflect.Indirect(v), also where such a method is called
// through an interface or a function value. And the call edges are
// wider: where the pass itself takes, for a dynamic call, the callees
// that the program's own instructions bring to it (the Candidates), the
// flooding pass takes every function or method of the program that fits
// the call (the Callees given to Flood: resolve.Upper.Wide), for callees
// and callers alike, and scans what those edges reach. What a store
// wrote may be the receiver or the function value of a call, of a type
// (or a function) that reaches the call in no other way: a plugin that
// is registered through reflection. A read through reflection
// need not read a location the pass names: the Value may be made from
// an interface value of unknown dynamic type (a helper that takes an
// any), or name memory that package reflect made. A call that is passed no
// Value (reflect.ValueOf, New, MakeSlice) reads no memory; it hands out
// what it would without the flood. The flooded locations are those
// keyed by a type, of whatever type (the stored value is behind a
// reflect.Value): the fields of every struct type, the memory of every
// type behind pointers and in slices and arrays (which is also every
// variable and field whose address is taken), the keys and values of
// every map type and the elements of every channel. Not flooded are the
// variables whose address is never taken (reflection needs the address;
// what a slice, map or channel in such a variable refers to is flooded),
// the contents of sync/atomic values (they are behind unexported
// fields), panic values, and the argument arrays that stay with the
// functions they are passed to (see What a Mark means: one that a
// function lets escape, and handing it to package reflect is that, is
// memory of its element type, which is flooded). A flooded field makes
// its type a wrapper as a store by the program would.
//
// If the flooding pass does not reach the Target either, no recorded
// store can put X where the Target finds it, as far as the pass models
// the program: the Verdict is a plain "no", and nothing is listed. If it
// does, the Verdict is "no, apart from reflection" and lists every
// recorded store and, after them and under a line of their own, the
// stores that only the flooding pass records (of a value that another
// store wrote, or that is read through reflection: such a store may be
// the one that writes what the Target reads), each part in the order of
// the scan. (After a "maybe" every recorded store is listed too; no
// flooding pass is needed.) The flooding pass is coarse: it tells
// whether the stores can matter, not that they do; a function that
// reads through reflection is reached by it whenever a store is
// recorded, and so is one that a wider call edge alone leads the
// Subject to.
//
// What is not modelled at all, and so neither followed nor listed (a "no"
// can be wrong here without saying so), for example:
//
//   - a value read through reflection from an interface-typed value of
//     unknown dynamic type, when no store through reflection is recorded:
//     a helper func dump(src any) that reads the fields of what it is
//     passed (reflect.ValueOf(src).Elem().Field(i).Interface()), called
//     with a composite that an ordinary store put a marked value in
//     (c := Cfg{OnFail: X}; dump(&c)). The reflect.Value is made from an
//     interface value, not from a composite of a static type at the call
//     of package reflect, so what the composite holds does not count.
//     (If a store is recorded anywhere in the scanned program, the
//     flooding pass runs and reaches the read: "no, apart from
//     reflection".)
//   - a dynamic call whose callee the program's own instructions do not
//     bring to it, when no store through reflection is recorded: a value
//     of a type, or a function, that gets into a slice of interface
//     values, an interface variable or a table of functions only by
//     reflect.Value.Set, and is called there (op.Run() for an op that
//     reflection registered); or a method value that reflection makes
//     and the program calls, with no store at all
//     (reflect.ValueOf(&T{}).MethodByName("Run").Interface().(func()
//     error)(), where no instruction of the program takes the value of
//     (*T).Run). The store is recorded only if the value
//     stored carries a Mark and the pass does not follow the store: not
//     if it follows it to its destination, nor if the value is a
//     function, or of a type whose method returns the Subject itself.
//     (If a store is recorded anywhere in the scanned program, the
//     flooding pass runs, with its wider call edges, and reaches the
//     call: "no, apart from reflection".)
//   - a reflect.Value that is converted to an interface value and
//     asserted back to reflect.Value: the Mark of what it holds does not
//     pass the assertion (fmt.Sprint(reflect.ValueOf(x)), which prints
//     what the Value holds and calls its Error method);
//   - the methods that package fmt calls on what an operand holds inside
//     (fmt.Errorf("%v", s) and fmt.Sprint(s) for a struct, a slice, a
//     map or a pointer to a struct s with a marked value in it): package
//     fmt finds the value through reflection, from an interface value
//     (see the first point);
//   - a method called through Value.Call or Value.Method whose result
//     is in no memory the pass saw a store to: the []error of an
//     Unwrap() []error of errors.Join called through reflection;
//   - a value read out of existing memory through reflection and handed
//     out by Value.Interface(), when it is marked only by its type (a type
//     seed, a wrapper type): the conversion to an interface happens
//     inside package reflect, where no seed is applied. That is so when
//     the composite it is read from goes into reflection in another
//     function (reflect.ValueOf(a).Field(0).Interface() in a helper that
//     is passed a struct with a field of the Subject's type), and also
//     within one function when the value is reached through a pointer
//     (c := code(0); reflect.ValueOf(&c).Elem().Interface()) or is a
//     field of a wrapper type read through Value.Field
//     (reflect.ValueOf(H{}).Field(0).Interface(), for a field whose type
//     has an Is method). So too the keys of a map that Value.MapRange or
//     Value.MapKeys hand out (it.Key().Interface()), a value of a wrapper
//     type that reflection makes (reflect.New of a type with an Is
//     method), and the result of a function called by Value.Call;
//   - the arguments of a function made by reflect.MakeFunc: they come to
//     its implementation in a []reflect.Value that no store the pass saw
//     filled (reflect.MakeFunc(t, impl).Interface().(func(error))(X));
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
//     address converted to unsafe.Pointer is not followed. That includes
//     the standard library where it keeps a value behind such a pointer:
//     a weak.Pointer does (weak.Make(p).Value() is p again); what comes
//     back out is covered by its type only (a type seed, a wrapper type,
//     the Concrete Type, and a pointer Sentinel's type through the
//     conversion in Value, see Seeds).
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
//     reflection with an unknown destination are recorded, not followed,
//     and listed unless the flooding pass shows that they cannot matter
//     to the Target; reads and calls through reflection that the pass
//     does not model are not reported at all. A store through a known
//     composite
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
	// (Result.ReflectStores lists them).
	ReflectStores int
}

// Result is the outcome of the upward pass for one Subject.
type Result struct {
	a *analysis
	// unfollowed are the stores through reflection that the Subject
	// reaches and the pass did not follow (see ReflectStores).
	unfollowed map[ssa.CallInstruction]bool
	// floodOnly are those of unfollowed that only the flooding pass
	// recorded, on the Result of Flood.
	floodOnly map[ssa.CallInstruction]bool
	// ctxRes are the Callees the pass ran with, for Flood.
	ctxRes Callees
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
	return &Result{a: a, unfollowed: a.unfollowed, ctxRes: res}, nil
}

// Flood runs the pass again as the flooding pass and returns its
// outcome: every store through reflection that the pass does not follow
// (all the way) is taken to write every memory location that a store
// through reflection could write, the type-keyed locations of whatever
// type (see floodable), with the Marks of the value it stores, and what
// it wrote is taken to be what every call of package reflect that is
// passed a reflect.Value hands out (see floodLocs). What the flooding
// pass does not reach, no recorded store can put the Subject in, as far
// as the pass models the program: if it does not reach a Target, the
// stores that ReflectStores lists do not matter to that Target.
//
// It is an upper bound of the pass it is called on, and coarse: it tells
// whether the recorded stores can matter, not where the Subject is. Its
// Marks and traces are of use only to show why a store can matter. Its
// ReflectStores are those of the pass it is called on and, after them
// and told apart (ReflectStore.FloodOnly), the stores that only the flood
// reaches: of a value that an earlier store wrote, or that is read
// through reflection.
//
// The call edges of the flooding pass are res (nil: those of the pass it
// is called on). They must not limit a dynamic call to the callees that
// the program's own instructions bring to it: a store through reflection
// may put a value of any type, or any function, where the call finds its
// receiver or function value (resolve.Upper.Wide).
func (r *Result) Flood(ctx context.Context, res Callees) (*Result, error) {
	a := r.a
	if res == nil {
		res = r.ctxRes
	}
	f := newAnalysis(a.prog, res, a.subj)
	f.open = a.open
	f.flood = true
	if err := f.run(ctx); err != nil {
		return nil, err
	}
	// Its stores are those of r and the ones only the flood reaches: a
	// store of a value that is marked because of an earlier store may
	// be the one that writes what a Target reads.
	stores := make(map[ssa.CallInstruction]bool, len(f.unfollowed)+len(r.unfollowed))
	for site := range r.unfollowed {
		stores[site] = true
	}
	only := make(map[ssa.CallInstruction]bool)
	for site := range f.unfollowed {
		stores[site] = true
		if !r.unfollowed[site] {
			only[site] = true
		}
	}
	return &Result{a: f, unfollowed: stores, floodOnly: only, ctxRes: res}, nil
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
