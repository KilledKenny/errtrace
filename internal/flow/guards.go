package flow

import (
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
)

// This file finds the guards that hold on a return path and decides what
// they do to an abstract error value. SSA has no refined copy of err after
// a check, so a guard is found per path: an If whose block dominates the
// place a value is used, together with the successor that dominates it,
// says which way the check went there. A Phi edge also gets the check of
// its predecessor's If when that If branches straight to the Phi's block.

// condGuard is one guard an If's condition makes on an error value, as
// the If's true branch has it.
type condGuard struct {
	// raw is the tested value and operand the same with ChangeInterface
	// and ChangeType conversions removed.
	raw, operand ssa.Value
	// g is the guard; its Holds is set per path.
	g errset.Guard
	// positive reports whether the If's true branch is the one on which
	// the check holds (false for err != io.EOF).
	positive bool
	// id numbers the guard within its function.
	id int
}

// guardUse is a guard that holds, one way or the other, at a point.
type guardUse struct {
	raw, operand ssa.Value
	g            errset.Guard
	// id names the guard and its outcome within the function.
	id int
}

// point is where a value is used: at the end of block b, or, with edgeTo
// set, on the edge from b to edgeTo (the value of a Phi edge).
type point struct {
	b, edgeTo *ssa.BasicBlock
}

// funcGuards are the guards of one function's Ifs, with the guards that
// hold at each point asked for so far.
type funcGuards struct {
	conds map[*ssa.BasicBlock][]condGuard
	at    map[point][]guardUse
}

// newFuncGuards recognises the guards of fn's Ifs.
func newFuncGuards(fn *ssa.Function) *funcGuards {
	gd := &funcGuards{conds: make(map[*ssa.BasicBlock][]condGuard), at: make(map[point][]guardUse)}
	n := 0
	for _, b := range fn.Blocks {
		if len(b.Instrs) == 0 {
			continue
		}
		ifInstr, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If)
		if !ok || len(b.Succs) != 2 {
			continue
		}
		for _, cg := range recognise(ifInstr.Cond) {
			cg.id = n
			n++
			gd.conds[b] = append(gd.conds[b], cg)
		}
	}
	return gd
}

// guardsAt returns the guards that hold at pt: those of the Ifs whose
// block strictly dominates pt's block through a successor that dominates
// it and is entered only from that If, and, for a Phi edge, that of the
// predecessor's If whose one branch is the edge.
func (gd *funcGuards) guardsAt(pt point) []guardUse {
	if pt.b == nil {
		return nil
	}
	if out, ok := gd.at[pt]; ok {
		return out
	}
	var out []guardUse
	add := func(b *ssa.BasicBlock, outcome bool) {
		for _, cg := range gd.conds[b] {
			g := cg.g
			g.Holds = outcome == cg.positive
			if g.Kind == errset.GuardNil && !g.Holds {
				// err != nil changes no abstract value.
				continue
			}
			id := 2 * cg.id
			if outcome {
				id++
			}
			out = append(out, guardUse{raw: cg.raw, operand: cg.operand, g: g, id: id})
		}
	}
	if e := pt.edgeTo; e != nil && len(gd.conds[pt.b]) > 0 {
		s0, s1 := pt.b.Succs[0], pt.b.Succs[1]
		switch {
		case s0 == s1:
		case s0 == e:
			add(pt.b, true)
		case s1 == e:
			add(pt.b, false)
		}
	}
	for b := pt.b.Idom(); b != nil; b = b.Idom() {
		if len(gd.conds[b]) == 0 {
			continue
		}
		s0, s1 := b.Succs[0], b.Succs[1]
		switch {
		case s0 == s1:
		case len(s0.Preds) == 1 && s0.Dominates(pt.b):
			add(b, true)
		case len(s1.Preds) == 1 && s1.Dominates(pt.b):
			add(b, false)
		}
	}
	gd.at[pt] = out
	return out
}

// stripConv removes ChangeInterface and ChangeType conversions from v.
func stripConv(v ssa.Value) ssa.Value {
	for {
		switch c := v.(type) {
		case *ssa.ChangeInterface:
			v = c.X
		case *ssa.ChangeType:
			v = c.X
		default:
			return v
		}
	}
}

// recognise returns the guards that the If condition cond makes:
// comparisons with a Sentinel or nil, errors.Is with a Sentinel, errors.As,
// comma-ok type assertions, and calls of other functions with an error
// argument (helper guards). Boolean negations are looked through.
func recognise(cond ssa.Value) []condGuard {
	positive := true
	for {
		u, ok := cond.(*ssa.UnOp)
		if !ok || u.Op != token.NOT {
			break
		}
		cond, positive = u.X, !positive
	}
	mk := func(raw ssa.Value, g errset.Guard, pos bool) condGuard {
		g.Site, g.Pos = cond, cond.Pos()
		return condGuard{raw: raw, operand: stripConv(raw), g: g, positive: pos}
	}
	switch c := cond.(type) {
	case *ssa.BinOp:
		if c.Op != token.EQL && c.Op != token.NEQ {
			return nil
		}
		pos := positive == (c.Op == token.EQL)
		x, y := c.X, c.Y
		if xv, _ := globalVar(x); isNilConst(x) || xv != nil {
			x, y = y, x
		}
		if !types.IsInterface(x.Type()) {
			return nil
		}
		if isNilConst(y) {
			return []condGuard{mk(x, errset.Guard{Kind: errset.GuardNil}, pos)}
		}
		if v, g := globalVar(y); v != nil {
			return []condGuard{mk(x, errset.Guard{Kind: errset.GuardEqual, Var: v, Type: SentinelType(g)}, pos)}
		}
	case *ssa.Extract:
		ta, ok := c.Tuple.(*ssa.TypeAssert)
		if ok && ta.CommaOk && c.Index == 1 && types.IsInterface(ta.X.Type()) {
			g := mk(ta.X, errset.Guard{Kind: errset.GuardAssert, Type: unalias(ta.AssertedType)}, positive)
			g.g.Site, g.g.Pos = ta, ta.Pos()
			return []condGuard{g}
		}
	case *ssa.Call:
		common := c.Common()
		callee := common.StaticCallee()
		if callee != nil && callee.Pkg != nil && callee.Pkg.Pkg.Path() == "errors" && callee.Signature.Recv() == nil && len(common.Args) == 2 {
			switch callee.Name() {
			case "Is":
				if v, g := globalVar(common.Args[1]); v != nil {
					return []condGuard{mk(common.Args[0], errset.Guard{Kind: errset.GuardIs, Var: v, Type: SentinelType(g)}, positive)}
				}
				return []condGuard{mk(common.Args[0], errset.Guard{Kind: errset.GuardHelper, Detail: "errors.Is with a target that is not a sentinel"}, positive)}
			case "As":
				if t := asTarget(common.Args[1]); t != nil {
					return []condGuard{mk(common.Args[0], errset.Guard{Kind: errset.GuardAs, Type: t}, positive)}
				}
				return []condGuard{mk(common.Args[0], errset.Guard{Kind: errset.GuardHelper, Detail: "errors.As with a target that is not modelled"}, positive)}
			}
		}
		var out []condGuard
		args := common.Args
		if common.IsInvoke() {
			args = append([]ssa.Value{common.Value}, args...)
		}
		for _, arg := range args {
			if IsErrorType(arg.Type()) && types.IsInterface(arg.Type()) {
				out = append(out, mk(arg, errset.Guard{Kind: errset.GuardHelper, Detail: "guarded by call to " + CalleeName(common)}, positive))
			}
		}
		return out
	}
	return nil
}

// globalOf returns the package-level variable v loads, or nil.
func globalOf(v ssa.Value) *ssa.Global {
	u, ok := stripConv(v).(*ssa.UnOp)
	if !ok || u.Op != token.MUL {
		return nil
	}
	g, _ := u.X.(*ssa.Global)
	return g
}

// globalVar returns the variable of a load of a package-level variable,
// and its Global, or nil.
func globalVar(v ssa.Value) (*types.Var, *ssa.Global) {
	g := globalOf(v)
	if g == nil {
		return nil, nil
	}
	obj, _ := g.Object().(*types.Var)
	if obj == nil {
		return nil, nil
	}
	return obj, g
}

// asTarget returns the type errors.As assigns to through target, the
// address of a variable converted to any (&pe), or nil.
func asTarget(target ssa.Value) types.Type {
	mi, ok := target.(*ssa.MakeInterface)
	if !ok {
		return nil
	}
	p, ok := mi.X.Type().Underlying().(*types.Pointer)
	if !ok {
		return nil
	}
	return unalias(p.Elem())
}

// matchGuards adds to the analyzer's guards those that hold at its point
// and test v.
func (a *analyzer) matchGuards(v ssa.Value) {
	if a.fn == nil || a.fn.Blocks == nil {
		return
	}
	if a.gd == nil {
		a.gd = newFuncGuards(a.fn)
	}
	for _, u := range a.gd.guardsAt(a.pt) {
		if u.raw != v && u.operand != v {
			continue
		}
		i, found := slices.BinarySearchFunc(a.guards, u.id, func(x guardUse, id int) int { return x.id - id })
		if !found {
			a.guards = slices.Insert(slices.Clip(a.guards), i, u)
		}
	}
}

// guardsKey names the analyzer's current guards, for its seen set.
func (a *analyzer) guardsKey() string {
	if len(a.guards) == 0 {
		return ""
	}
	var b strings.Builder
	for _, u := range a.guards {
		b.WriteString(strconv.Itoa(u.id))
		b.WriteByte(',')
	}
	return b.String()
}

// guarded returns id under the analyzer's current guards.
func (a *analyzer) guarded(id errset.ValueID) errset.ValueID {
	if len(a.guards) == 0 {
		return id
	}
	gs := make([]errset.Guard, len(a.guards))
	for i, u := range a.guards {
		gs[i] = u.g
	}
	v := a.tbl.Get(id)
	return a.tbl.Intern(errset.Value{Kind: errset.Guarded, Site: v.Site, Pos: v.Pos, Inner: []errset.ValueID{id}, Guards: gs})
}

// Verdict is what errtrace knows about whether a guard's check is true
// for an abstract error value.
type Verdict uint8

const (
	// No: the check is false for the value.
	No Verdict = iota
	// Yes: the check is true for the value.
	Yes
	// Maybe: errtrace cannot decide the check for the value.
	Maybe
	// Unknown: the value holds symbolic values (such as a Param) that a
	// caller has to substitute before the check can be decided.
	Unknown
)

// Decide returns whether the check of g is true for the value tree id,
// and, for Maybe, why it cannot be decided.
func Decide(tbl *errset.Table, id errset.ValueID, g errset.Guard) (Verdict, string) {
	switch g.Kind {
	case errset.GuardNil:
		// No abstract value is nil (a typed nil is a non-nil interface).
		return No, ""
	case errset.GuardHelper:
		return Maybe, ""
	case errset.GuardEqual:
		return decideTop(tbl, id, func(v errset.Value) (Verdict, string) {
			return equalVerdict(v, g)
		})
	case errset.GuardAssert:
		return decideTop(tbl, id, func(v errset.Value) (Verdict, string) {
			return typeVerdict(v, g.Type)
		})
	case errset.GuardIs:
		return decideTree(tbl, id, func(v errset.Value) (Verdict, string) {
			if verdict, why := equalVerdict(v, g); verdict != No {
				return verdict, why
			}
			if t := nodeType(v); t != nil && hasMethod(t, "Is") {
				return Maybe, "custom Is method of " + typeString(t)
			}
			return No, ""
		})
	case errset.GuardAs:
		return decideTree(tbl, id, func(v errset.Value) (Verdict, string) {
			verdict, why := typeVerdict(v, g.Type)
			if verdict == No {
				if t := nodeType(v); t != nil && hasMethod(t, "As") {
					return Maybe, "custom As method of " + typeString(t)
				}
			}
			return verdict, why
		})
	}
	return Maybe, ""
}

// decideTop decides a check that only looks at the top-level value.
func decideTop(tbl *errset.Table, id errset.ValueID, node func(errset.Value) (Verdict, string)) (Verdict, string) {
	v := tbl.Get(id)
	switch v.Kind {
	case errset.Unresolved:
		return Maybe, "unresolved value"
	case errset.Truncated:
		return Maybe, "Wrap Chain truncated"
	case errset.Param, errset.FreeVar, errset.CallRef, errset.ErrorfLike, errset.Guarded:
		return Unknown, ""
	}
	return node(v)
}

// decideTree decides a check that is true if it is true for any node of
// the value's Wrap Chains (errors.Is, errors.As).
func decideTree(tbl *errset.Table, id errset.ValueID, node func(errset.Value) (Verdict, string)) (Verdict, string) {
	found, unknown := false, false
	maybe := ""
	var walk func(errset.ValueID)
	walk = func(id errset.ValueID) {
		v := tbl.Get(id)
		var verdict Verdict
		why := ""
		switch v.Kind {
		case errset.Unresolved:
			verdict, why = Maybe, "unresolved value"
		case errset.Truncated:
			verdict, why = Maybe, "Wrap Chain truncated"
		case errset.Param, errset.FreeVar, errset.CallRef, errset.ErrorfLike, errset.Guarded:
			verdict = Unknown
		default:
			verdict, why = node(v)
		}
		switch verdict {
		case Yes:
			found = true
		case Unknown:
			unknown = true
		case Maybe:
			if maybe == "" {
				maybe = why
			}
		}
		for _, in := range v.Inner {
			walk(in)
		}
	}
	walk(id)
	switch {
	case found:
		return Yes, ""
	case unknown:
		return Unknown, ""
	case maybe != "":
		return Maybe, maybe
	}
	return No, ""
}

// equalVerdict says whether a node is == to the Sentinel of g (a
// GuardEqual or GuardIs, whose Type is the Sentinel's dynamic type when
// errtrace knows it). Only that Sentinel is equal to it for sure. A
// Concrete Type of a comparable non-pointer type may be equal to it: two
// equal struct values are ==, so unless the Sentinel's dynamic type is
// known to differ it is undecided. A pointer made by the program is a new
// pointer, never the Sentinel's.
func equalVerdict(v errset.Value, g errset.Guard) (Verdict, string) {
	switch v.Kind {
	case errset.Sentinel:
		if v.Var == g.Var {
			return Yes, ""
		}
	case errset.Concrete:
		t := unalias(v.Type)
		if v.TypedNil || t == nil || !types.Comparable(t) {
			return No, ""
		}
		if _, ok := t.Underlying().(*types.Pointer); ok {
			return No, ""
		}
		if g.Type != nil && !types.Identical(t, unalias(g.Type)) {
			return No, ""
		}
		return Maybe, "value of comparable type " + typeString(t) + " may equal " + g.Var.Name()
	}
	return No, ""
}

// nodeType returns the dynamic type of a value tree node, if errtrace
// knows it: that of a Concrete Type, of a Sentinel whose type or
// initializer gives it (see SentinelType), or of a custom Unwrap Wrap.
func nodeType(v errset.Value) types.Type {
	switch v.Kind {
	case errset.Concrete, errset.Sentinel:
		return v.Type
	case errset.Wrap:
		if v.WrapKind == errset.WrapUnwrapMethod {
			return v.Type
		}
	}
	return nil
}

// typeVerdict says whether a node's dynamic type is target (or implements
// it, for an interface target). An Opaque Error and the Wraps made by
// fmt.Errorf, errors.Join and errtrace.Wrap have types of their own that
// only have Error (and Unwrap) methods: they match an interface target
// that needs no more, and may match one that needs Unwrap; an Opaque
// Error, whose type errtrace does not model, may match any interface
// target. So may a Sentinel whose dynamic type errtrace does not know,
// which may match a concrete target too.
func typeVerdict(v errset.Value, target types.Type) (Verdict, string) {
	if t := nodeType(v); t != nil {
		if typeMatches(t, target) {
			return Yes, ""
		}
		return No, ""
	}
	iface, isIface := target.Underlying().(*types.Interface)
	switch v.Kind {
	case errset.Sentinel:
		if isIface && methodsWithin(iface, "Error") {
			return Yes, ""
		}
		return Maybe, "sentinel " + v.Var.Name() + " of unknown dynamic type"
	case errset.Opaque:
		if !isIface {
			return No, ""
		}
		if methodsWithin(iface, "Error") {
			return Yes, ""
		}
		return Maybe, "opaque error"
	case errset.Wrap:
		if !isIface {
			return No, ""
		}
		if methodsWithin(iface, "Error") {
			return Yes, ""
		}
		if methodsWithin(iface, "Error", "Unwrap") {
			return Maybe, "wrapper type of " + string(v.WrapKind)
		}
	}
	return No, ""
}

// typeMatches reports whether a value of dynamic type t is assignable to
// target: t is target, or implements the interface target.
func typeMatches(t, target types.Type) bool {
	t = unalias(t)
	if iface, ok := target.Underlying().(*types.Interface); ok {
		return types.Implements(t, iface)
	}
	return types.Identical(t, unalias(target))
}

// methodsWithin reports whether every method of iface is one of names.
func methodsWithin(iface *types.Interface, names ...string) bool {
	for i := range iface.NumMethods() {
		if !slices.Contains(names, iface.Method(i).Name()) {
			return false
		}
	}
	return true
}

// hasMethod reports whether the method set of t has a method name with
// one parameter and one bool result (Is(error) bool or As(any) bool).
func hasMethod(t types.Type, name string) bool {
	sel := types.NewMethodSet(t).Lookup(nil, name)
	if sel == nil {
		return false
	}
	sig, ok := sel.Type().(*types.Signature)
	if !ok || sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false
	}
	b, ok := sig.Results().At(0).Type().Underlying().(*types.Basic)
	return ok && b.Kind() == types.Bool
}

// Outcome is what applying a guard does to a value.
type Outcome uint8

const (
	// Keep: the value stays.
	Keep Outcome = iota
	// Remove: the guard proves the value does not take the path.
	Remove
	// Note: the value stays but is possibly handled by the guard.
	Note
	// Pending: the guard cannot be applied until the value's symbolic
	// values are substituted.
	Pending
)

// ApplyGuard applies g to the value tree id. On a path where g's check
// holds, values for which it is false are removed; where it does not
// hold, values for which it is true are removed and values for which it
// cannot be decided are kept and noted. A guard removes a value only when
// errtrace can prove it. The returned note is set for Note.
func ApplyGuard(tbl *errset.Table, id errset.ValueID, g errset.Guard) (Outcome, errset.GuardNote) {
	verdict, why := Decide(tbl, id, g)
	switch {
	case verdict == Unknown:
		return Pending, errset.GuardNote{}
	case g.Holds:
		if verdict == No {
			return Remove, errset.GuardNote{}
		}
		return Keep, errset.GuardNote{}
	case verdict == Yes:
		return Remove, errset.GuardNote{}
	case verdict == Maybe:
		reason := g.String()
		if why != "" {
			reason += ": " + why
		}
		return Note, errset.NoteOf(g, reason)
	}
	return Keep, errset.GuardNote{}
}

// sentinelTypes caches SentinelType per Global.
var sentinelTypes sync.Map // *ssa.Global -> sentinelType

type sentinelType struct{ t types.Type }

// SentinelType returns the dynamic type of the value of the package-level
// variable g, or nil if errtrace does not know it: g's own type if it is
// not an interface, else the one type that every store to g in its
// package's init function gives it. The stored values are followed
// through conversions, loads of other package-level variables and calls
// of functions whose every return gives the same type (errors.New gives
// *errors.errorString). A Sentinel is taken to keep the value of its
// initializer.
func SentinelType(g *ssa.Global) types.Type {
	if g == nil {
		return nil
	}
	if c, ok := sentinelTypes.Load(g); ok {
		return c.(sentinelType).t
	}
	t := globalDynType(g, make(map[any]bool), 0)
	sentinelTypes.Store(g, sentinelType{t})
	return t
}

// maxDynDepth bounds how deep dynType follows values.
const maxDynDepth = 8

func globalDynType(g *ssa.Global, visiting map[any]bool, depth int) types.Type {
	p, ok := g.Type().Underlying().(*types.Pointer)
	if !ok {
		return nil
	}
	if !types.IsInterface(p.Elem()) {
		return unalias(p.Elem())
	}
	if visiting[g] || g.Pkg == nil {
		return nil
	}
	visiting[g] = true
	defer delete(visiting, g)
	init := g.Pkg.Func("init")
	if init == nil {
		return nil
	}
	var out types.Type
	for _, b := range init.Blocks {
		for _, instr := range b.Instrs {
			st, ok := instr.(*ssa.Store)
			if !ok || st.Addr != g {
				continue
			}
			t := dynType(st.Val, visiting, depth+1)
			if t == nil || (out != nil && !types.Identical(out, t)) {
				return nil
			}
			out = t
		}
	}
	return out
}

// dynType returns the dynamic type of the interface value v, if every
// value it can have has the same one that errtrace can find, or nil.
func dynType(v ssa.Value, visiting map[any]bool, depth int) types.Type {
	if depth > maxDynDepth {
		return nil
	}
	switch v := v.(type) {
	case *ssa.MakeInterface:
		if t := v.X.Type(); !types.IsInterface(t) {
			return unalias(t)
		}
	case *ssa.ChangeInterface:
		return dynType(v.X, visiting, depth+1)
	case *ssa.UnOp:
		if g, ok := v.X.(*ssa.Global); ok && v.Op == token.MUL {
			return globalDynType(g, visiting, depth+1)
		}
	case *ssa.Phi:
		return sameType(v.Edges, visiting, depth)
	case *ssa.Call:
		fn := v.Common().StaticCallee()
		if fn == nil || fn.Blocks == nil || fn.Signature.Results().Len() != 1 || visiting[fn] {
			return nil
		}
		visiting[fn] = true
		defer delete(visiting, fn)
		var results []ssa.Value
		for _, b := range fn.Blocks {
			if len(b.Instrs) == 0 {
				continue
			}
			if r, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return); ok {
				results = append(results, r.Results[0])
			}
		}
		return sameType(results, visiting, depth)
	}
	return nil
}

// sameType returns the dynamic type all of vs have, or nil.
func sameType(vs []ssa.Value, visiting map[any]bool, depth int) types.Type {
	var out types.Type
	for _, e := range vs {
		t := dynType(e, visiting, depth+1)
		if t == nil || (out != nil && !types.Identical(out, t)) {
			return nil
		}
		out = t
	}
	return out
}
