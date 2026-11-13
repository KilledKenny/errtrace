package mark

import (
	"go/constant"
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
	// modelAtomicStore is Store, Swap or CompareAndSwap of
	// sync/atomic.Value or atomic.Pointer: its arguments are stored.
	modelAtomicStore
	// modelAtomicLoad is Load of those types.
	modelAtomicLoad
	// modelAtomicSwap is Swap: it stores and loads.
	modelAtomicSwap
	// modelReflectValueOf is reflect.ValueOf: its argument goes into
	// reflection.
	modelReflectValueOf
	// modelReflectInterface is reflect.Value.Interface: its result comes
	// out of reflection.
	modelReflectInterface
)

func (a *analysis) model(fn *ssa.Function) model {
	if fn == nil {
		return modelNone
	}
	if m, ok := a.models[fn]; ok {
		return m
	}
	m := modelOf(fn)
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
		if recv == "Value" && o.Name() == "Interface" {
			return modelReflectInterface
		}
		if recv == "" && o.Name() == "ValueOf" {
			return modelReflectValueOf
		}
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
			a.addReader(loc{kind: locPanic}, key{call, 0})
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
		case modelReflectInterface:
			a.addReader(loc{kind: locReflect}, key{call, 0})
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
	switch name {
	case "fmt.Errorf":
		if len(args) != 2 {
			return
		}
		if format, ok := constFormat(args[0]); ok && len(fmtverb.Wrapped(fmtverb.Parse(format, -1))) == 0 {
			// It wraps nothing.
			return
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
			a.varargs[alloc] = call
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
				a.asTarget(common.Args[1], from, site)
			}
		case modelReflectValueOf:
			if len(common.Args) == 1 && common.Args[0] == v {
				a.markLoc(loc{kind: locReflect}, reason{kind: kStore, from: from, site: site})
			}
		case modelAtomicStore, modelAtomicSwap:
			for _, arg := range common.Args[1:] {
				if arg == v {
					a.markLoc(a.atomicLoc(v.Type()), reason{kind: kStore, from: from, site: site})
				}
			}
		}
	}
	// A call of a method named Unwrap on a marked receiver gives a
	// marked result. (A slice it returns is not a value that is marked:
	// its elements are memory, marked where they were stored.)
	if call != nil && unwrapReceiver(common) == v {
		res := common.Signature().Results()
		for r := range res.Len() {
			a.markResult(call, r, delta, reason{kind: kUnwrap, from: from, site: site})
		}
	}
	a.linkArgs(k, delta, site)
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
func (a *analysis) asTarget(target ssa.Value, from ref, site ssa.Instruction) {
	why := reason{kind: kAs, from: from, site: site}
	if mi, ok := target.(*ssa.MakeInterface); ok {
		pe := pointerElem(mi.X.Type())
		if pe == nil || !a.markable(pe) || !a.assertable(pe) {
			return
		}
		locs, n := a.addrLocs(mi.X)
		for _, l := range locs[:n] {
			a.markLoc(l, why)
		}
		return
	}
	if a.asUnknown {
		return
	}
	a.asUnknown = true
	for _, t := range a.asPointees {
		a.markLoc(a.typedLoc(t), why)
	}
}
