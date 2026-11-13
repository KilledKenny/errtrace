package mark

import (
	"go/types"
	"sort"

	"golang.org/x/tools/go/ssa"
)

// callees returns the functions that may run at site.
func (a *analysis) callees(site ssa.CallInstruction) []*ssa.Function {
	if sc := site.Common().StaticCallee(); sc != nil {
		return []*ssa.Function{sc}
	}
	fns, ok := a.calleesCache[site]
	if !ok {
		fns = a.res.Callees(site)
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
		if _, scanned := a.siteOrd[s]; scanned {
			sites = append(sites, s)
		}
	}
	sort.Slice(sites, func(i, j int) bool { return a.siteOrd[sites[i]] < a.siteOrd[sites[j]] })
	a.callersCache[fn] = sites
	return sites
}

// known reports whether the pass scanned the body of the callee g. The
// result of a callee it did not scan (no body: assembly, linkname, cgo; or
// an intrinsic called dynamically) is not known.
func (a *analysis) known(g *ssa.Function) bool {
	if g.Blocks == nil {
		return false
	}
	_, ok := a.fnIndex[g]
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
			// A callee without a body (or an intrinsic called
			// dynamically) may return any of its arguments.
			if call != nil {
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
				a.mark(key{g.Params[p], 0}, paramTag(p), reason{kind: kParam, from: from, site: site, fn: g})
			}
			pt := paramTag(p)
			if call == nil || pt == free {
				continue
			}
			for r := range a.rets[g] {
				if a.rets[g][r].t&pt != 0 {
					t := a.tagsOf(v)
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
		if delta&free != 0 {
			a.markResult(call, r, free, reason{kind: kReturn, from: ref{kind: refRet, fn: g, idx: int32(r), free: true}, site: site, fn: g})
			continue
		}
		for p := 0; p < maxParamTag && p < len(g.Params); p++ {
			if delta&paramTag(p) == 0 {
				continue
			}
			arg := argAt(site, g, p)
			if arg == nil {
				continue
			}
			if t := a.tagsOf(arg); t != 0 {
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
		if t&free != 0 {
			return true
		}
		for p := 0; p < maxParamTag && p < len(g.Params); p++ {
			if t&paramTag(p) == 0 {
				continue
			}
			if arg := argAt(site, g, p); arg != nil && a.tagsOf(arg) != 0 {
				return true
			}
		}
	}
	return false
}
