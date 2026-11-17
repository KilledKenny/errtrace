package mark

import (
	"go/constant"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/flow"
	"github.com/KilledKenny/errtrace/internal/fmtverb"
)

// model is a function whose effect on Marks the pass models itself,
// because its body moves values where the pass cannot see (reflection,
// unsafe).
type model uint8

const (
	modelNone model = iota
	// modelErrorsUnwrap is errors.Unwrap.
	modelErrorsUnwrap
	// modelErrorsAs is errors.As.
	modelErrorsAs
	// modelErrorsIs is errors.Is.
	modelErrorsIs
	// modelAtomicStore is Store, Swap or CompareAndSwap of
	// sync/atomic.Value or atomic.Pointer: its arguments are stored.
	modelAtomicStore
	// modelAtomicLoad is Load of those types.
	modelAtomicLoad
	// modelAtomicSwap is Swap: it stores and loads.
	modelAtomicSwap
	// modelReflect is a function or method of package reflect: what it
	// is passed may come back in what it returns (reflect.ValueOf(x),
	// v.Elem(), v.Interface()).
	modelReflect
)

func (a *analysis) model(fn *ssa.Function) model {
	if fn == nil {
		return modelNone
	}
	if m, ok := a.models[fn]; ok {
		return m
	}
	m := modelOf(fn)
	if m == modelReflect && !a.modelReflect {
		m = modelNone
	}
	a.models[fn] = m
	return m
}

func modelOf(fn *ssa.Function) model {
	o := fn
	if fn.Origin() != nil {
		o = fn.Origin()
	}
	if o.Pkg == nil || o.Pkg.Pkg == nil {
		return modelNone
	}
	recv := ""
	if r := o.Signature.Recv(); r != nil {
		t := r.Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		n, ok := types.Unalias(t).(*types.Named)
		if !ok {
			return modelNone
		}
		recv = n.Obj().Name()
	}
	switch o.Pkg.Pkg.Path() {
	case "errors":
		if recv == "" {
			switch o.Name() {
			case "Unwrap":
				return modelErrorsUnwrap
			case "As":
				return modelErrorsAs
			case "Is":
				return modelErrorsIs
			}
		}
	case "sync/atomic":
		if recv == "Value" || recv == "Pointer" {
			switch o.Name() {
			case "Store", "CompareAndSwap":
				return modelAtomicStore
			case "Swap":
				return modelAtomicSwap
			case "Load":
				return modelAtomicLoad
			}
		}
	case "reflect":
		return modelReflect
	}
	return modelNone
}

// intrinsic reports whether a static call of fn is modelled as an
// intrinsic (see flow.Intrinsic), with the intrinsic's name.
func (a *analysis) intrinsic(fn *ssa.Function) (string, bool) {
	if !a.modelIntrinsics {
		return "", false
	}
	return flow.Intrinsic(fn)
}

// indexCall records what the call at site reads, during the index scan.
func (a *analysis) indexCall(site ssa.CallInstruction) {
	common := site.Common()
	call, _ := site.(*ssa.Call)
	if b, ok := common.Value.(*ssa.Builtin); ok {
		if b.Name() == "recover" && call != nil {
			a.recovers = append(a.recovers, recoverCall{call.Parent(), key{call, 0}})
		}
		return
	}
	if sc := common.StaticCallee(); sc != nil && call != nil {
		if name, ok := a.intrinsic(sc); ok {
			a.indexIntrinsic(call, name)
			return
		}
		switch a.model(sc) {
		case modelAtomicLoad, modelAtomicSwap:
			if res := common.Signature().Results(); res.Len() == 1 && a.markable(res.At(0).Type()) {
				a.addReader(a.atomicLoc(res.At(0).Type()), key{call, 0})
			}
		case modelReflect:
			if reflectReads(sc) {
				a.reflectReads = append(a.reflectReads, reflectRead{call, sc})
			}
			res := common.Signature().Results()
			if reflectBoxes(sc) {
				a.boxes[call] = true
			}
			if reflectMakes(sc) && a.madeMayBeSeed(call, sc) {
				// A value of a type chosen at run time: it may be of a
				// type every value of which is marked (see seed).
				for r := range res.Len() {
					if !reflectGives(sc, r) {
						continue
					}
					k := key{call, 0}
					if res.Len() > 1 {
						k.idx = int32(r)
					}
					a.reflectMade = append(a.reflectMade, k)
				}
			}
			for _, arg := range common.Args {
				// A composite argument is not marked itself: what it
				// holds is looked at when the pass settles (see run).
				if mi, ok := arg.(*ssa.MakeInterface); ok && !isInterface(mi.X.Type()) && !a.markable(mi.X.Type()) {
					a.reflectArgs = append(a.reflectArgs, reflectArg{call: call, typ: mi.X.Type()})
				}
				// The reflect.Values of a slice argument are memory.
				if st, ok := arg.Type().Underlying().(*types.Slice); ok && reflectCarrier(st.Elem()) {
					for r := range res.Len() {
						if !reflectGives(sc, r) {
							continue
						}
						k := key{call, 0}
						if res.Len() > 1 {
							k.idx = int32(r)
						}
						a.addReader(a.typedLoc(st.Elem()), k)
						if l, ok := a.varargsParam[sliceAlloc(arg)]; ok {
							a.addReader(l, k)
						}
					}
				}
			}
		}
	}
	if call != nil && common.StaticCallee() == nil && a.modelReflect {
		// A dynamic call that may run a function of package reflect
		// (a method of reflect.Value called through an interface).
		for _, g := range a.callees(site) {
			if a.model(g) == modelReflect && reflectReads(g) {
				a.reflectReads = append(a.reflectReads, reflectRead{call, g})
			}
		}
	}
	a.indexCallees(site)
}

// indexIntrinsic records how the operands of the intrinsic call reach it:
// through the argument array the call owns, or, when the operands come in
// a slice the pass does not follow, from the memory of the slice's
// element type.
func (a *analysis) indexIntrinsic(call *ssa.Call, name string) {
	args := call.Call.Args
	var slice ssa.Value
	wraps := true
	switch name {
	case "fmt.Errorf":
		if len(args) != 2 {
			return
		}
		if format, ok := constFormat(args[0]); ok && len(fmtverb.Wrapped(fmtverb.Parse(format, -1))) == 0 {
			wraps = false
		}
		slice = args[1]
	case "errors.Join":
		if len(args) != 1 {
			return
		}
		slice = args[0]
	default:
		return
	}
	if _, ok := flow.VarargsOperands(slice); ok {
		sl, isSlice := slice.(*ssa.Slice)
		if !isSlice {
			// A nil slice: no operands.
			return
		}
		if alloc, ok := sl.X.(*ssa.Alloc); ok && onlyUsedBy(sl, call) {
			// The array is read by the intrinsic alone: a store into
			// it is an operand, not a store to memory.
			a.varargs[alloc] = call
			return
		}
	}
	if !wraps {
		return
	}
	// The operands may be a slice parameter of the caller
	// (fmt.Errorf(format, args...)), whose elements may be in no other
	// memory.
	if l, ok := sliceParam(slice); ok {
		a.addReader(l, key{call, 0})
		if a.closed(l) {
			return
		}
	}
	if st, ok := slice.Type().Underlying().(*types.Slice); ok {
		a.addReader(a.typedLoc(st.Elem()), key{call, 0})
	}
}

// onlyUsedBy reports whether call is the only user of v.
func onlyUsedBy(v ssa.Value, call *ssa.Call) bool {
	refs := v.Referrers()
	if refs == nil {
		return false
	}
	for _, r := range *refs {
		switch r := r.(type) {
		case *ssa.DebugRef:
		case *ssa.Call:
			if r != call {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func constFormat(v ssa.Value) (string, bool) {
	c, ok := v.(*ssa.Const)
	if !ok || c.Value == nil || c.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(c.Value), true
}

// evalIntrinsic marks the result of a static call of an intrinsic from
// its operands: errtrace.Wrap and errors.Join hold every operand,
// fmt.Errorf the operands at its %w verbs (every operand if its format is
// not a constant). errors.New holds nothing.
func (a *analysis) evalIntrinsic(call *ssa.Call) {
	name, _ := a.intrinsic(call.Call.StaticCallee())
	args := call.Call.Args
	var t tags
	var from ssa.Value
	use := func(op ssa.Value) {
		if op == nil {
			return
		}
		if ot := a.tagsOf(op); ot != 0 {
			if from == nil {
				from = op
			}
			t |= ot
		}
	}
	switch name {
	case "errtrace.Wrap":
		if len(args) > 0 {
			use(args[0])
		}
	case "errors.Join":
		if len(args) != 1 {
			return
		}
		ops, _ := flow.VarargsOperands(args[0])
		for _, op := range ops {
			use(op)
		}
	case "fmt.Errorf":
		if len(args) != 2 {
			return
		}
		ops, _ := flow.VarargsOperands(args[1])
		if format, ok := constFormat(args[0]); ok {
			for _, i := range fmtverb.Wrapped(fmtverb.Parse(format, len(ops))) {
				if i < len(ops) {
					use(ops[i])
				}
			}
		} else {
			for _, op := range ops {
				use(op)
			}
		}
	}
	if t == 0 {
		return
	}
	a.mark(key{call, 0}, t, reason{kind: kWrap, from: valueRef(key{from, 0}, t), site: call, text: name})
}

// callUse handles the marked value k as an operand of the call at site.
func (a *analysis) callUse(k key, delta tags, site ssa.CallInstruction) {
	v := k.v
	common := site.Common()
	call, _ := site.(*ssa.Call)
	from := valueRef(k, delta)
	if a.open != nil && call != nil && common.StaticCallee() == nil && common.Value == v {
		// The function value or receiver of a dynamic call is marked:
		// it may come from outside the program.
		a.outsideResults(call, delta, from)
	}
	if sc := common.StaticCallee(); sc != nil {
		if _, ok := a.intrinsic(sc); ok {
			if call != nil {
				a.evalIntrinsic(call)
			}
			return
		}
		switch a.model(sc) {
		case modelErrorsUnwrap:
			if call != nil && len(common.Args) == 1 && common.Args[0] == v {
				a.markResult(call, 0, delta, reason{kind: kUnwrap, from: from, site: site})
			}
		case modelErrorsAs:
			if len(common.Args) == 2 && common.Args[0] == v {
				a.asTarget(common.Args[1], from, planesOf(delta), site)
			}
		case modelReflect:
			// What goes into reflection may come out of the call: a
			// reflect.Value that holds it, or the value itself.
			if reflectStore(sc) && len(common.Args) > 1 && common.Args[0] != v {
				a.reflectStored(site, common.Args[0], planesOf(delta), reason{kind: kStore, from: from, site: site, text: "reflection"})
			}
			if call != nil {
				a.reflectResults(call, delta, reason{kind: kThrough, from: from, site: site, fn: sc})
				a.reflectYields(call, sc, delta, from)
			}
			// Its body is not followed.
			return
		case modelAtomicStore, modelAtomicSwap:
			for _, arg := range common.Args[1:] {
				if arg == v {
					a.markLoc(a.atomicLoc(v.Type()), planesOf(delta), reason{kind: kStore, from: from, site: site})
				}
			}
		}
	}
	// A call of a method named Unwrap on a marked receiver gives a
	// marked result. A slice it returns (Unwrap() []error) holds what
	// the receiver holds: the operands of errors.Join and of a
	// fmt.Errorf with several %w are in no memory the pass saw a store
	// to.
	if call != nil && unwrapReceiver(common) == v {
		res := common.Signature().Results()
		why := reason{kind: kUnwrap, from: from, site: site}
		for r := range res.Len() {
			if a.markable(res.At(r).Type()) {
				a.markResult(call, r, delta, why)
				continue
			}
			if res.Len() == 1 {
				a.sliceHolds(call, delta, why, make(map[ssa.Value]bool))
				continue
			}
			if refs := call.Referrers(); refs != nil {
				for _, x := range *refs {
					if ex, ok := x.(*ssa.Extract); ok && ex.Index == r {
						a.sliceHolds(ex, delta, why, make(map[ssa.Value]bool))
					}
				}
			}
		}
	}
	a.linkArgs(k, delta, site)
}

// sliceHolds handles v, a slice or array (or a reslicing or phi of one)
// that holds values with the tags t: the result of an Unwrap() []error
// method called on a marked receiver. The values loaded from it where it
// is made are marked with those tags. If it goes anywhere else (it is
// stored, returned, passed on, appended to), its elements are from then
// on memory of their type.
func (a *analysis) sliceHolds(v ssa.Value, t tags, why reason, seen map[ssa.Value]bool) {
	if seen[v] {
		return
	}
	seen[v] = true
	var elem types.Type
	switch u := v.Type().Underlying().(type) {
	case *types.Slice:
		elem = u.Elem()
	case *types.Array:
		elem = u.Elem()
	case *types.Pointer:
		// A slice converted to an array pointer, or the address of an
		// element.
		if arr, ok := u.Elem().Underlying().(*types.Array); ok {
			elem = arr.Elem()
		}
	}
	if elem == nil || !a.markable(elem) {
		// (errors.Is and errors.As only ask an Unwrap() []error.)
		return
	}
	escapes := func() {
		a.markLoc(a.typedLoc(elem), planesOf(t), why)
	}
	refs := v.Referrers()
	if refs == nil {
		escapes()
		return
	}
	for _, r := range *refs {
		switch x := r.(type) {
		case *ssa.DebugRef:
		case *ssa.Phi:
			a.sliceHolds(x, t, why, seen)
		case *ssa.Slice:
			a.sliceHolds(x, t, why, seen)
		case *ssa.Index:
			// An element of an array value.
			a.mark(key{x, 0}, t, why)
		case *ssa.IndexAddr:
			if x.X != v || x.Referrers() == nil {
				escapes()
				continue
			}
			for _, rr := range *x.Referrers() {
				switch y := rr.(type) {
				case *ssa.DebugRef:
				case *ssa.UnOp:
					if y.Op != token.MUL {
						escapes()
						continue
					}
					a.mark(key{y, 0}, t, why)
				case *ssa.Store:
					// A store into an element is seen as any store.
					if y.Val == ssa.Value(x) {
						escapes()
					}
				default:
					escapes()
				}
			}
		case *ssa.Call:
			if b, ok := x.Call.Value.(*ssa.Builtin); ok && (b.Name() == "len" || b.Name() == "cap") {
				continue
			}
			escapes()
		default:
			escapes()
		}
	}
}

// unwrapReceiver returns the receiver of a call of a method named Unwrap,
// or nil.
func unwrapReceiver(common *ssa.CallCommon) ssa.Value {
	if common.IsInvoke() {
		if common.Method.Name() == "Unwrap" {
			return common.Value
		}
		return nil
	}
	if sc := common.StaticCallee(); sc != nil && sc.Signature.Recv() != nil && sc.Name() == "Unwrap" && len(common.Args) > 0 {
		return common.Args[0]
	}
	return nil
}

// asTarget marks what errors.As may store into its target when its error
// operand is marked: the variable the target points to, if the target is
// a pointer converted at the call; else every memory a pointer converted
// to an interface anywhere in the program points to.
func (a *analysis) asTarget(target ssa.Value, from ref, planes tags, site ssa.Instruction) {
	why := reason{kind: kAs, from: from, site: site}
	if mi, ok := target.(*ssa.MakeInterface); ok {
		pe := pointerElem(mi.X.Type())
		if pe == nil || !a.markable(pe) || !a.assertable(pe) {
			return
		}
		locs, n := a.addrLocs(mi.X)
		for _, l := range locs[:n] {
			a.markLoc(l, planes, why)
		}
		return
	}
	if a.asUnknown&planes == planes {
		return
	}
	a.asUnknown |= planes
	for _, t := range a.asPointees {
		a.markLoc(a.typedLoc(t), planes, why)
	}
}

// reflectArg is a composite value passed to a function of package reflect;
// done are the planes in which the call's results are marked for it.
type reflectArg struct {
	call *ssa.Call
	typ  types.Type
	done tags
}

// sliceAlloc returns the argument array that the slice v is made from, or
// nil.
func sliceAlloc(v ssa.Value) *ssa.Alloc {
	if sl, ok := v.(*ssa.Slice); ok {
		alloc, _ := sl.X.(*ssa.Alloc)
		return alloc
	}
	return nil
}

// reflectCarrier reports whether a value of type t that a function of
// package reflect returns can give back what the function was passed: a
// reflect.Value, a *reflect.MapIter (Value.MapRange), or an empty
// interface (Value.Interface). A reflect.Type or a bool cannot.
func reflectCarrier(t types.Type) bool {
	if isReflectValue(t) || isReflectIter(t) {
		return true
	}
	if _, isParam := t.(*types.TypeParam); isParam {
		return true
	}
	i, ok := t.Underlying().(*types.Interface)
	return ok && i.NumMethods() == 0
}

// reflectGives reports whether result r of the function fn of package
// reflect can give back what fn was passed: its type is a carrier (see
// reflectCarrier), or fn is an instance of a generic function and the
// result has the type of a type parameter (reflect.TypeAssert[T], whose
// result is what the Value holds, as a T).
func reflectGives(fn *ssa.Function, r int) bool {
	res := fn.Signature.Results()
	if r >= res.Len() {
		return false
	}
	if reflectCarrier(res.At(r).Type()) {
		return true
	}
	if o := fn.Origin(); o != nil {
		if ores := o.Signature.Results(); r < ores.Len() {
			_, isParam := types.Unalias(ores.At(r).Type()).(*types.TypeParam)
			return isParam
		}
	}
	return false
}

// reflectMakes reports whether the function or method fn of package
// reflect returns a value of a type it is told at run time: it has a
// parameter of type reflect.Type (New, Zero, MakeSlice, MakeMap,
// MakeChan, MakeFunc, NewAt, SliceAt, Value.Convert).
func reflectMakes(fn *ssa.Function) bool {
	params := fn.Signature.Params()
	for i := range params.Len() {
		if isReflectType(params.At(i).Type()) {
			return true
		}
	}
	return false
}

// madeMayBeSeed reports whether the value that the call of the function
// sc of package reflect makes (see reflectMakes) may be, or hold in the
// memory made for it, a value of a type every value of which is marked
// (see typeSeed). It may, unless the pass can tell the type: the
// reflect.Type operand is reflect.TypeOf(x) for an x of a known static
// type, reflect.TypeFor[T](), or the Elem of such a type, and no type
// seed is in it. (reflect.NewAt and SliceAt name memory that is already
// there: anything may be in it.)
func (a *analysis) madeMayBeSeed(call *ssa.Call, sc *ssa.Function) bool {
	if sc.Signature.Recv() == nil && (sc.Name() == "NewAt" || sc.Name() == "SliceAt") {
		return true
	}
	for _, arg := range call.Call.Args {
		if !isReflectType(arg.Type()) {
			continue
		}
		t := a.staticReflectType(arg, 0)
		if t == nil || a.seedIn(t, make(map[types.Type]bool), 0) {
			return true
		}
		if sc.Signature.Recv() == nil && sc.Name() == "New" && a.typeSeed(types.NewPointer(t)) {
			// reflect.New(t) is a *t.
			return true
		}
	}
	return false
}

// isReflectType reports whether t is reflect.Type.
func isReflectType(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Name() == "Type" && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "reflect"
}

// staticReflectType returns the type that the reflect.Type v stands for,
// if v is reflect.TypeOf(x) for an x of a static type that is not an
// interface, reflect.TypeFor[T](), or the Elem of such a type; nil if the
// pass cannot tell.
func (a *analysis) staticReflectType(v ssa.Value, depth int) types.Type {
	call, ok := v.(*ssa.Call)
	if !ok || depth > 8 {
		return nil
	}
	common := call.Common()
	if common.IsInvoke() {
		if common.Method.Name() != "Elem" || len(common.Args) != 0 || !isReflectType(common.Value.Type()) {
			return nil
		}
		t := a.staticReflectType(common.Value, depth+1)
		if t == nil {
			return nil
		}
		switch u := t.Underlying().(type) {
		case *types.Pointer:
			return u.Elem()
		case *types.Slice:
			return u.Elem()
		case *types.Array:
			return u.Elem()
		case *types.Chan:
			return u.Elem()
		case *types.Map:
			return u.Elem()
		}
		return nil
	}
	sc := common.StaticCallee()
	if a.model(sc) != modelReflect || sc.Signature.Recv() != nil {
		return nil
	}
	switch sc.Name() {
	case "TypeOf":
		if len(common.Args) != 1 {
			return nil
		}
		mi, ok := common.Args[0].(*ssa.MakeInterface)
		if !ok || isInterface(mi.X.Type()) {
			return nil
		}
		return mi.X.Type()
	}
	if o := sc.Origin(); o != nil && o.Name() == "TypeFor" && len(sc.TypeArgs()) == 1 {
		return sc.TypeArgs()[0]
	}
	return nil
}

// seedIn reports whether t is a type seed, or has one among the types of
// its fields, elements, keys and pointees. (A fresh value of an interface
// type is nil: it holds nothing.)
func (a *analysis) seedIn(t types.Type, seen map[types.Type]bool, depth int) bool {
	if depth > maxHeldDepth {
		return true
	}
	if seen[t] {
		return false
	}
	if _, ok := types.Unalias(t).(*types.Named); ok {
		seen[t] = true
		depth = 0
	}
	if _, isParam := types.Unalias(t).(*types.TypeParam); isParam {
		return true
	}
	if a.typeSeed(t) {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return a.seedIn(u.Elem(), seen, depth+1)
	case *types.Slice:
		return a.seedIn(u.Elem(), seen, depth+1)
	case *types.Array:
		return a.seedIn(u.Elem(), seen, depth+1)
	case *types.Chan:
		return a.seedIn(u.Elem(), seen, depth+1)
	case *types.Map:
		return a.seedIn(u.Key(), seen, depth+1) || a.seedIn(u.Elem(), seen, depth+1)
	case *types.Struct:
		for i := range u.NumFields() {
			if a.seedIn(u.Field(i).Type(), seen, depth+1) {
				return true
			}
		}
	}
	return false
}

// reflectRead is a call that may run the function fn of package reflect,
// which is passed a reflect.Value (see reflectReads).
type reflectRead struct {
	call *ssa.Call
	fn   *ssa.Function
}

// reflectReads reports whether the function fn of package reflect is
// passed a reflect.Value, a *reflect.MapIter or a slice of them, as its
// receiver or a parameter: what it returns may then be read from the
// memory such a value names (Value.Interface, Elem, Field, Index,
// MapIndex, Recv, reflect.Indirect, MapIter.Value). A function that is
// passed none (reflect.ValueOf, New, Zero, MakeSlice) reads no memory: it
// returns what it is passed, or memory it made.
func reflectReads(fn *ssa.Function) bool {
	reads := func(t types.Type) bool {
		if st, ok := t.Underlying().(*types.Slice); ok {
			t = st.Elem()
		}
		return isReflectValue(t) || isReflectIter(t)
	}
	sig := fn.Signature
	if recv := sig.Recv(); recv != nil && reads(recv.Type()) {
		return true
	}
	for i := range sig.Params().Len() {
		if reads(sig.Params().At(i).Type()) {
			return true
		}
	}
	return false
}

// reflectResults marks what the call of a function of package reflect
// returns: its results that can give back what it was passed, and the
// memory of the elements of a slice of them.
func (a *analysis) reflectResults(call *ssa.Call, t tags, why reason) {
	a.reflectResultsOf(call, call.Call.StaticCallee(), t, why)
}

// reflectResultsOf is reflectResults for a call that may run the function
// sc of package reflect.
func (a *analysis) reflectResultsOf(call *ssa.Call, sc *ssa.Function, t tags, why reason) {
	res := call.Call.Signature().Results()
	for r := range res.Len() {
		rt := res.At(r).Type()
		if sc != nil && reflectGives(sc, r) {
			a.markResult(call, r, t, why)
			if !isInterface(rt) && !isReflectValue(rt) && !isReflectIter(rt) {
				// reflect.TypeAssert[*T](v): as an assertion of what
				// the Value holds to a composite type.
				if t&planeH != 0 {
					a.markHeld(rt, reason{kind: kAssert, from: why.from, site: call, typ: rt})
				} else if t&boxed != 0 {
					a.markBoxed(rt, reason{kind: kAssert, from: why.from, site: call, typ: rt})
				}
			}
			continue
		}
		if st, ok := rt.Underlying().(*types.Slice); ok && reflectCarrier(st.Elem()) {
			a.markLoc(a.typedLoc(st.Elem()), planesOf(t), why)
		}
	}
}

// reflectYields handles the marked reflect.Value that is the receiver of
// the call of Value.Seq or Value.Seq2: the iterator function it returns
// passes what the Value holds to the function it is called with (the body
// of a range statement). The parameters of that function are marked where
// the iterator is called at once; an iterator that goes anywhere else is
// not followed.
func (a *analysis) reflectYields(call *ssa.Call, sc *ssa.Function, delta tags, from ref) {
	if sc.Signature.Recv() == nil || (sc.Name() != "Seq" && sc.Name() != "Seq2") {
		return
	}
	refs := call.Referrers()
	if refs == nil {
		return
	}
	for _, r := range *refs {
		site, ok := r.(ssa.CallInstruction)
		if !ok || site.Common().Value != ssa.Value(call) {
			continue
		}
		for _, arg := range site.Common().Args {
			var yield *ssa.Function
			switch x := arg.(type) {
			case *ssa.MakeClosure:
				yield, _ = x.Fn.(*ssa.Function)
			case *ssa.Function:
				yield = x
			}
			if yield == nil || !a.known(yield) {
				continue
			}
			for _, p := range yield.Params {
				// As memory: in any calling context.
				a.mark(key{p, 0}, planesOf(delta), reason{kind: kParam, from: from, site: site, fn: yield})
			}
		}
	}
}
