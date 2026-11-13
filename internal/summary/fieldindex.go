package summary

import (
	"fmt"
	"go/token"
	"go/types"
	"reflect"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/KilledKenny/errtrace/internal/flow"
	"github.com/KilledKenny/errtrace/internal/resolve"
)

// This file builds the index behind field locations (see fields.go): the
// writers of every struct field, the call sites of every function, and
// what the analysis has to assume about code it does not see.
//
// The default is that a field is NOT completely known. A location is
// reported without an Unresolved value only if none of the following
// holds for a struct the field belongs to.
//
// Exposure (holeExposed). Reflection, unsafe code and functions without a
// body may write, or build, any struct they can get hold of. The places
// where a value enters such code are the sinks:
//
//   - an argument of a call to a function of reflect or
//     internal/reflectlite, to a Scan function of fmt, or to a function
//     without a body, and the results of a function without a body;
//   - a conversion between a pointer and unsafe.Pointer.
//
// Outside the standard library, a type assertion to a concrete type on
// what a reflect call returned (v.Interface().(*T)), and the type
// arguments of reflect's generic functions (reflect.TypeFor[T]), name
// types that reflection may have built values of: they are exposed like a
// value at a sink.
//
// Every struct reachable from the type of a value at a sink (through
// pointers, slices, arrays, maps, channels and struct fields) is exposed.
// A value at a sink that is an interface is traced back, in its function
// and up through the parameters of the functions it was passed down, to
// the conversions that made it (MakeInterface): those concrete types are
// exposed. Where the trace ends at anything else (a load, a call result,
// a captured variable, a parameter of a function whose callers are not
// all seen), the dynamic type is not known: every concrete type that the
// reachable program converts to an interface and that can flow to that
// interface type is exposed (dynamicTypes). The same holds where
// reflection reaches an interface-typed field or element of an exposed
// type.
//
// These assumptions bound this, and each is a place where an Error Set
// could be incomplete if the assumption is wrong:
//
//   - the trusted packages (trustedPkgs: runtime, reflect, sync, fmt, log,
//     errors, testing and the internal packages) do not write the fields
//     of what they are given; sinks inside them only say what reflection
//     may read (see below). So does a reflect.ValueOf or reflect.TypeOf
//     whose result is only inspected where it is made (reflectReads);
//   - unsafe conversions and functions without a body (assembly,
//     linknamed, cgo) do not go on through the interface values stored in
//     what they are given, and reflection does not get past an unexported
//     field (reach);
//   - reflect.TypeOf of an interface value of unknown dynamic type
//     exposes nothing: it gives a type, and a value that reflection then
//     builds of a type it did not get from a traced conversion is not
//     followed;
//   - the standard library does not write through an interface value
//     that it neither received from its caller (which the trace follows)
//     nor found inside such a value: a trace that ends inside a standard
//     library function exposes nothing. A trace that ends in any other
//     code exposes everything the interface type may hold;
//   - a dynamic call with no Candidates runs no code outside the
//     reachable program other than the functions described next; only
//     the structs it returns are taken as built by code that is not
//     seen.
//
// Functions that only reflection calls (rawLocation.unreached). The
// reachable program is what the Resolver's call graph reaches; a method
// of a type that reflection gets hold of may run without being in it
// (encoding/json calling MarshalJSON, text/template calling a method,
// reflect.Value.Call, fmt calling String on a nested value). The methods
// outside the reachable program of every exposed type, and those of every
// type that reaches a sink in a trusted package whose name is the name of
// a method that reachable code calls through an interface, are scanned
// with everything they call that is also outside it: a field they store
// to keeps an Unresolved value naming the function, and a function they
// call has callers that are not seen.
//
// Callers (callersSeen). The arguments of every call site of a function
// stand for its parameter only if every call is seen: the function has a
// call site, is not called from a function scanned as above, and, if it
// is used as a value, a dynamic call resolves to it and neither it nor a
// function type it has was handed to reflection or to code that is not
// seen. The methods of exposed types may be called by reflection too.
//
// Open world. If a function the Query was asked about is not reachable
// from main and the init functions, its callers are not seen and may
// write any field: every location keeps an Unresolved value.

// holeFlags are the ways a field location may be written that the scan
// does not attribute to a writer.
type holeFlags uint8

const (
	// holeEscapes: the field's address is used other than to load from it
	// or store to it.
	holeEscapes holeFlags = 1 << iota
	// holeExposed: reflection, unsafe code or a function errtrace does not
	// see may reach a struct holding the field.
	holeExposed
)

// holeReasons are the details of the Unresolved values of holeFlags, after
// "field <f> of <T> ", in the order they are reported.
var holeReasons = []struct {
	flag   holeFlags
	reason string
}{
	{holeEscapes, "may be written through an escaped pointer"},
	{holeExposed, "may be written by reflection, unsafe code or a function that is not seen"},
}

// openWorldReason is the detail of the Unresolved value every location
// keeps when the Query's roots have callers outside the reachable program.
const openWorldReason = "may be written by code that is not reachable from the Query's roots"

// rawLocation is what the scan found for one fieldKey, before the keys
// that conversions unite are merged.
type rawLocation struct {
	name    string
	writers []*ssa.Store
	flags   holeFlags
	// unreached names a function outside the reachable program that
	// reflection may call and that writes the field, or is empty.
	unreached string
}

// sinkArg is a value handed to code that may write what it reaches.
type sinkArg struct {
	fn *ssa.Function
	v  ssa.Value
	// mode says what the code may do with the value.
	mode reachMode
	// weak says that a trace of the value that ends at an interface of
	// unknown dynamic type exposes nothing (reflect.TypeOf: see trace).
	weak bool
}

// exposedType is a type handed to code that may write what it reaches.
type exposedType struct {
	t    types.Type
	mode reachMode
}

// reachMode says how code that got a value goes on from it (see reach).
type reachMode struct {
	// write: the code may write what it reaches. Otherwise it only
	// reads, and may call methods.
	write bool
	// deep: the code goes on through the interface values it finds, to
	// the concrete types they may hold.
	deep bool
	// exported: the code is reflection, which cannot write (or go on
	// from) an unexported struct field.
	exported bool
	// tag: the code is encoding/json, which also skips the fields
	// tagged `json:"-"`.
	tag string
}

// fieldIndex is the index of the field locations of the reachable
// program, with their writers, and of the call sites of its functions.
type fieldIndex struct {
	res  resolve.Resolver
	prog *ssa.Program
	// parent unites the keys of struct types converted to one another
	// (union-find).
	parent map[fieldKey]fieldKey
	raw    map[fieldKey]*rawLocation
	// order lists the keys of raw in the order the scan met them.
	order []fieldKey
	// locs are the locations by the root of their keys.
	locs map[fieldKey]*location
	// reachable are the functions scanned as the reachable program.
	reachable map[*ssa.Function]bool
	// callers are the call sites that may call each function, in scan
	// order; refs are the functions each function calls or refers to.
	callers map[*ssa.Function][]ssa.CallInstruction
	refs    map[*ssa.Function][]*ssa.Function
	// addrTaken are the functions used as a value other than as the
	// callee of a call, and dynCalled those that are a Candidate of a
	// dynamic call.
	addrTaken, dynCalled map[*ssa.Function]bool
	// entry are the functions reachable from main and the init
	// functions.
	entry map[*ssa.Function]bool

	// boxes are, per interface type (by its string), the concrete types
	// converted to it; into are the interface types converted or
	// asserted to it; ifaces are the interface types themselves.
	boxes   map[string][]types.Type
	into    map[string][]string
	ifaces  map[string]*types.Interface
	dynamic map[string][]types.Type
	// invoked are the names of the methods reachable code calls through
	// an interface or asserts an interface for.
	invoked map[string]bool

	// sinks and exposeLater are the values and types found at sinks,
	// handled once the scan is complete; readSinks are the values at
	// sinks inside the trusted packages.
	sinks, readSinks []sinkArg
	exposeLater      []exposedType
	// traced are the values followed from a sink that may write, and
	// tracedRead those followed from one that only reads.
	traced, tracedRead map[ssa.Value]bool
	// stdRoot is the directory of the standard library's sources, and
	// stdPkgs caches std by package path.
	stdRoot string
	stdPkgs map[string]bool
	// tracedParams are the parameters a sink value was traced through.
	tracedParams []*ssa.Parameter
	// reached are the types visited by reach, per mode; exposedNamed are
	// the named types reached by code that may write, and seenNamed
	// those reached by code that only reads, in the order found.
	reached                 map[reachMode]map[types.Type]bool
	named                   map[*types.Named]int
	exposedNamed, seenNamed []*types.Named

	// funcBoxed are the functions whose value reflection or unseen code
	// got; boxedSigs the function types it got.
	funcBoxed map[*ssa.Function]bool
	boxedSigs []*types.Signature
	sigSeen   map[*types.Signature]bool
	// reflectCallable are the reachable methods of exposed types.
	reflectCallable map[*ssa.Function]bool
	// unseenCaller are the functions that a function outside the
	// reachable program calls or refers to; unseenInvoke the names of the
	// methods such a function calls through an interface; unseenDynCall
	// reports that one calls a function value.
	unseenCaller  map[*ssa.Function]bool
	unseenInvoke  map[string]bool
	unseenDynCall bool
}

// index returns the Solver's field index, building it from prog if it is
// not built yet.
func (s *Solver) index(prog *ssa.Program) *fieldIndex {
	if s.fields != nil {
		return s.fields
	}
	ix := &fieldIndex{
		res:             s.res,
		prog:            prog,
		parent:          make(map[fieldKey]fieldKey),
		raw:             make(map[fieldKey]*rawLocation),
		locs:            make(map[fieldKey]*location),
		reachable:       make(map[*ssa.Function]bool),
		callers:         make(map[*ssa.Function][]ssa.CallInstruction),
		refs:            make(map[*ssa.Function][]*ssa.Function),
		addrTaken:       make(map[*ssa.Function]bool),
		dynCalled:       make(map[*ssa.Function]bool),
		entry:           make(map[*ssa.Function]bool),
		boxes:           make(map[string][]types.Type),
		into:            make(map[string][]string),
		ifaces:          make(map[string]*types.Interface),
		dynamic:         make(map[string][]types.Type),
		invoked:         make(map[string]bool),
		traced:          make(map[ssa.Value]bool),
		tracedRead:      make(map[ssa.Value]bool),
		stdPkgs:         make(map[string]bool),
		reached:         make(map[reachMode]map[types.Type]bool),
		named:           make(map[*types.Named]int),
		funcBoxed:       make(map[*ssa.Function]bool),
		sigSeen:         make(map[*types.Signature]bool),
		reflectCallable: make(map[*ssa.Function]bool),
		unseenCaller:    make(map[*ssa.Function]bool),
		unseenInvoke:    make(map[string]bool),
	}
	ix.findStd()
	fns := universe(prog, s.res)
	for _, fn := range fns {
		ix.reachable[fn] = true
	}
	for _, fn := range fns {
		ix.scan(fn)
	}
	ix.findEntry(fns)
	for _, fn := range fns {
		if fn.Origin() == nil || pkgPath(fn) != "reflect" {
			continue
		}
		// An instance of a generic function of reflect
		// (reflect.TypeFor[T]) called from outside the standard library:
		// reflection gets the type argument, and may build values of it.
		for _, site := range ix.callers[fn] {
			if !ix.std(site.Parent()) {
				for _, t := range fn.TypeArgs() {
					ix.exposeLater = append(ix.exposeLater, exposedType{t, reachMode{write: true, exported: true}})
				}
				break
			}
		}
	}
	for _, e := range ix.exposeLater {
		ix.reach(e.t, e.mode)
	}
	for _, a := range ix.sinks {
		ix.trace(a.fn, a.v, a.mode, a.weak)
	}
	ix.settle()
	ix.scanUnreached()
	ix.settle()
	for _, k := range ix.order {
		r := ix.raw[k]
		l := ix.at(k)
		if l.name == "" {
			l.name = r.name
		}
		l.writers = append(l.writers, r.writers...)
		l.flags |= r.flags
		if l.unreached == "" {
			l.unreached = r.unreached
		}
	}
	s.fields = ix
	return ix
}

// universe returns the functions with a body that res calls reachable, in
// a deterministic order.
func universe(prog *ssa.Program, res resolve.Resolver) []*ssa.Function {
	var fns []*ssa.Function
	if f, ok := res.(interface{ Functions() []*ssa.Function }); ok {
		fns = f.Functions()
	} else {
		for fn := range ssautil.AllFunctions(prog) {
			fns = append(fns, fn)
		}
		sortFuncs(fns)
	}
	var out []*ssa.Function
	for _, fn := range fns {
		if fn != nil && fn.Blocks != nil && res.Reachable(fn) {
			out = append(out, fn)
		}
	}
	return out
}

func sortFuncs(fns []*ssa.Function) {
	sort.SliceStable(fns, func(i, j int) bool {
		if a, b := fns[i].String(), fns[j].String(); a != b {
			return a < b
		}
		return fns[i].Pos() < fns[j].Pos()
	})
}

func (ix *fieldIndex) find(k fieldKey) fieldKey {
	for {
		p, ok := ix.parent[k]
		if !ok || p == k {
			return k
		}
		k = p
	}
}

func (ix *fieldIndex) union(a, b fieldKey) {
	if ra, rb := ix.find(a), ix.find(b); ra != rb {
		ix.parent[rb] = ra
	}
}

// fieldDescription returns "field <f> of <T>" for field i of the struct
// type t.
func fieldDescription(t types.Type, i int) string {
	name := fmt.Sprintf("#%d", i)
	if st, ok := t.Underlying().(*types.Struct); ok && i < st.NumFields() {
		name = st.Field(i).Name()
	}
	return fmt.Sprintf("field %s of %s", name, types.TypeString(t, nil))
}

// rawAt returns the scan's record of key k, which is field i of the
// struct type t.
func (ix *fieldIndex) rawAt(k fieldKey, t types.Type, i int) *rawLocation {
	r := ix.raw[k]
	if r == nil {
		r = &rawLocation{name: fieldDescription(t, i)}
		ix.raw[k] = r
		ix.order = append(ix.order, k)
	}
	return r
}

// at returns the location of key k: one that nothing was found for has no
// writers, so its field is always the zero value.
func (ix *fieldIndex) at(k fieldKey) *location {
	root := ix.find(k)
	l := ix.locs[root]
	if l == nil {
		l = new(location)
		ix.locs[root] = l
	}
	return l
}

// trustedPkgs are the packages taken not to write the fields of the
// values they are given, by reflection or unsafe code of their own: the
// runtime and reflection themselves, the synchronisation primitives
// (which hand a value back as they got it), the packages that format,
// log and compare values, and sort (which swaps the elements of the slice
// it is given: whole copies, which add nothing to a field location). Packages under internal/ and runtime/ are
// trusted too.
var trustedPkgs = map[string]bool{
	"runtime": true, "reflect": true, "unsafe": true, "sync": true, "sync/atomic": true,
	"fmt": true, "log": true, "log/slog": true, "errors": true, "testing": true, "sort": true,
}

// pkgPath returns the path of the package fn belongs to (that of the
// function it is nested in or an instance of), or "".
func pkgPath(fn *ssa.Function) string {
	if fn == nil {
		return ""
	}
	for fn.Parent() != nil {
		fn = fn.Parent()
	}
	if o := fn.Origin(); o != nil {
		fn = o
	}
	switch {
	case fn.Pkg != nil:
		return fn.Pkg.Pkg.Path()
	case fn.Object() != nil && fn.Object().Pkg() != nil:
		return fn.Object().Pkg().Path()
	}
	return ""
}

func trusted(fn *ssa.Function) bool {
	path := pkgPath(fn)
	return trustedPkgs[path] || strings.HasPrefix(path, "internal/") || strings.HasPrefix(path, "runtime/")
}

// sinkCallee reports whether fn is an entry into reflection: a function
// of reflect or internal/reflectlite, or a Scan function of fmt.
func sinkCallee(fn *ssa.Function) bool {
	switch pkgPath(fn) {
	case "reflect", "internal/reflectlite":
		return true
	case "fmt":
		return strings.Contains(strings.ToLower(fn.Name()), "scan")
	}
	return false
}

// scan indexes the instructions of the reachable function fn.
func (ix *fieldIndex) scan(fn *ssa.Function) {
	var buf [10]*ssa.Value
	safe := trusted(fn)
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch instr := instr.(type) {
			case *ssa.FieldAddr:
				ix.fieldAddr(instr)
			case *ssa.MakeInterface:
				ix.box(instr)
			case *ssa.ChangeInterface:
				ix.flow(instr.X.Type(), instr.Type())
			case *ssa.TypeAssert:
				ix.flow(instr.X.Type(), instr.AssertedType)
				if _, toIface := ix.ifaceKey(instr.AssertedType); !toIface && !ix.std(fn) && fromReflect(instr.X, 0) {
					// Typed code takes a value out of reflection
					// (v.Interface().(*T)): reflection may have built
					// it.
					ix.exposeLater = append(ix.exposeLater, exposedType{instr.AssertedType, reachMode{write: true, exported: true}})
				}
			case *ssa.ChangeType:
				ix.convert(safe, instr.X.Type(), instr.Type())
			case *ssa.Convert:
				ix.convert(safe, instr.X.Type(), instr.Type())
			case *ssa.MakeClosure:
				ix.closure(instr)
			case *ssa.DebugRef:
				// Debug information names values without using them.
				continue
			}
			site, isCall := instr.(ssa.CallInstruction)
			if isCall {
				ix.call(site, safe)
			}
			mc, _ := instr.(*ssa.MakeClosure)
			for _, op := range instr.Operands(buf[:0]) {
				f, ok := (*op).(*ssa.Function)
				if !ok {
					continue
				}
				ix.refs[fn] = append(ix.refs[fn], f)
				if (isCall && op == &site.Common().Value) || (mc != nil && op == &mc.Fn) {
					continue
				}
				ix.addrTaken[f] = true
			}
		}
	}
}

// fieldAddr indexes the uses of the address of a field: the stores to it
// are the field's writers, and any use other than a store to it or a load
// from it lets the address escape.
func (ix *fieldIndex) fieldAddr(fa *ssa.FieldAddr) {
	st := structOf(fa.X.Type(), true)
	if st == nil {
		return
	}
	k, ok := fieldKeyOf(st, fa.Field)
	if !ok {
		return
	}
	refs := fa.Referrers()
	if refs == nil {
		return
	}
	r := ix.rawAt(k, st, fa.Field)
	for _, ref := range *refs {
		switch ref := ref.(type) {
		case *ssa.Store:
			if ref.Addr != fa || ref.Val == fa {
				r.flags |= holeEscapes
				continue
			}
			r.writers = append(r.writers, ref)
		case *ssa.UnOp:
			if ref.Op != token.MUL {
				r.flags |= holeEscapes
			}
		case *ssa.DebugRef:
		default:
			r.flags |= holeEscapes
		}
	}
}

// fromReflect reports whether the interface value v is, in its function,
// the result of a call to a function or method of reflect.
func fromReflect(v ssa.Value, depth int) bool {
	if depth > 8 {
		return false
	}
	switch v := v.(type) {
	case *ssa.Call:
		callee := v.Common().StaticCallee()
		return callee != nil && pkgPath(callee) == "reflect"
	case *ssa.Extract:
		return fromReflect(v.Tuple, depth+1)
	case *ssa.ChangeInterface:
		return fromReflect(v.X, depth+1)
	case *ssa.Phi:
		for _, e := range v.Edges {
			if fromReflect(e, depth+1) {
				return true
			}
		}
	}
	return false
}

// ifaceKey names the interface type t, or returns false if t is not one.
func (ix *fieldIndex) ifaceKey(t types.Type) (string, bool) {
	t = types.Unalias(t)
	if _, param := t.(*types.TypeParam); param {
		return "", false
	}
	iface, ok := t.Underlying().(*types.Interface)
	if !ok {
		return "", false
	}
	k := types.TypeString(t, nil)
	if _, known := ix.ifaces[k]; !known {
		ix.ifaces[k] = iface
		for i := range iface.NumMethods() {
			ix.invoked[iface.Method(i).Name()] = true
		}
	}
	return k, true
}

// box indexes a conversion of a concrete value to an interface.
func (ix *fieldIndex) box(mi *ssa.MakeInterface) {
	k, ok := ix.ifaceKey(mi.Type())
	if !ok {
		return
	}
	ix.boxes[k] = append(ix.boxes[k], mi.X.Type())
}

// flow indexes a conversion or assertion of a value of interface type
// from to the interface type to: what the one holds, the other may.
func (ix *fieldIndex) flow(from, to types.Type) {
	kf, ok := ix.ifaceKey(from)
	if !ok {
		return
	}
	kt, ok := ix.ifaceKey(to)
	if !ok || kf == kt {
		return
	}
	for _, k := range ix.into[kt] {
		if k == kf {
			return
		}
	}
	ix.into[kt] = append(ix.into[kt], kf)
}

// dynamicTypes returns the concrete types that a value of the interface
// type t may hold, as far as typed code goes: those converted to t, or to
// an interface type that is converted or asserted to t, directly or not,
// and that implement t. A value changes its static interface type only
// by such a conversion, so every interface value of the reachable program
// that typed code made holds one of them.
func (ix *fieldIndex) dynamicTypes(t types.Type) []types.Type {
	key, ok := ix.ifaceKey(t)
	if !ok {
		return nil
	}
	if out, ok := ix.dynamic[key]; ok {
		return out
	}
	iface := ix.ifaces[key]
	var out []types.Type
	seen := map[string]bool{key: true}
	have := make(map[types.Type]bool)
	work := []string{key}
	for len(work) > 0 {
		k := work[0]
		work = work[1:]
		for _, x := range ix.boxes[k] {
			if !have[x] && types.Implements(x, iface) {
				have[x] = true
				out = append(out, x)
			}
		}
		for _, from := range ix.into[k] {
			if !seen[from] {
				seen[from] = true
				work = append(work, from)
			}
		}
	}
	ix.dynamic[key] = out
	return out
}

// reach visits every type that code holding a value of type t can reach:
// through pointers, slices, arrays, maps, channels and struct fields, and,
// in a deep mode, for an interface, the concrete types it may hold
// (dynamicTypes). If the code may write (m.write), the locations of the
// structs get holeExposed and the function types are recorded as handed
// to it.
//
// Reflection cannot set an unexported field, nor anything it reaches
// through one (reflect panics), so in an exported mode such fields are
// neither flagged nor followed; encoding/json also leaves the fields
// tagged `json:"-"` alone. deep is not set for what unsafe conversions and
// functions without a body are given (a syscall's buffer), and for what
// the trusted packages read: they are taken not to go through the
// interface values inside it.
func (ix *fieldIndex) reach(t types.Type, m reachMode) {
	t = types.Unalias(t)
	done := ix.reached[m]
	if done == nil {
		done = make(map[types.Type]bool)
		ix.reached[m] = done
	}
	if done[t] || ix.reached[reachMode{write: true, deep: true}][t] {
		return
	}
	done[t] = true
	if _, param := t.(*types.TypeParam); param {
		return
	}
	if n, ok := t.(*types.Named); ok {
		// 1: read, 2: may be written.
		switch was := ix.named[n]; {
		case m.write && was < 2:
			ix.named[n] = 2
			ix.exposedNamed = append(ix.exposedNamed, n)
		case !m.write && was == 0:
			ix.named[n] = 1
			ix.seenNamed = append(ix.seenNamed, n)
		}
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for i := range u.NumFields() {
			if m.exported && !u.Field(i).Exported() {
				continue
			}
			if m.tag != "" {
				if name, _, _ := strings.Cut(reflect.StructTag(u.Tag(i)).Get(m.tag), ","); name == "-" {
					continue
				}
			}
			if k, ok := fieldKeyOf(t, i); ok && m.write {
				ix.rawAt(k, t, i).flags |= holeExposed
			}
			ix.reach(u.Field(i).Type(), m)
		}
	case *types.Pointer:
		ix.reach(u.Elem(), m)
	case *types.Slice:
		ix.reach(u.Elem(), m)
	case *types.Array:
		ix.reach(u.Elem(), m)
	case *types.Chan:
		ix.reach(u.Elem(), m)
	case *types.Map:
		ix.reach(u.Key(), m)
		ix.reach(u.Elem(), m)
	case *types.Interface:
		if m.deep {
			for _, x := range ix.dynamicTypes(t) {
				ix.reach(x, m)
			}
		}
	case *types.Signature:
		if m.write && !ix.sigSeen[u] {
			ix.sigSeen[u] = true
			ix.boxedSigs = append(ix.boxedSigs, u)
		}
	}
}

// anything is the mode of code that may write whatever it reaches.
var anything = reachMode{write: true, deep: true}

// findStd finds where the standard library's sources are: the parent of
// the directory of package runtime.
func (ix *fieldIndex) findStd() {
	rt := ix.prog.ImportedPackage("runtime")
	if rt == nil {
		return
	}
	if fn := rt.Func("GC"); fn != nil && fn.Pos().IsValid() {
		file := ix.prog.Fset.Position(fn.Pos()).Filename
		if i := strings.LastIndex(file, "/runtime/"); i >= 0 {
			ix.stdRoot = file[:i+1]
		}
	}
}

// std reports whether fn belongs to a package of the standard library:
// its source file is under the directory that holds package runtime.
func (ix *fieldIndex) std(fn *ssa.Function) bool {
	path := pkgPath(fn)
	if is, ok := ix.stdPkgs[path]; ok {
		return is
	}
	for fn.Parent() != nil {
		fn = fn.Parent()
	}
	if o := fn.Origin(); o != nil {
		fn = o
	}
	if ix.stdRoot == "" || !fn.Pos().IsValid() {
		return false
	}
	is := strings.HasPrefix(ix.prog.Fset.Position(fn.Pos()).Filename, ix.stdRoot)
	ix.stdPkgs[path] = is
	return is
}

// trace finds what the value v of fn, handed to a sink, may be, and
// reaches it in mode m (see reach). An interface value is followed back
// to the conversions that made it, through the parameters of the
// functions it was passed down. Where that ends in code outside the
// standard library, at a sink that may write, every type its static type
// may hold is reached; where it ends inside the standard library, or the
// sink only reads or is weak, nothing is (see the top of this file).
func (ix *fieldIndex) trace(fn *ssa.Function, v ssa.Value, m reachMode, weak bool) {
	done := ix.tracedRead
	if m.write {
		done = ix.traced
	}
	if done[v] {
		return
	}
	done[v] = true
	flood := m.write && !weak && !ix.std(fn)
	_, isIface := ix.ifaceKey(v.Type())
	switch v := v.(type) {
	case *ssa.Const:
		if isIface {
			return // a nil interface holds nothing
		}
	case *ssa.Function:
		if m.write {
			ix.funcBoxed[v] = true
		}
		return
	case *ssa.MakeClosure:
		if f, ok := v.Fn.(*ssa.Function); ok && m.write {
			ix.funcBoxed[f] = true
		}
		for _, b := range v.Bindings {
			ix.reach(b.Type(), m)
		}
		return
	case *ssa.MakeInterface:
		ix.trace(fn, v.X, m, weak)
		return
	case *ssa.ChangeInterface:
		ix.trace(fn, v.X, m, weak)
		return
	case *ssa.Phi:
		for _, e := range v.Edges {
			ix.trace(fn, e, m, weak)
		}
		return
	case *ssa.Slice:
		// The operands packed into the slice of a variadic call.
		if ops, ok := flow.VarargsOperands(v); ok {
			for _, op := range ops {
				if op != nil {
					ix.trace(fn, op, m, weak)
				}
			}
			return
		}
	case *ssa.Parameter:
		if !isIface {
			break
		}
		if flood {
			ix.tracedParams = append(ix.tracedParams, v)
		}
		sites := ix.callers[fn]
		if len(sites) == 0 {
			break
		}
		index := paramIndex(v)
		for _, site := range sites {
			if op := argument(site.Common(), fn, index); op != nil {
				ix.trace(site.Parent(), op, m, weak)
			} else if flood {
				ix.reach(v.Type(), anything)
			}
		}
		return
	}
	if isIface {
		// The trace ends at an interface value of unknown dynamic type.
		if flood {
			ix.reach(v.Type(), anything)
		}
		return
	}
	// A value the standard library made itself and hands to reflection
	// is reached without going through the interfaces inside it.
	m.deep = m.deep && !ix.std(fn)
	ix.reach(v.Type(), m)
}

// settle exposes the static types of the traced parameters of functions
// whose callers turned out not to be all seen, until nothing changes: a
// trace through such a parameter saw only some of its values.
func (ix *fieldIndex) settle() {
	done := make(map[*ssa.Parameter]bool)
	for changed := true; changed; {
		changed = false
		for _, p := range ix.tracedParams {
			if done[p] {
				continue
			}
			if _, ok := ix.callersSeen(p.Parent()); !ok {
				done[p] = true
				changed = true
				ix.reach(p.Type(), anything)
			}
		}
	}
}

// convert indexes a conversion from type from to type to. Two struct
// types converted to one another (or pointers to them) hold the same
// fields: their locations are united. A conversion between a pointer and
// unsafe.Pointer, outside the trusted packages, is a sink.
func (ix *fieldIndex) convert(safe bool, from, to types.Type) {
	if isUnsafePointer(from) != isUnsafePointer(to) {
		if !safe {
			// Unsafe code reinterprets the memory the pointer leads
			// to; it is taken not to go on through the interface
			// values stored there.
			ix.exposeLater = append(ix.exposeLater, exposedType{from, reachMode{write: true}}, exposedType{to, reachMode{write: true}})
		}
		return
	}
	a, b := structOf(from, false), structOf(to, false)
	if a == nil || b == nil {
		a, b = structOf(from, true), structOf(to, true)
	}
	if a == nil || b == nil {
		return
	}
	n := a.Underlying().(*types.Struct).NumFields()
	if n != b.Underlying().(*types.Struct).NumFields() {
		return
	}
	for i := range n {
		ka, oka := fieldKeyOf(a, i)
		kb, okb := fieldKeyOf(b, i)
		if oka && okb && ka != kb {
			ix.union(ka, kb)
		}
	}
}

func isUnsafePointer(t types.Type) bool {
	b, ok := types.Unalias(t).Underlying().(*types.Basic)
	return ok && b.Kind() == types.UnsafePointer
}

// call indexes a call site: it is a caller of each of its Candidates. A
// call to an entry into reflection or to a function without a body is a
// sink for its arguments, and what a function without a body returns was
// built by it.
func (ix *fieldIndex) call(site ssa.CallInstruction, safe bool) {
	common := site.Common()
	if _, ok := common.Value.(*ssa.Builtin); ok {
		return
	}
	if common.IsInvoke() {
		ix.invoked[common.Method.Name()] = true
	}
	dynamic := ix.res.IsDynamic(site)
	bodiless, sink := false, false
	fn := site.Parent()
	callees := ix.res.Callees(site)
	if len(callees) == 0 && !safe {
		// A dynamic call with no Candidates: what it returns was built
		// by a function that is not seen.
		res := common.Signature().Results()
		for i := range res.Len() {
			ix.exposeLater = append(ix.exposeLater, exposedType{res.At(i).Type(), reachMode{write: true}})
		}
	}
	for _, g := range callees {
		ix.callers[g] = append(ix.callers[g], site)
		ix.refs[fn] = append(ix.refs[fn], g)
		if dynamic {
			ix.dynCalled[g] = true
		}
		bodiless = bodiless || g.Blocks == nil
		sink = sink || sinkCallee(g)
		if recv := g.Signature.Recv(); g.Blocks == nil && recv != nil && common.IsInvoke() && !safe {
			// The receiver of a method without a body, called through
			// an interface, has that method's receiver type.
			ix.exposeLater = append(ix.exposeLater, exposedType{recv.Type(), reachMode{write: true}})
		}
	}
	if !bodiless && !sink {
		return
	}
	args := common.Args
	// What reflection is given, it may follow through interfaces, but
	// not through unexported fields; functions without a body (assembly,
	// linknamed, cgo) are taken not to follow interfaces.
	m := reachMode{write: true}
	if sink && !bodiless {
		m = reachMode{write: true, deep: true, exported: true}
		if strings.HasPrefix(pkgPath(fn), "encoding/json") {
			m.tag = "json"
		}
	}
	if sink && !bodiless && reflectReads(site) {
		// The reflect.Value or reflect.Type made here is only inspected.
		safe = true
	}
	// reflect.TypeOf gives a type, not the value: it writes nothing. A
	// value of that type that reflection goes on to build is seen where
	// the type was a concrete one converted for the call.
	weak := false
	if callee := common.StaticCallee(); callee != nil && sink && !bodiless && callee.Name() == "TypeOf" && callee.Signature.Recv() == nil {
		weak = true
	}
	for _, arg := range args {
		if safe {
			ix.readSinks = append(ix.readSinks, sinkArg{fn, arg, reachMode{}, true})
		} else {
			ix.sinks = append(ix.sinks, sinkArg{fn, arg, m, weak})
		}
	}
	if bodiless && !safe {
		res := common.Signature().Results()
		for i := range res.Len() {
			ix.exposeLater = append(ix.exposeLater, exposedType{res.At(i).Type(), reachMode{write: true}})
		}
	}
}

// valueReaders are the methods of reflect.Value and reflect.Type that only
// inspect, and valueSteps those that give another Value or Type to
// inspect.
var (
	valueReaders = map[string]bool{
		"Kind": true, "Type": true, "IsValid": true, "IsNil": true, "IsZero": true, "Len": true, "Cap": true,
		"Int": true, "Uint": true, "Float": true, "Complex": true, "Bool": true, "String": true,
		"NumField": true, "NumMethod": true, "CanSet": true, "CanAddr": true, "CanInterface": true,
		"CanInt": true, "CanUint": true, "CanFloat": true, "CanComplex": true, "Comparable": true, "Equal": true,
		"Name": true, "PkgPath": true, "Size": true, "Bits": true, "Implements": true, "AssignableTo": true,
		"ConvertibleTo": true, "NumIn": true, "NumOut": true, "IsVariadic": true, "Align": true, "FieldAlign": true,
	}
	valueSteps = map[string]bool{
		"Elem": true, "Field": true, "FieldByName": true, "FieldByIndex": true, "Index": true, "MapIndex": true,
		"Key": true, "In": true, "Out": true,
	}
)

// reflectReads reports whether the call site, an entry into reflection,
// only inspects its argument: it is reflect.DeepEqual, or a call of
// reflect.ValueOf or reflect.TypeOf whose result is used, in its
// function, only through the methods that read (Kind, Type, Field, Int,
// ...): never set, addressed, called, converted back to an interface,
// stored, returned or passed on.
func reflectReads(site ssa.CallInstruction) bool {
	callee := site.Common().StaticCallee()
	if callee == nil || callee.Signature.Recv() != nil {
		return false
	}
	switch callee.Name() {
	case "DeepEqual":
		return pkgPath(callee) == "reflect"
	case "ValueOf", "TypeOf":
		call, ok := site.(*ssa.Call)
		return ok && onlyInspected(call, make(map[ssa.Value]bool))
	}
	return false
}

// onlyInspected reports whether the reflect.Value or reflect.Type v is
// used only through methods that read (see reflectReads).
func onlyInspected(v ssa.Value, seen map[ssa.Value]bool) bool {
	if seen[v] {
		return true
	}
	seen[v] = true
	refs := v.Referrers()
	if refs == nil {
		return true
	}
	for _, ref := range *refs {
		switch ref := ref.(type) {
		case *ssa.DebugRef:
		case *ssa.BinOp:
			if ref.Op != token.EQL && ref.Op != token.NEQ {
				return false
			}
		case *ssa.Phi:
			if !onlyInspected(ref, seen) {
				return false
			}
		case *ssa.MakeInterface:
			// Printed: handed, as an interface, to a trusted package.
			if !onlyTrustedArgs(ref) {
				return false
			}
		case *ssa.ChangeInterface:
			if !onlyTrustedArgs(ref) {
				return false
			}
		case *ssa.Call:
			common := ref.Common()
			name, recv := "", ssa.Value(nil)
			switch callee := common.StaticCallee(); {
			case common.IsInvoke():
				name, recv = common.Method.Name(), common.Value
			case callee != nil && callee.Signature.Recv() != nil && len(common.Args) > 0:
				if p := pkgPath(callee); p != "reflect" && p != "internal/reflectlite" {
					return false
				}
				name, recv = callee.Name(), common.Args[0]
			}
			if recv != v {
				return false
			}
			for i, arg := range common.Args {
				if arg == v && (common.IsInvoke() || i > 0) {
					return false
				}
			}
			switch {
			case valueReaders[name]:
			case valueSteps[name]:
				if !onlyInspected(ref, seen) {
					return false
				}
			default:
				return false
			}
		default:
			return false
		}
	}
	return true
}

// onlyTrustedArgs reports whether the interface value v is used only as
// an argument (or a variadic operand) of calls to functions of the
// trusted packages.
func onlyTrustedArgs(v ssa.Value) bool {
	trustedCall := func(instr ssa.Instruction) bool {
		site, ok := instr.(ssa.CallInstruction)
		if !ok {
			return false
		}
		callee := site.Common().StaticCallee()
		return callee != nil && trusted(callee) && !sinkCallee(callee)
	}
	refs := v.Referrers()
	if refs == nil {
		return true
	}
	for _, ref := range *refs {
		switch ref := ref.(type) {
		case *ssa.DebugRef:
		case *ssa.Phi:
			if len(ref.Edges) > 8 || !onlyTrustedArgs(ref) {
				return false
			}
		case *ssa.ChangeInterface:
			if !onlyTrustedArgs(ref) {
				return false
			}
		case *ssa.Store:
			ia, ok := ref.Addr.(*ssa.IndexAddr)
			if !ok || ref.Val != v {
				return false
			}
			alloc, ok := ia.X.(*ssa.Alloc)
			if !ok || alloc.Comment != "varargs" {
				return false
			}
			for _, ar := range *alloc.Referrers() {
				switch ar := ar.(type) {
				case *ssa.IndexAddr, *ssa.DebugRef:
				case *ssa.Slice:
					for _, sr := range *ar.Referrers() {
						if _, dbg := sr.(*ssa.DebugRef); !dbg && !trustedCall(sr) {
							return false
						}
					}
				default:
					return false
				}
			}
		default:
			if !trustedCall(ref) {
				return false
			}
		}
	}
	return true
}

// closure indexes the creation of a closure: its function is used as a
// value unless the closure is only ever called where it is made.
func (ix *fieldIndex) closure(mc *ssa.MakeClosure) {
	fn, ok := mc.Fn.(*ssa.Function)
	if !ok {
		return
	}
	refs := mc.Referrers()
	if refs == nil {
		return
	}
	for _, ref := range *refs {
		if _, ok := ref.(*ssa.DebugRef); ok {
			continue
		}
		site, ok := ref.(ssa.CallInstruction)
		if !ok || site.Common().Value != mc {
			ix.addrTaken[fn] = true
			return
		}
		for _, arg := range site.Common().Args {
			if arg == mc {
				ix.addrTaken[fn] = true
				return
			}
		}
	}
}

// findEntry finds the functions reachable from the main and init
// functions among fns, through calls and references.
func (ix *fieldIndex) findEntry(fns []*ssa.Function) {
	var work []*ssa.Function
	for _, fn := range fns {
		if isEntry(fn) {
			ix.entry[fn] = true
			work = append(work, fn)
		}
	}
	for len(work) > 0 {
		fn := work[len(work)-1]
		work = work[:len(work)-1]
		for _, g := range ix.refs[fn] {
			if !ix.entry[g] {
				ix.entry[g] = true
				work = append(work, g)
			}
		}
	}
}

// scanUnreached scans the functions outside the reachable program that
// reflection may call: the methods of the exposed types, and the methods
// of the types that reach a sink in a trusted package that have the name
// of a method reachable code calls through an interface, with everything
// they call
// or refer to that is outside the reachable program too. A field such a
// function stores to (or takes the address of) keeps an Unresolved value
// naming it; what it calls has a caller that is not seen. The reachable
// methods of the exposed types may be called by reflection as well.
func (ix *fieldIndex) scanUnreached() {
	for _, a := range ix.readSinks {
		ix.trace(a.fn, a.v, reachMode{}, true)
	}
	var work []*ssa.Function
	queued := make(map[*ssa.Function]bool)
	push := func(fn *ssa.Function) {
		if fn != nil && fn.Blocks != nil && !ix.reachable[fn] && !queued[fn] {
			queued[fn] = true
			work = append(work, fn)
		}
	}
	methods := func(named []*types.Named, any bool) {
		for _, n := range named {
			if _, iface := n.Underlying().(*types.Interface); iface || (n.TypeParams().Len() > 0 && n.TypeArgs().Len() == 0) {
				continue
			}
			for _, t := range []types.Type{n, types.NewPointer(n)} {
				mset := ix.prog.MethodSets.MethodSet(t)
				for i := range mset.Len() {
					sel := mset.At(i)
					if !any && !ix.invoked[sel.Obj().Name()] {
						continue
					}
					fn := ix.prog.MethodValue(sel)
					if fn == nil {
						continue
					}
					if any && ix.reachable[fn] {
						ix.reflectCallable[fn] = true
					}
					push(fn)
				}
			}
		}
	}
	methods(ix.exposedNamed, true)
	methods(ix.seenNamed, false)
	var buf [10]*ssa.Value
	var byName map[string][]*ssa.Function
	for len(work) > 0 {
		fn := work[0]
		work = work[1:]
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				switch instr := instr.(type) {
				case *ssa.DebugRef:
					continue
				case *ssa.FieldAddr:
					ix.unreachedFieldAddr(fn, instr)
				}
				if site, ok := instr.(ssa.CallInstruction); ok {
					common := site.Common()
					_, builtin := common.Value.(*ssa.Builtin)
					switch {
					case common.IsInvoke():
						name := common.Method.Name()
						if !ix.unseenInvoke[name] {
							ix.unseenInvoke[name] = true
							if byName == nil {
								byName = ix.methodsByName()
							}
							for _, m := range byName[name] {
								push(m)
							}
						}
					case common.StaticCallee() == nil && !builtin:
						ix.unseenDynCall = true
					}
				}
				for _, op := range instr.Operands(buf[:0]) {
					if g, ok := (*op).(*ssa.Function); ok {
						if ix.reachable[g] {
							ix.unseenCaller[g] = true
						}
						push(g)
					}
				}
			}
		}
	}
}

// methodsByName returns the methods of the program's runtime types (those
// converted to interfaces, and those reflection reaches from them) that
// are outside the reachable program, by name.
func (ix *fieldIndex) methodsByName() map[string][]*ssa.Function {
	out := make(map[string][]*ssa.Function)
	seen := make(map[*ssa.Function]bool)
	for _, t := range ix.prog.RuntimeTypes() {
		mset := ix.prog.MethodSets.MethodSet(t)
		for i := range mset.Len() {
			fn := ix.prog.MethodValue(mset.At(i))
			if fn != nil && fn.Blocks != nil && !ix.reachable[fn] && !seen[fn] {
				seen[fn] = true
				out[fn.Name()] = append(out[fn.Name()], fn)
			}
		}
	}
	for _, fns := range out {
		sortFuncs(fns)
	}
	return out
}

// unreachedFieldAddr records that fn, a function outside the reachable
// program, may write the field whose address fa is: it stores to it or
// uses the address other than to load from it.
func (ix *fieldIndex) unreachedFieldAddr(fn *ssa.Function, fa *ssa.FieldAddr) {
	st := structOf(fa.X.Type(), true)
	if st == nil {
		return
	}
	k, ok := fieldKeyOf(st, fa.Field)
	if !ok || fa.Referrers() == nil {
		return
	}
	for _, ref := range *fa.Referrers() {
		switch ref := ref.(type) {
		case *ssa.DebugRef:
			continue
		case *ssa.UnOp:
			if ref.Op == token.MUL {
				continue
			}
		}
		if r := ix.rawAt(k, st, fa.Field); r.unreached == "" {
			r.unreached = fn.String()
		}
		return
	}
}

// callersSeen returns the call sites of fn, and whether they are all of
// its callers as far as the analysis can tell (see the top of this file).
func (ix *fieldIndex) callersSeen(fn *ssa.Function) ([]ssa.CallInstruction, bool) {
	sites := ix.callers[fn]
	switch {
	case len(sites) == 0, ix.unseenCaller[fn], ix.funcBoxed[fn], ix.reflectCallable[fn]:
		return nil, false
	case fn.Signature.Recv() != nil && ix.unseenInvoke[fn.Name()]:
		return nil, false
	}
	if ix.addrTaken[fn] {
		if !ix.dynCalled[fn] || ix.unseenDynCall {
			return nil, false
		}
		for _, sig := range ix.boxedSigs {
			if types.Identical(sig.Params(), fn.Signature.Params()) && types.Identical(sig.Results(), fn.Signature.Results()) {
				return nil, false
			}
		}
	}
	return sites, true
}
