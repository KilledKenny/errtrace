package mark

import (
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/flow"
)

// callees returns the functions that may run at site.
func (a *analysis) callees(site ssa.CallInstruction) []*ssa.Function {
	if sc := site.Common().StaticCallee(); sc != nil {
		return []*ssa.Function{sc}
	}
	fns, ok := a.calleesCache[site]
	if !ok {
		fns = a.res.Callees(site)
		if !site.Common().IsInvoke() {
			// A call of a function value runs a function whose value
			// is taken somewhere (CHA assumes every function's is).
			var kept []*ssa.Function
			for _, g := range fns {
				if a.taken[g] {
					kept = append(kept, g)
				}
			}
			fns = kept
		}
		a.calleesCache[site] = fns
	}
	return fns
}

// callers returns the call sites in scanned functions that may call fn,
// in the order of the scan.
func (a *analysis) callers(fn *ssa.Function) []ssa.CallInstruction {
	sites, ok := a.callersCache[fn]
	if ok {
		return sites
	}
	for _, s := range a.res.Callers(fn) {
		if _, scanned := a.siteOrd[s]; !scanned {
			continue
		}
		if c := s.Common(); c.StaticCallee() == nil && !c.IsInvoke() && !a.taken[fn] {
			// Not a callee of a call of a function value (see callees).
			continue
		}
		sites = append(sites, s)
	}
	sort.Slice(sites, func(i, j int) bool { return a.siteOrd[sites[i]] < a.siteOrd[sites[j]] })
	a.callersCache[fn] = sites
	return sites
}

// known reports whether the pass scanned the body of the callee g.
func (a *analysis) known(g *ssa.Function) bool {
	if g.Blocks == nil {
		return false
	}
	_, ok := a.fnIndex[g]
	return ok
}

// opaqueCallee reports whether the callee g, which the pass did not scan,
// may return what it is passed: it has no body (assembly, linkname, cgo),
// or it is an intrinsic or a function of package reflect called
// dynamically. Any other callee the pass did
// not scan is a method that only reflection can reach (see collect), and
// is not a callee at all.
func (a *analysis) opaqueCallee(g *ssa.Function) bool {
	if g.Blocks == nil || a.reflectBody(g) {
		return true
	}
	_, ok := a.intrinsic(g)
	return ok
}

// indexCallees records what a dynamic call that may reach an intrinsic
// reads. The bodies of the intrinsics are not scanned, so their effect is
// modelled here too, without the operands: fmt.Errorf may wrap anything
// in memory of its operand type, errors.Join anything in memory of type
// error. (errors.New holds nothing, and errtrace.Wrap its argument, which
// linkArgs handles.)
func (a *analysis) indexCallees(site ssa.CallInstruction) {
	call, ok := site.(*ssa.Call)
	if !ok || site.Common().StaticCallee() != nil {
		return
	}
	res := call.Call.Signature().Results()
	if res.Len() == 0 || !a.markable(res.At(0).Type()) {
		return
	}
	for _, g := range a.callees(site) {
		name, ok := a.intrinsic(g)
		if !ok || (name != "fmt.Errorf" && name != "errors.Join") {
			continue
		}
		params := g.Signature.Params()
		if params.Len() == 0 {
			continue
		}
		if st, ok := params.At(params.Len() - 1).Type().Underlying().(*types.Slice); ok {
			a.addReader(a.typedLoc(st.Elem()), key{call, 0})
			a.operandsFrom(a.typedLoc(st.Elem()), st.Elem(), call, name)
		}
	}
}

// paramShift returns how many leading parameters of the callee g the call at
// site has no operand for: 0, except for a call of a bound method value
// whose wrapper VTA spliced out, where the callee's receiver comes from the
// closure (the wrapper's own call passes it) and operand j is parameter
// j+1.
func paramShift(site ssa.CallInstruction, g *ssa.Function) int {
	common := site.Common()
	n := len(common.Args)
	if common.IsInvoke() {
		n++
	}
	return len(g.Params) - n
}

// argAt returns the operand of the call at site that parameter p of the
// callee g receives, or nil.
func argAt(site ssa.CallInstruction, g *ssa.Function, p int) ssa.Value {
	common := site.Common()
	p -= paramShift(site, g)
	if p < 0 {
		return nil
	}
	if common.IsInvoke() {
		if p == 0 {
			return common.Value
		}
		p--
	}
	if p < 0 || p >= len(common.Args) {
		return nil
	}
	return common.Args[p]
}

// linkArgs passes the marked operand k of the call at site to the
// callees: it seeds their parameter with its Param tag, and marks the
// call's results that a callee returns from that parameter, with the tags
// the operand has in the caller.
func (a *analysis) linkArgs(k key, delta tags, site ssa.CallInstruction) {
	v := k.v
	common := site.Common()
	var ps [4]int
	params := ps[:0]
	shift := 0
	if common.IsInvoke() {
		shift = 1
		if common.Value == v {
			params = append(params, 0)
		}
	}
	for j, arg := range common.Args {
		if arg == v {
			params = append(params, j+shift)
		}
	}
	if len(params) == 0 {
		return
	}
	call, _ := site.(*ssa.Call)
	from := valueRef(k, delta)
	for _, g := range a.callees(site) {
		if !a.known(g) {
			if name, ok := a.intrinsic(g); ok {
				// An intrinsic called dynamically calls the methods of
				// its operand as one called statically does.
				a.operandMethods(v.Type(), delta, from, site, name, opAll)
			}
			// A callee without a body (or an intrinsic called
			// dynamically) may return any of its arguments.
			if call != nil && a.opaqueCallee(g) {
				t := a.tagsOf(v)
				for r := range call.Call.Signature().Results().Len() {
					a.markResult(call, r, t, reason{kind: kThrough, from: valueRef(k, t), site: site, fn: g})
				}
			}
			continue
		}
		sh := paramShift(site, g)
		for _, p := range params {
			p += sh
			if p < 0 {
				continue
			}
			if p < len(g.Params) {
				a.mark(key{g.Params[p], 0}, paramSeed(p, delta), reason{kind: kParam, from: from, site: site, fn: g})
				if pt := g.Params[p].Type(); !isInterface(pt) && isInterface(v.Type()) {
					// A held interface value is the receiver of a
					// method of a composite type (which may be an
					// error type itself): as a type assertion to that
					// type.
					why := reason{kind: kAssert, from: from, site: site, typ: pt}
					if delta&planeH != 0 {
						a.markHeld(pt, why)
					} else if delta&boxed != 0 {
						a.markBoxed(pt, why)
					}
				}
			}
			if call == nil || p >= maxParamTag {
				continue
			}
			for r := range a.rets[g] {
				if t := throughParam(a.rets[g][r].t&paramBits(p), a.tagsOf(v)); t != 0 {
					a.markResult(call, r, t, reason{kind: kThrough, from: valueRef(k, t), site: site, fn: g})
				}
			}
		}
	}
}

// retChanged pushes the new tags delta of result r of g to the calls of
// g: a free result marks every call, a Param(i) result the calls whose
// operand i is marked, with that operand's tags.
func (a *analysis) retChanged(g *ssa.Function, r int, delta tags) {
	a.reflectReturned(g, r, planesOf(delta&anyFree))
	for _, site := range a.callers(g) {
		call, ok := site.(*ssa.Call)
		if !ok {
			continue
		}
		if sc := site.Common().StaticCallee(); sc != nil {
			if _, ok := a.intrinsic(sc); ok {
				// Modelled at the call, not followed.
				continue
			}
		}
		if fr := delta & anyFree; fr != 0 {
			if a.unwrapMethod(g) {
				// An Unwrap method that can return the Subject makes
				// its receiver type a wrapper, and every wrapper is
				// marked: the result is marked where the receiver is.
				if recv := argAt(site, g, 0); recv != nil {
					t := a.tagsOf(recv)
					if !isInterface(recv.Type()) {
						t = freeT
					}
					if t != 0 {
						a.markResult(call, r, t, reason{kind: kUnwrap, from: valueRef(key{recv, 0}, t), site: site})
					}
				}
			} else {
				a.markResult(call, r, fr, reason{kind: kReturn, from: ref{kind: refRet, fn: g, idx: int32(r), free: true}, site: site, fn: g})
			}
		}
		for p := 0; p < maxParamTag && p < len(g.Params); p++ {
			bits := delta & paramBits(p)
			if bits == 0 {
				continue
			}
			arg := argAt(site, g, p)
			if arg == nil {
				continue
			}
			if t := throughParam(bits, a.tagsOf(arg)); t != 0 {
				a.markResult(call, r, t, reason{kind: kThrough, from: valueRef(key{arg, 0}, t), site: site, fn: g})
			}
		}
	}
}

// siteResultFromCallees reports whether result index of the call at site,
// which has no value of its own (go, defer), may hold the Subject, from
// the results of its callees.
func (a *analysis) siteResultFromCallees(site ssa.CallInstruction, index int) bool {
	if sc := site.Common().StaticCallee(); sc != nil {
		if _, ok := a.intrinsic(sc); ok {
			// The pass does not model an intrinsic whose result is
			// dropped.
			return true
		}
	}
	for _, g := range a.callees(site) {
		if !a.known(g) {
			// It may return any of its arguments (see linkArgs); what
			// a dynamically called intrinsic wraps is not known here.
			if _, ok := a.intrinsic(g); ok {
				return true
			}
			if !a.opaqueCallee(g) {
				continue
			}
			common := site.Common()
			if common.IsInvoke() && a.tagsOf(common.Value) != 0 {
				return true
			}
			for _, arg := range common.Args {
				if a.tagsOf(arg) != 0 {
					return true
				}
			}
			continue
		}
		rs := a.rets[g]
		if index >= len(rs) {
			continue
		}
		t := rs[index].t
		if t&anyFree != 0 {
			return true
		}
		for p := 0; p < maxParamTag && p < len(g.Params); p++ {
			if t&paramBits(p) == 0 {
				continue
			}
			if arg := argAt(site, g, p); arg != nil && a.tagsOf(arg) != 0 {
				return true
			}
		}
	}
	return false
}

// sliceRoot returns the parameter of slice type that v is, or is a
// reslicing of (args[1:] shares the elements of args).
func sliceRoot(v ssa.Value) (*ssa.Parameter, bool) {
	for range 8 {
		switch x := v.(type) {
		case *ssa.Parameter:
			_, ok := x.Type().Underlying().(*types.Slice)
			return x, ok && x.Parent() != nil
		case *ssa.Slice:
			if _, ok := x.X.Type().Underlying().(*types.Slice); !ok {
				return nil, false
			}
			v = x.X
		default:
			return nil, false
		}
	}
	return nil, false
}

// sliceParam returns the locVarargs of v, if v is (a reslicing of) a
// parameter of slice type: the elements of the slices passed to it that
// the pass can tell apart from other memory.
func sliceParam(v ssa.Value) (loc, bool) {
	p, ok := sliceRoot(v)
	if !ok {
		return loc{}, false
	}
	return loc{kind: locVarargs, v: p}, true
}

// soleCallee returns the one function that can run at site: its static
// callee, or the only callee of a call of a function value.
func (a *analysis) soleCallee(site ssa.CallInstruction) *ssa.Function {
	common := site.Common()
	if sc := common.StaticCallee(); sc != nil {
		return sc
	}
	if common.IsInvoke() {
		return nil
	}
	if _, ok := common.Value.(*ssa.Builtin); ok {
		return nil
	}
	if cs := a.callees(site); len(cs) == 1 {
		return cs[0]
	}
	return nil
}

// indexStatic records the calls that can only run one function, by that
// function, and how slices reach their parameters (see indexSlices),
// before the loads are indexed.
func (a *analysis) indexStatic() {
	if rp := a.prog.ImportedPackage("reflect"); rp != nil {
		if t := rp.Type("Value"); t != nil {
			a.reflectValues = a.typedLoc(t.Type())
		}
	}
	for _, fn := range a.fns {
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				site, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				if sc := site.Common().StaticCallee(); sc != nil && reflectCall(sc) {
					a.reflects = true
					if root := a.reflectRoot(site.Common().Args[0]); root != nil {
						a.reflectRoots = append(a.reflectRoots, root)
					} else if _, isCall := site.Common().Args[0].(*ssa.Call); !isCall || a.reflectRoot(site.Common().Args[0]) == nil {
						a.reflectAny = a.reflectAny || !a.rootedInReflectCall(site.Common().Args[0])
					}
					if n := len(a.reflectCallers); n == 0 || a.reflectCallers[n-1] != fn {
						a.reflectCallers = append(a.reflectCallers, fn)
					}
				}
				g := a.soleCallee(site)
				if g == nil {
					continue
				}
				a.staticSites[g] = append(a.staticSites[g], site)
				if call, ok := site.(*ssa.Call); ok {
					if _, intrinsic := a.intrinsic(g); !intrinsic {
						a.indexSlices(call, g)
					}
				}
			}
		}
	}
}

// closed reports whether the elements of the slices passed to the slice
// parameter of l are in no other memory than l. That is so if l and every
// slice parameter whose slice may be passed on to l, directly or through
// others, are sealed (see sealed): nothing lets the slice out of the
// functions that receive it, and nothing passes in a slice that is other
// memory. Loads from such a parameter need not look at the type-keyed
// memory of the element type. The notion exists for precision only: when
// in doubt, a parameter is not closed.
func (a *analysis) closed(l loc) bool {
	switch a.closedMemo[l] {
	case 1:
		return true
	case 2:
		return false
	}
	// The parameters whose slices reach l, l among them. (A cycle of
	// passing on is closed if all of it is sealed.)
	seen := map[loc]bool{l: true}
	work := []loc{l}
	for len(work) > 0 {
		cur := work[len(work)-1]
		work = work[:len(work)-1]
		if a.closedMemo[cur] == 1 {
			// Decided, with all that reaches it.
			continue
		}
		from, ok := a.sealed(cur)
		if a.closedMemo[cur] == 2 || !ok {
			a.closedMemo[l] = 2
			return false
		}
		for _, f := range from {
			if !seen[f] {
				seen[f] = true
				work = append(work, f)
			}
		}
	}
	// What reaches one of them reaches l.
	for s := range seen {
		a.closedMemo[s] = 1
	}
	return true
}

// sealed reports whether the slice parameter of l keeps the slices passed
// to it to itself, as far as its own function and the calls of that
// function tell: neither the function nor a function the slice is passed
// on to, however far, lets it escape (see passedOnEscapes); every call
// that may run the function can run nothing else, and no call through
// reflection may run it (see reflectTarget); and every such call passes a
// fresh argument array, nil, or a slice parameter of the caller. (A
// function whose value reaches a call that VTA does not see it at, by a
// store through reflection, is sealed in the first pass and not in the
// flooding pass, whose call edges list every call that matches it.)
// Those parameters are returned: l is closed only if they are (see
// closed).
func (a *analysis) sealed(l loc) (from []loc, ok bool) {
	p := l.v.(*ssa.Parameter)
	g := p.Parent()
	if a.open != nil || a.passedOnEscapes(l) {
		return nil, false
	}
	if a.reflectTarget(g) {
		// A call through reflection (reflect.ValueOf(g).Call, an
		// exported method through Value.Method) is none of the calls
		// looked at below, and may pass a slice that is other memory.
		return nil, false
	}
	idx := -1
	for i, q := range g.Params {
		if q == p {
			idx = i
		}
	}
	if idx < 0 {
		return nil, false
	}
	// (Not a.callers: the scan that orders the call sites is running.)
	for _, site := range a.res.Callers(g) {
		if _, scanned := a.fnIndex[site.Parent()]; !scanned {
			continue
		}
		if c := site.Common(); c.StaticCallee() == nil && !c.IsInvoke() && !a.taken[g] {
			continue
		}
		if a.soleCallee(site) != g {
			return nil, false
		}
	}
	if len(a.staticSites[g]) == 0 {
		// Nothing the pass sees calls it: do not conclude from that.
		return nil, false
	}
	for _, site := range a.staticSites[g] {
		args := site.Common().Args
		if _, isCall := site.(*ssa.Call); !isCall || len(args) != len(g.Params) {
			return nil, false
		}
		arg := args[idx]
		if c, ok := arg.(*ssa.Const); ok && c.IsNil() {
			continue
		}
		if sl, ok := arg.(*ssa.Slice); ok {
			if alloc, ok := sl.X.(*ssa.Alloc); ok && a.varargsParam[alloc] == l {
				continue
			}
		}
		if f, ok := sliceParam(arg); ok {
			from = append(from, f)
			continue
		}
		return nil, false
	}
	return from, true
}

// passedOnEscapes reports whether the slice passed to the slice parameter
// of l escapes: its function lets it (see vaEscapes), or a function it is
// passed on to as a slice parameter does (see indexSlices), however far.
// What such a function does with the slice (copy into it, append to it
// and keep the result, store or return it) writes or reads the type-keyed
// memory of the element type, and the elements of l are that memory too.
func (a *analysis) passedOnEscapes(l loc) bool {
	if esc, ok := a.passedEsc[l]; ok {
		return esc
	}
	seen := map[loc]bool{l: true}
	work := []loc{l}
	for len(work) > 0 {
		cur := work[len(work)-1]
		work = work[:len(work)-1]
		if a.passedEsc[cur] || a.vaEscapes(cur) {
			a.passedEsc[l] = true
			return true
		}
		for _, to := range a.forwards[cur] {
			if !seen[to] {
				seen[to] = true
				work = append(work, to)
			}
		}
	}
	// What one of them is passed on to, l is passed on to.
	for s := range seen {
		a.passedEsc[s] = false
	}
	return false
}

// indexSlices records, for a call that can only run the function g, which
// the pass scanned, how slices reach the callee's parameters: a fresh argument
// array of a variadic call that only the call uses is the memory of the
// callee's variadic parameter (locVarargs), and a slice parameter of the
// caller passed on as an argument (f(args...), p.doPrintf(format, a))
// forwards its memory to the callee's parameter.
func (a *analysis) indexSlices(call *ssa.Call, g *ssa.Function) {
	args := call.Call.Args
	if g == nil || !a.known(g) || len(args) == 0 || len(args) != len(g.Params) {
		return
	}
	for j, arg := range args {
		from, ok := sliceParam(arg)
		if !ok {
			continue
		}
		if to, ok := sliceParam(g.Params[j]); ok {
			a.forwards[from] = append(a.forwards[from], to)
			a.backwards[to] = append(a.backwards[to], from)
		}
	}
	if !g.Signature.Variadic() {
		return
	}
	to, ok := sliceParam(g.Params[len(g.Params)-1])
	if !ok {
		return
	}
	sl, ok := args[len(args)-1].(*ssa.Slice)
	if !ok {
		return
	}
	if _, ok := flow.VarargsOperands(sl); !ok {
		return
	}
	if alloc, ok := sl.X.(*ssa.Alloc); ok && onlyUsedBy(sl, call) {
		a.varargsParam[alloc] = to
	}
}

// vaEscapes reports whether the function of the slice parameter of l uses
// the slice other than to read its elements, take its length, or pass it
// on as an argument of a static call the pass follows or as the operands
// of a modelled intrinsic. If it does, its elements are also memory of
// their type.
func (a *analysis) vaEscapes(l loc) bool {
	if esc, ok := a.vaEsc[l]; ok {
		return esc
	}
	esc := a.vaEscapesNow(l.v.(*ssa.Parameter))
	a.vaEsc[l] = esc
	return esc
}

func (a *analysis) vaEscapesNow(p *ssa.Parameter) bool {
	return a.sliceEscapes(p, 0)
}

// sliceEscapes is vaEscapesNow for v, the slice parameter or a reslicing
// of it.
func (a *analysis) sliceEscapes(v ssa.Value, depth int) bool {
	refs := v.Referrers()
	if refs == nil || depth > 8 {
		return true
	}
	for _, r := range *refs {
		switch r := r.(type) {
		case *ssa.DebugRef:
		case *ssa.Slice:
			if _, ok := r.Type().Underlying().(*types.Slice); r.X != v || !ok || a.sliceEscapes(r, depth+1) {
				return true
			}
		case *ssa.IndexAddr:
			if r.X != v || r.Referrers() == nil {
				return true
			}
			for _, rr := range *r.Referrers() {
				switch rr := rr.(type) {
				case *ssa.DebugRef:
				case *ssa.UnOp:
					if rr.Op != token.MUL {
						return true
					}
				case *ssa.Store:
					// A store into an element (args[i] = x) stays in
					// the slice: see store.
					if rr.Val == ssa.Value(r) {
						return true
					}
				default:
					return true
				}
			}
		case *ssa.Call:
			args := r.Call.Args
			if b, ok := r.Call.Value.(*ssa.Builtin); ok {
				if b.Name() != "len" && b.Name() != "cap" {
					return true
				}
				continue
			}
			g := r.Call.StaticCallee()
			if g == nil {
				return true
			}
			if name, ok := a.intrinsic(g); ok {
				// Only as the operands, which the model reads.
				operands := -1
				switch name {
				case "fmt.Errorf":
					operands = 1
				case "errors.Join":
					operands = 0
				}
				for j, arg := range args {
					if arg == v && j != operands {
						return true
					}
				}
				continue
			}
			if !a.known(g) || len(args) != len(g.Params) {
				return true
			}
			for j, arg := range args {
				if arg != v {
					continue
				}
				if _, ok := sliceParam(g.Params[j]); !ok {
					return true
				}
			}
		default:
			return true
		}
	}
	return false
}

// unwrapMethod reports whether g is a method named Unwrap of a type that
// can be an error.
func (a *analysis) unwrapMethod(g *ssa.Function) bool {
	recv := g.Signature.Recv()
	return recv != nil && g.Name() == "Unwrap" && flow.IsErrorType(recv.Type())
}

// panicLoc is the value of the panics raised in fn.
func (a *analysis) panicLoc(fn *ssa.Function) loc {
	return loc{kind: locPanic, id: a.fnIndex[fn]}
}

// wakeRecovers marks the calls of recover that can see a panic raised in
// the function of the panic location l: a recover in F sees the panics of
// the functions that the functions deferring F call, directly or not (not
// through a go statement). A function that calls fmt.Errorf on operands
// counts as calling the methods that package fmt may run there.
func (a *analysis) wakeRecovers(l loc, planes tags) {
	if len(a.recovers) == 0 || int(l.id) >= len(a.fns) {
		return
	}
	// The functions that reach the panic.
	reach := map[*ssa.Function]bool{a.fns[l.id]: true}
	work := []*ssa.Function{a.fns[l.id]}
	for len(work) > 0 {
		g := work[len(work)-1]
		work = work[:len(work)-1]
		if a.reflectTarget(g) {
			// It may be called through reflection: the panic surfaces
			// where the program does that.
			for _, c := range a.reflectCallers {
				if !reach[c] {
					reach[c] = true
					work = append(work, c)
				}
			}
		}
		if formatMethod(g) {
			// A call of fmt.Errorf may run it, with no call edge
			// (see formatMethod).
			for _, c := range a.formatters {
				if !reach[c] {
					reach[c] = true
					work = append(work, c)
				}
			}
		}
		for _, site := range a.callers(g) {
			if _, isGo := site.(*ssa.Go); isGo {
				continue
			}
			if p := site.Parent(); !reach[p] {
				reach[p] = true
				work = append(work, p)
			}
		}
	}
	for _, rc := range a.recovers {
		sees := false
		for _, site := range a.callers(rc.fn) {
			if _, isDefer := site.(*ssa.Defer); isDefer && reach[site.Parent()] {
				sees = true
				break
			}
		}
		if sees {
			a.mark(rc.k, planes, reason{kind: kLoad, from: ref{kind: refLoc, l: l, free: true}, site: rc.k.v.(ssa.Instruction)})
		}
	}
}

// storeSliceParam marks the memory of the slice parameter of l, into an
// element of which its function stores a marked value, and of the slice
// parameters whose slices are passed on to it: they share the elements.
func (a *analysis) storeSliceParam(l loc, planes tags, why reason) {
	seen := map[loc]bool{l: true}
	work := []loc{l}
	for len(work) > 0 {
		cur := work[len(work)-1]
		work = work[:len(work)-1]
		a.markLoc(cur, planes, why)
		for _, from := range a.backwards[cur] {
			if !seen[from] {
				seen[from] = true
				work = append(work, from)
			}
		}
	}
}
