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

// What an Is or As method answers about the Subject.
//
// errors.Is(err, X) asks the Is method of every error in the tree of err
// with X as its target, and errors.As(err, &T) the As methods with a
// pointer to a variable of the Subject's type. A type whose method may
// then answer true is a wrapper of the Subject. targetMatches decides it
// by running the method with "the target is the Subject" as the only
// thing known:
//
//   - it follows the blocks that can be entered then. A branch is not
//     taken only where its condition is known not to hold for the
//     Subject: the true edge of target == V (the false edge of target !=
//     V) for a value V that carries no Mark although its type can carry
//     one (an interface, a type that can be an error), so that V is not
//     the Subject; the true edge of errors.Is(V, target) or errors.As(V,
//     target) for such a V; the ok edge of an assertion of the target to
//     a type the Subject's value cannot have, and the not-ok edge of an
//     assertion to a type it is known to have; and the same through !,
//     && and || and through a static call that is passed the target
//     (followed maxTargetDepth deep). The false edge of target == V
//     excludes nothing: the target may be the Subject;
//   - the method matches if it can then return anything but a value that
//     is false for the Subject: the constant false, or one of the
//     conditions above where it cannot hold.
//
// Whatever the pass does not recognize is taken to be possible: a value
// it cannot place may be true, a call it cannot follow may return true,
// and a comparison of something that only may be the target (a phi, the
// value of a comma-ok assertion) or that is a conversion of it to
// another concrete type (int(target.(Code)): the int it is equal to
// carries no Mark, and no value does for being equal to a conversion of
// the Subject) excludes nothing. "No Mark" is a proof of difference only
// for a value that is compared with the target itself and whose type
// Marks exist for. So a type is no
// wrapper only where every way to a true answer is closed by a look at
// the target that the Subject fails.

// maxTargetDepth bounds how many static calls the target of an Is or As
// method is followed through.
const maxTargetDepth = 4

// maxTargetSteps bounds the conditions one targetMatches looks at (in
// the method and the helpers it follows); beyond it the method matches.
const maxTargetSteps = 20000

// targetMatch is a method that targetMatches found to match, and the
// marked value a comparison of it went through (if any), for traces.
type targetMatch struct {
	by ref
}

// targetMatches reports whether the Is method (for a Sentinel Subject) or
// As method (for a Concrete Type Subject) fn may answer true when its
// target is the Subject. It grows with the Marks: a method that matches
// stays matching.
func (a *analysis) targetMatches(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	if m, ok := a.targets[fn]; ok {
		a.heldBy = m.by
		return true
	}
	if len(fn.Params) != 2 {
		// Not the method errors.Is and errors.As call; not known.
		a.targets[fn] = &targetMatch{}
		a.heldBy = ref{}
		return true
	}
	if a.settled && a.noTargets[fn] {
		return false
	}
	w := &targetWalk{a: a, busy: make(map[targetKey]bool)}
	if !w.answers(fn, fn.Params[1], true, 0) {
		if a.settled {
			// The Marks are final: so is the answer.
			a.noTargets[fn] = true
		}
		return false
	}
	a.targets[fn] = &targetMatch{by: w.by}
	a.heldBy = w.by
	return true
}

// targetKey names one question of a targetWalk: can the function, with
// the target in its parameter p, return want.
type targetKey struct {
	fn   *ssa.Function
	p    ssa.Value
	want bool
}

// targetWalk is one run of targetMatches.
type targetWalk struct {
	a     *analysis
	busy  map[targetKey]bool
	steps int
	// by is the first marked value that a comparison with the target
	// was possible through.
	by ref
}

// answers reports whether fn can return want (or something that is not a
// constant) when its parameter p is the Subject, the target of an Is or
// As method.
func (w *targetWalk) answers(fn *ssa.Function, p ssa.Value, want bool, depth int) bool {
	if depth > maxTargetDepth || !w.a.known(fn) || len(fn.Blocks) == 0 {
		return true
	}
	k := targetKey{fn, p, want}
	if w.busy[k] {
		// A recursion adds no way to an answer of its own.
		return false
	}
	w.busy[k] = true
	defer delete(w.busy, k)
	f := &targetFrame{w: w, fn: fn, depth: depth, alias: make(map[ssa.Value]bool), reach: make(map[*ssa.BasicBlock]bool), phis: make(map[phiKey]bool)}
	f.aliases(p)
	// The blocks that can be entered: each is looked at once. An edge
	// that cannot be taken is looked at again only if its condition
	// asked about a block that was not reached yet (through a phi).
	var queue []*ssa.BasicBlock
	enter := func(b *ssa.BasicBlock) {
		if !f.reach[b] {
			f.reach[b] = true
			queue = append(queue, b)
		}
	}
	type edge struct {
		b *ssa.BasicBlock
		i int
	}
	var waiting []edge
	enter(fn.Blocks[0])
	if fn.Recover != nil {
		// Where a recovered panic goes on: the named results.
		enter(fn.Recover)
	}
	for len(queue) > 0 {
		for len(queue) > 0 {
			b := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			for i, succ := range b.Succs {
				if f.reach[succ] {
					continue
				}
				f.unreached = false
				if f.feasible(b, i) {
					enter(succ)
				} else if f.unreached {
					waiting = append(waiting, edge{b, i})
				}
			}
			if w.steps > maxTargetSteps {
				// Too much to look at: not known.
				return true
			}
		}
		rest := waiting[:0]
		for _, e := range waiting {
			succ := e.b.Succs[e.i]
			if f.reach[succ] {
				continue
			}
			f.unreached = false
			if f.feasible(e.b, e.i) {
				enter(succ)
			} else if f.unreached {
				rest = append(rest, e)
			}
		}
		waiting = rest
	}
	for _, b := range fn.Blocks {
		if !f.reach[b] || len(b.Instrs) == 0 {
			continue
		}
		ret, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return)
		if !ok {
			continue
		}
		if len(ret.Results) != 1 || f.canBe(ret.Results[0], want) {
			return true
		}
	}
	return w.steps > maxTargetSteps
}

type phiKey struct {
	phi  *ssa.Phi
	want bool
}

// targetFrame is the walk of one function.
type targetFrame struct {
	w     *targetWalk
	fn    *ssa.Function
	depth int
	// alias are the values that are the target: the parameter and its
	// conversions.
	alias map[ssa.Value]bool
	// reach are the blocks that can be entered when the target is the
	// Subject.
	reach map[*ssa.BasicBlock]bool
	// phis are the phis being looked at.
	phis map[phiKey]bool
	// unreached is set when canBe passes over an edge of a phi because
	// its block is not reached (it may be later).
	unreached bool
}

// aliases collects the values that are the target p whenever they are
// computed: its conversions between interface types, its assertions that
// panic if they fail, and such a value converted back to an interface. A
// phi and the value of a comma-ok assertion (which is a zero value where
// the assertion failed) only may be the target: a comparison of one says
// nothing about it. Nor is a conversion of the asserted value to another
// concrete type the target (int(t.(Code)), (*plain)(t.(*T))): it is a
// value of a type the Subject does not have, and what it is equal to
// carries no Mark for being so.
func (f *targetFrame) aliases(p ssa.Value) {
	work := []ssa.Value{p}
	f.alias[p] = true
	for len(work) > 0 {
		v := work[len(work)-1]
		work = work[:len(work)-1]
		refs := v.Referrers()
		if refs == nil {
			continue
		}
		for _, r := range *refs {
			var x ssa.Value
			switch r := r.(type) {
			case *ssa.ChangeInterface:
				x = r
			case *ssa.MakeInterface:
				x = r
			case *ssa.ChangeType:
				// Between interface types of one underlying type.
				if isInterface(r.X.Type()) && isInterface(r.Type()) {
					x = r
				}
			case *ssa.TypeAssert:
				if !r.CommaOk {
					x = r
				}
			}
			if x != nil && !f.alias[x] {
				f.alias[x] = true
				work = append(work, x)
			}
		}
	}
}

// feasible reports whether the edge from the reachable block b to its
// successor i can be taken when the target is the Subject.
func (f *targetFrame) feasible(b *ssa.BasicBlock, i int) bool {
	if len(b.Instrs) == 0 {
		return true
	}
	br, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If)
	if !ok || len(b.Succs) != 2 {
		return true
	}
	return f.canBe(br.Cond, i == 0)
}

// canBe reports whether the boolean v can be want when the target is the
// Subject. Anything it does not recognize can be.
func (f *targetFrame) canBe(v ssa.Value, want bool) bool {
	w := f.w
	a := w.a
	if w.steps++; w.steps > maxTargetSteps {
		return true
	}
	switch x := v.(type) {
	case *ssa.Const:
		if x.Value != nil && x.Value.Kind() == constant.Bool {
			return constant.BoolVal(x.Value) == want
		}
	case *ssa.UnOp:
		if x.Op == token.NOT {
			return f.canBe(x.X, !want)
		}
	case *ssa.Phi:
		// t == A && cond, t == A || cond: one of the edges that can be
		// taken brings a value that can be want.
		k := phiKey{x, want}
		if f.phis[k] {
			return false
		}
		f.phis[k] = true
		defer delete(f.phis, k)
		blk := x.Block()
		for i, e := range x.Edges {
			pred := blk.Preds[i]
			if !f.reach[pred] {
				f.unreached = true
				continue
			}
			taken := false
			for j, succ := range pred.Succs {
				if succ == blk && f.feasible(pred, j) {
					taken = true
				}
			}
			if taken && f.canBe(e, want) {
				return true
			}
		}
		return false
	case *ssa.BinOp:
		if x.Op != token.EQL && x.Op != token.NEQ {
			break
		}
		var other ssa.Value
		switch {
		case f.alias[x.X] && f.alias[x.Y]:
			// t == t.
			return want == (x.Op == token.EQL)
		case f.alias[x.X]:
			other = x.Y
		case f.alias[x.Y]:
			other = x.X
		default:
			return true
		}
		if want == (x.Op == token.EQL) {
			// The target is equal to other: other is the Subject.
			return f.mayBeSubject(other)
		}
		// It is not: that is so for most Subjects.
		return true
	case *ssa.Extract:
		ta, ok := x.Tuple.(*ssa.TypeAssert)
		if !ok || x.Index != 1 || !f.alias[ta.X] {
			break
		}
		may, known := a.subjectHasType(ta.AssertedType)
		if want {
			return may
		}
		return !known
	case *ssa.Call:
		common := x.Common()
		sc := common.StaticCallee()
		if sc == nil || common.IsInvoke() {
			break
		}
		if m := a.model(sc); (m == modelErrorsIs && a.subj.Sentinel != nil) || (m == modelErrorsAs && a.subj.Type != nil) {
			if len(common.Args) == 2 && f.alias[common.Args[1]] && !f.alias[common.Args[0]] {
				// errors.Is(v, target) is true only of a v that holds
				// the target.
				if want {
					return f.marked(common.Args[0])
				}
				return true
			}
			break
		}
		// A helper that is passed the target: what it can return.
		at := -1
		for j, arg := range common.Args {
			if !f.alias[arg] {
				continue
			}
			if at >= 0 {
				return true
			}
			at = j
		}
		if at < 0 || len(common.Args) != len(sc.Params) || sc.Signature.Results().Len() != 1 {
			break
		}
		if _, intrinsic := a.intrinsic(sc); intrinsic {
			break
		}
		return w.answers(sc, sc.Params[at], want, f.depth+1)
	}
	return true
}

// marked reports whether v, a value that is equal to the target or is
// asked whether it holds the target, may be or hold the Subject: it
// carries a Mark (which is noted for the trace). A value of a type that
// Marks do not exist for (an int, a string, a struct or pointer that is
// no error) carries none whatever it is equal to: it may.
func (f *targetFrame) marked(v ssa.Value) bool {
	a := f.w.a
	if !a.markable(v.Type()) {
		// (A tuple too.)
		return true
	}
	if a.tagsOf(v) == 0 {
		return false
	}
	if f.w.by.kind == refNone && a.explicit(key{v, 0}) {
		f.w.by = ref{kind: refValue, k: key{v, 0}}
	}
	return true
}

// mayBeSubject reports whether v, a value the target is compared with,
// may be the Subject when the two are equal.
func (f *targetFrame) mayBeSubject(v ssa.Value) bool {
	a := f.w.a
	if a.subj.Sentinel == nil {
		// The target of an As method is a pointer to a variable of the
		// Subject's type, not the Subject: no Mark says which.
		return true
	}
	if _, ok := v.(*ssa.Const); ok {
		// target == nil: the pass does not ask whether the Sentinel
		// may be nil.
		return true
	}
	return f.marked(v)
}

// subjectHasType reports whether the target of an Is or As method, when
// it is the Subject, may have the dynamic type t (or implement the
// interface t), and whether it is known to.
func (a *analysis) subjectHasType(t types.Type) (may, known bool) {
	if a.subj.Sentinel == nil {
		// The target of As is a pointer to a variable of the Subject's
		// type, or of an interface type the Subject implements.
		if isInterface(t) {
			return true, false
		}
		pe := pointerElem(t)
		return pe != nil && (types.Identical(pe, a.subj.Type) || isInterface(pe)), false
	}
	if a.dynUnknown || len(a.dynTypes) == 0 {
		return true, false
	}
	iface, _ := t.Underlying().(*types.Interface)
	if _, isParam := types.Unalias(t).(*types.TypeParam); isParam {
		return true, false
	}
	known = true
	for _, d := range a.dynTypes {
		var has bool
		if iface != nil {
			has = types.Implements(d, iface)
		} else {
			has = types.Identical(d, t)
		}
		may = may || has
		known = known && has
	}
	if iface == nil && a.typeSeed(t) {
		may = true
	}
	return may, known
}
