package mark

import (
	"go/constant"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// deadBlocks returns the blocks that are entered only when errors.Is(v, X)
// was false, for the Sentinel Subject X: in a block they dominate, v is
// neither X nor a Wrap of X, so its Mark does not reach the uses there.
// This is the one guard the upward pass applies (if errors.Is(err, X) {
// handle it }; return err), and only for a Sentinel whose variable the
// program never reassigns. A comparison (err != X) excludes nothing: err
// may still wrap X.
func (a *analysis) deadBlocks(v ssa.Value) []*ssa.BasicBlock {
	if a.subj.Sentinel == nil || !a.guardOK {
		return nil
	}
	if dead, ok := a.guards[v]; ok {
		return dead
	}
	var dead []*ssa.BasicBlock
	if refs := v.Referrers(); refs != nil {
		for _, r := range *refs {
			call, ok := r.(*ssa.Call)
			if !ok || a.model(call.Call.StaticCallee()) != modelErrorsIs {
				continue
			}
			args := call.Call.Args
			if len(args) != 2 || args[0] != v || args[1] == v {
				continue
			}
			if g := globalLoad(args[1]); g == nil || !a.inClass(g) {
				continue
			}
			dead = falseBlocks(call, false, dead, 0)
		}
	}
	a.guards[v] = dead
	return dead
}

func (a *analysis) inClass(g *ssa.Global) bool {
	for _, h := range a.class {
		if h == g {
			return true
		}
	}
	return false
}

// falseBlocks adds the blocks entered only when the boolean cond is false
// (true, if negated).
func falseBlocks(cond ssa.Value, negated bool, dead []*ssa.BasicBlock, depth int) []*ssa.BasicBlock {
	refs := cond.Referrers()
	if refs == nil || depth > 4 {
		return dead
	}
	for _, r := range *refs {
		switch x := r.(type) {
		case *ssa.If:
			if x.Cond != cond || len(x.Block().Succs) != 2 {
				continue
			}
			b, other := x.Block().Succs[1], x.Block().Succs[0]
			if negated {
				b, other = other, b
			}
			// The edge must be the only way into the block.
			if len(b.Preds) == 1 && b != other {
				dead = append(dead, b)
			}
		case *ssa.UnOp:
			if x.Op == token.NOT {
				dead = falseBlocks(x, !negated, dead, depth+1)
			}
		}
	}
	return dead
}

// guarded reports whether the use r of v lies where one of the dead
// blocks of v dominates.
func guarded(dead []*ssa.BasicBlock, v ssa.Value, r ssa.Instruction) bool {
	dominated := func(b *ssa.BasicBlock) bool {
		for _, d := range dead {
			if d.Dominates(b) {
				return true
			}
		}
		return false
	}
	if phi, ok := r.(*ssa.Phi); ok {
		// A phi uses v on the edges from its predecessors.
		used := false
		for i, e := range phi.Edges {
			if e != v {
				continue
			}
			used = true
			if !dominated(phi.Block().Preds[i]) {
				return false
			}
		}
		return used
	}
	return r.Block() != nil && dominated(r.Block())
}

// targetInfo is what an Is(target error) bool or As(target any) bool
// method does with its target.
type targetInfo struct {
	// opaque: the target goes where the pass cannot see what is done
	// with it; the method may report any error.
	opaque bool
	// vals are the values the target is compared with, or that the
	// method asks errors.Is or errors.As about together with the target.
	vals []ssa.Value
	// asserted are the concrete types the target is asserted to.
	asserted []types.Type
	// conds are the boolean values that depend on the target: its
	// comparisons, and the calls it is passed to.
	conds map[ssa.Value]bool
}

// maxTargetDepth bounds how many static calls the target of an Is or As
// method is followed through.
const maxTargetDepth = 4

func (a *analysis) targetInfo(fn *ssa.Function) *targetInfo {
	if ti, ok := a.targets[fn]; ok {
		return ti
	}
	ti := &targetInfo{conds: make(map[ssa.Value]bool)}
	a.targets[fn] = ti
	if len(fn.Params) != 2 {
		ti.opaque = true
		return ti
	}
	a.followTarget(fn, fn.Params[1], 0, ti, make(map[ssa.Value]bool))
	if !ti.opaque && !answersFromTarget(fn, ti) {
		// It can report true without having looked at its target (func
		// (E) Is(error) bool { return true }): it reports every error.
		ti.opaque = true
	}
	return ti
}

// answersFromTarget reports whether the method fn returns true only where
// that follows from a look at its target: every value it returns is false,
// one of ti.conds (or its negation), or is returned on a path that a
// branch on one of them leads to.
func answersFromTarget(fn *ssa.Function, ti *targetInfo) bool {
	cond := func(v ssa.Value) bool {
		if u, ok := v.(*ssa.UnOp); ok && u.Op == token.NOT {
			v = u.X
		}
		return ti.conds[v]
	}
	// The blocks entered only through a branch on the target.
	var branches []*ssa.BasicBlock
	for _, b := range fn.Blocks {
		if len(b.Instrs) == 0 {
			continue
		}
		if br, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If); ok && cond(br.Cond) {
			for _, succ := range b.Succs {
				if len(succ.Preds) == 1 {
					branches = append(branches, succ)
				}
			}
		}
	}
	after := func(b *ssa.BasicBlock) bool {
		for _, d := range branches {
			if d.Dominates(b) {
				return true
			}
		}
		return false
	}
	seen := make(map[ssa.Value]bool)
	var ok func(v ssa.Value, at *ssa.BasicBlock) bool
	ok = func(v ssa.Value, at *ssa.BasicBlock) bool {
		if cond(v) {
			return true
		}
		switch x := v.(type) {
		case *ssa.Const:
			if x.Value != nil && x.Value.Kind() == constant.Bool && !constant.BoolVal(x.Value) {
				return true
			}
		case *ssa.Phi:
			if seen[x] {
				return true
			}
			seen[x] = true
			for i, e := range x.Edges {
				if !ok(e, x.Block().Preds[i]) {
					return false
				}
			}
			return true
		}
		// Anything else (true, e == EACCES) only where a branch on the
		// target led.
		return after(at)
	}
	for _, b := range fn.Blocks {
		if len(b.Instrs) == 0 {
			continue
		}
		ret, isRet := b.Instrs[len(b.Instrs)-1].(*ssa.Return)
		if !isRet {
			continue
		}
		if len(ret.Results) != 1 || !ok(ret.Results[0], b) {
			return false
		}
	}
	return true
}

// followTarget collects into ti what fn does with its parameter p, the
// target of an Is or As method (or what a function it is passed to does).
func (a *analysis) followTarget(fn *ssa.Function, p ssa.Value, depth int, ti *targetInfo, seen map[ssa.Value]bool) {
	if depth > maxTargetDepth || !a.known(fn) {
		ti.opaque = true
		return
	}
	work := []ssa.Value{p}
	for len(work) > 0 && !ti.opaque {
		v := work[len(work)-1]
		work = work[:len(work)-1]
		if seen[v] {
			continue
		}
		seen[v] = true
		refs := v.Referrers()
		if refs == nil {
			ti.opaque = true
			return
		}
		if v == p {
			used := false
			for _, r := range *refs {
				if _, dbg := r.(*ssa.DebugRef); !dbg {
					used = true
				}
			}
			if !used {
				// It decides without its target.
				ti.opaque = true
				return
			}
		}
		for _, r := range *refs {
			switch x := r.(type) {
			case *ssa.DebugRef, *ssa.If, *ssa.Return, *ssa.FieldAddr, *ssa.IndexAddr:
			case *ssa.Phi:
				work = append(work, x)
			case *ssa.ChangeInterface:
				work = append(work, x)
			case *ssa.MakeInterface:
				work = append(work, x)
			case *ssa.ChangeType:
				work = append(work, x)
			case *ssa.Extract:
				if _, isAssert := x.Tuple.(*ssa.TypeAssert); isAssert && x.Index == 1 {
					// The ok of a type assertion of the target.
					ti.conds[x] = true
					continue
				}
				work = append(work, x)
			case *ssa.TypeAssert:
				if isInterface(x.AssertedType) {
					if a.subj.Sentinel == nil {
						// An As method that looks at the target through
						// an interface may set anything.
						ti.opaque = true
					}
				} else {
					ti.asserted = append(ti.asserted, x.AssertedType)
				}
				work = append(work, x)
			case *ssa.BinOp:
				if x.Op != token.EQL && x.Op != token.NEQ {
					continue
				}
				other := x.Y
				if x.Y == v {
					other = x.X
				}
				if _, isConst := other.(*ssa.Const); isConst || other == v {
					// t != nil, t == t: true of every Subject.
					ti.opaque = true
					continue
				}
				ti.vals = append(ti.vals, other)
				ti.conds[x] = true
			case *ssa.UnOp:
				// A load through the asserted pointer.
			case *ssa.Store:
				if x.Val == v {
					ti.opaque = true
				}
			case *ssa.Call:
				common := x.Common()
				if _, ok := common.Value.(*ssa.Builtin); ok {
					continue
				}
				sc := common.StaticCallee()
				switch a.model(sc) {
				case modelErrorsIs, modelErrorsAs:
					if len(common.Args) == 2 && common.Args[1] == v && common.Args[0] != v {
						ti.vals = append(ti.vals, common.Args[0])
						ti.conds[x] = true
						continue
					}
				}
				if sc == nil || common.IsInvoke() || len(common.Args) != len(sc.Params) {
					ti.opaque = true
					continue
				}
				for j, arg := range common.Args {
					if arg == v {
						a.followTarget(sc, sc.Params[j], depth+1, ti, seen)
					}
				}
				ti.conds[x] = true
			default:
				ti.opaque = true
			}
		}
	}
}

// targetMatches reports whether the Is method (for a Sentinel Subject) or
// As method (for a Concrete Type Subject) fn may report the Subject: it
// compares its target with a marked value, asks errors.Is or errors.As
// about a marked value, asserts its target to the Subject's type, or
// does something with the target that the pass cannot see.
func (a *analysis) targetMatches(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	ti := a.targetInfo(fn)
	if ti.opaque {
		a.heldBy = ref{}
		return true
	}
	for _, v := range ti.vals {
		if _, isTuple := v.Type().(*types.Tuple); isTuple {
			continue
		}
		if a.tagsOf(v) != 0 {
			a.heldBy = ref{}
			if a.explicit(key{v, 0}) {
				a.heldBy = ref{kind: refValue, k: key{v, 0}}
			}
			return true
		}
	}
	for _, t := range ti.asserted {
		if a.subj.Sentinel != nil {
			// Is asserts its target to a type the Sentinel may have.
			if a.typeSeed(t) || a.dynUnknown {
				a.heldBy = ref{}
				return true
			}
			for _, d := range a.dynTypes {
				if types.Identical(t, d) {
					a.heldBy = ref{}
					return true
				}
			}
			continue
		}
		// As asserts its target to a pointer to the Subject's type (or
		// to an interface type, which the Subject may implement).
		if pe := pointerElem(t); pe != nil && (types.Identical(pe, a.subj.Type) || isInterface(pe)) {
			a.heldBy = ref{}
			return true
		}
	}
	return false
}
