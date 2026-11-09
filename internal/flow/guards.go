package flow

import (
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"

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
	// nonNil are, per point asked for, the values that a check that
	// holds there proves non-nil (see provesNonNil).
	nonNil map[point][]ssa.Value
}

// newFuncGuards recognises the guards of fn's Ifs.
func newFuncGuards(fn *ssa.Function, sents *Sentinels) *funcGuards {
	gd := &funcGuards{conds: make(map[*ssa.BasicBlock][]condGuard), at: make(map[point][]guardUse), nonNil: make(map[point][]ssa.Value)}
	n := 0
	for _, b := range fn.Blocks {
		if len(b.Instrs) == 0 {
			continue
		}
		ifInstr, ok := b.Instrs[len(b.Instrs)-1].(*ssa.If)
		if !ok || len(b.Succs) != 2 {
			continue
		}
		for _, cg := range recognise(ifInstr.Cond, sents) {
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
// predecessor's If whose one branch is the edge. A check err != nil that
// holds changes no abstract value and is left out; isNonNil uses it.
func (gd *funcGuards) guardsAt(pt point) []guardUse {
	if pt.b == nil {
		return nil
	}
	if out, ok := gd.at[pt]; ok {
		return out
	}
	var out []guardUse
	var nonNil []ssa.Value
	add := func(b *ssa.BasicBlock, outcome bool) {
		for _, cg := range gd.conds[b] {
			g := cg.g
			g.Holds = outcome == cg.positive
			if provesNonNil(g) {
				nonNil = append(nonNil, cg.raw, cg.operand)
			}
			if g.Kind == errset.GuardNil && !g.Holds {
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
	gd.at[pt], gd.nonNil[pt] = out, nonNil
	return out
}

// provesNonNil reports whether the check of g, as it went, proves the
// tested value non-nil: err != nil, a type assertion or errors.As that
// succeeded, or err == X or errors.Is(err, X) that holds for a Sentinel X
// that is never nil (Fresh and not reassigned).
func provesNonNil(g errset.Guard) bool {
	switch g.Kind {
	case errset.GuardNil:
		return !g.Holds
	case errset.GuardAssert, errset.GuardAs:
		return g.Holds
	case errset.GuardEqual, errset.GuardIs:
		return g.Holds && g.Fresh && !g.Unstable
	}
	return false
}

// isNonNil reports whether a check that holds at pt proves v non-nil
// there (see provesNonNil).
func (gd *funcGuards) isNonNil(pt point, v ssa.Value) bool {
	if pt.b == nil {
		return false
	}
	gd.guardsAt(pt)
	return slices.Contains(gd.nonNil[pt], v)
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
func recognise(cond ssa.Value, sents *Sentinels) []condGuard {
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
			return []condGuard{mk(x, sentinelGuard(errset.GuardEqual, v, sents.Info(g)), pos)}
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
					return []condGuard{mk(common.Args[0], sentinelGuard(errset.GuardIs, v, sents.Info(g)), positive)}
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

// sentinelGuard returns a GuardEqual or GuardIs with the Sentinel v.
func sentinelGuard(kind errset.GuardKind, v *types.Var, inf SentinelInfo) errset.Guard {
	return errset.Guard{Kind: kind, Var: v, Type: inf.Type, Canon: canonVar(inf, v), Fresh: inf.Fresh, Unstable: inf.Unstable}
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
	for _, u := range a.funcGuards().guardsAt(a.pt) {
		if u.raw != v && u.operand != v {
			continue
		}
		i, found := slices.BinarySearchFunc(a.guards, u.id, func(x guardUse, id int) int { return x.id - id })
		if !found {
			a.guards = slices.Insert(slices.Clip(a.guards), i, u)
		}
	}
}

// funcGuards returns the guards of the analyzer's function, shared with
// its child analyzers.
func (a *analyzer) funcGuards() *funcGuards {
	if a.gd == nil {
		a.gd = newFuncGuards(a.fn, a.sentinels())
	}
	return a.gd
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
			t := nodeType(v)
			if t != nil && hasMethod(t, "Is") {
				return Maybe, "custom Is method of " + typeString(t)
			}
			if t == nil && v.Kind == errset.Sentinel {
				// Its type, which errtrace does not know, may have an
				// Is method that matches.
				return Maybe, "sentinel " + qualifiedName(v.Var) + " of unknown dynamic type may have a custom Is method"
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
// the value's Wrap Chains (errors.Is, errors.As). A node below a Wrap that
// may hold nothing for one of its operands (MayHoldNil) may not be in the
// chain at run time, so the check being true for it gives only Maybe.
func decideTree(tbl *errset.Table, id errset.ValueID, node func(errset.Value) (Verdict, string)) (Verdict, string) {
	found, unknown := false, false
	maybe := ""
	var walk func(errset.ValueID, bool)
	walk = func(id errset.ValueID, mayBeMissing bool) {
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
		if verdict == Yes && mayBeMissing {
			verdict, why = Maybe, "wrapped value may be nil"
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
		below := mayBeMissing || (v.Kind == errset.Wrap && v.MayHoldNil)
		for _, in := range v.Inner {
			walk(in, below)
		}
	}
	walk(id, false)
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
// errtrace knows it). A Sentinel is equal to it for sure when both hold
// the value of the same variable (os.ErrNotExist and fs.ErrNotExist), and
// differs from it for sure when both values are pointers allocated for
// their own variables, or when their dynamic types are known and differ;
// otherwise the two may hold the same value. A typed nil differs from a
// Fresh Sentinel, which holds a non-nil pointer, but may equal any other
// Sentinel of its type, which may hold a nil pointer of that type. A
// Concrete Type of a comparable type may be equal to it unless the
// Sentinel's dynamic type is known to differ: two equal struct values are
// ==, and a pointer may be the one stored in the variable. Only a new
// allocation (Allocated) of a type of non-zero size against a Fresh
// Sentinel differs for sure; a pointer from a type assertion, an
// errors.As target, a parameter or a load may be the Sentinel's own.
// Pointers to distinct zero-size variables may be == (Go spec,
// Comparison operators), so neither they nor Fresh Sentinels of such
// types (see Sentinels.fresh) are taken as different. An Opaque Error or
// a library Wrap is a new allocation too, decided by
// newLibraryErrorVerdict; a custom-Unwrap Wrap is decided as the
// allocated Concrete Type it is. Two loads of a variable that is
// assigned outside init (Unstable) may give different values, so a
// Sentinel of that variable may differ from it.
func equalVerdict(v errset.Value, g errset.Guard) (Verdict, string) {
	switch v.Kind {
	case errset.Sentinel:
		if v.Var == g.Var || sentinelCanon(v.Canon, v.Var) == sentinelCanon(g.Canon, g.Var) {
			if g.Unstable {
				// The variable may have been reassigned between the
				// load that made the node and the one the check makes.
				return Maybe, qualifiedName(g.Var) + " is reassigned outside its initialization"
			}
			return Yes, ""
		}
		if v.Fresh && g.Fresh {
			return No, ""
		}
		if v.Type != nil && g.Type != nil && !types.Identical(unalias(v.Type), unalias(g.Type)) {
			// Interface values of different dynamic types are never ==.
			return No, ""
		}
		return Maybe, "sentinel " + qualifiedName(v.Var) + " may be the same value as " + qualifiedName(g.Var)
	case errset.Concrete:
		t := unalias(v.Type)
		if t == nil || !types.Comparable(t) {
			return No, ""
		}
		if g.Type != nil && !types.Identical(t, unalias(g.Type)) {
			return No, ""
		}
		if v.TypedNil {
			if g.Fresh {
				// The Sentinel holds a non-nil pointer allocated for it.
				return No, ""
			}
			// The Sentinel may hold a nil pointer of the same type
			// (var ErrX error = (*T)(nil), or one returned by its
			// initializer), and two such interface values are ==.
			return Maybe, "typed nil " + typeString(t) + " may equal " + g.Var.Name()
		}
		if ptr, ok := t.Underlying().(*types.Pointer); ok {
			switch {
			case mayBeZeroSize(ptr.Elem()):
				// Pointers to distinct zero-size variables may be ==
				// (the runtime points them all at one address).
				return Maybe, "pointer to zero-size type " + typeString(ptr.Elem()) + " may equal " + g.Var.Name()
			case g.Fresh && v.Allocated:
				// The Sentinel's pointer was allocated for it alone, and
				// this one is a new allocation.
				return No, ""
			}
			// The Sentinel's value may be a pointer the program also
			// returns (stored to it outside init, cached by the function
			// that initializes it, or taken out of the Sentinel itself by
			// a type assertion or errors.As).
			return Maybe, "pointer of type " + typeString(t) + " may be the value of " + g.Var.Name()
		}
		return Maybe, "value of comparable type " + typeString(t) + " may equal " + g.Var.Name()
	case errset.Opaque:
		return newLibraryErrorVerdict(g, "opaque error")
	case errset.Wrap:
		if v.WrapKind == errset.WrapUnwrapMethod {
			// A composite literal of the wrapper type: a new value of
			// v.Type, decided as a Concrete Type Origin allocated there.
			return equalVerdict(errset.Value{Kind: errset.Concrete, Type: v.Type, Allocated: true}, g)
		}
		return newLibraryErrorVerdict(g, "wrapped error")
	}
	return No, ""
}

// newLibraryErrorVerdict says whether a new error made by a library
// function (an Opaque Error from errors.New or fmt.Errorf, a Wrap from
// fmt.Errorf with %w, errors.Join or errtrace.Wrap) is == to the Sentinel
// of g. Such an error is a pointer allocated by the call, so it differs
// from a Fresh Sentinel, which holds a pointer allocated for it alone,
// and from a Sentinel whose dynamic type is known to be another one than
// the unexported types of those packages. Otherwise the Sentinel may
// hold that very pointer (stored into it outside init, or cached by the
// function that initializes it).
func newLibraryErrorVerdict(g errset.Guard, what string) (Verdict, string) {
	if g.Fresh {
		return No, ""
	}
	if g.Type != nil && !isLibraryErrorType(g.Type) {
		return No, ""
	}
	return Maybe, what + " may be the value of " + g.Var.Name()
}

// bracesErrtracePath is the import path of the errtrace library.
const bracesErrtracePath = "braces.dev/errtrace"

// libraryErrorPkgs are the packages whose unexported error types the
// errors errtrace models as Opaque Errors and library Wraps may have.
var libraryErrorPkgs = map[string]bool{"errors": true, "fmt": true, bracesErrtracePath: true}

// isLibraryErrorType reports whether t may be the dynamic type of an
// error made by errors.New, fmt.Errorf, errors.Join or errtrace.Wrap: a
// pointer to a named type of one of those packages.
func isLibraryErrorType(t types.Type) bool {
	p, ok := unalias(t).Underlying().(*types.Pointer)
	if !ok {
		return false
	}
	n, ok := types.Unalias(p.Elem()).(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return false
	}
	return libraryErrorPkgs[n.Obj().Pkg().Path()]
}

// mayBeZeroSize reports whether values of t may have size zero: an empty
// struct, an array of length zero, a struct or array of such, or a type
// parameter.
func mayBeZeroSize(t types.Type) bool {
	return mayBeZeroSizeDepth(t, 0)
}

func mayBeZeroSizeDepth(t types.Type, depth int) bool {
	if depth > maxDynDepth {
		return true
	}
	if _, ok := unalias(t).(*types.TypeParam); ok {
		return true
	}
	switch u := unalias(t).Underlying().(type) {
	case *types.Struct:
		for i := range u.NumFields() {
			if !mayBeZeroSizeDepth(u.Field(i).Type(), depth+1) {
				return false
			}
		}
		return true
	case *types.Array:
		return u.Len() == 0 || mayBeZeroSizeDepth(u.Elem(), depth+1)
	}
	return false
}

// sentinelCanon returns canon, or v if canon is nil.
func sentinelCanon(canon, v *types.Var) *types.Var {
	if canon != nil {
		return canon
	}
	return v
}

// qualifiedName returns pkg.Name of a package-level variable.
func qualifiedName(v *types.Var) string {
	if v == nil {
		return "?"
	}
	if v.Pkg() != nil {
		return v.Pkg().Name() + "." + v.Name()
	}
	return v.Name()
}

// nodeType returns the dynamic type of a value tree node, if errtrace
// knows it: that of a Concrete Type, of a Sentinel whose type or
// initializer gives it (see Sentinels.Info), or of a custom Unwrap Wrap.
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
// fmt.Errorf and errors.Join have types of their own that only have Error
// (and Unwrap) methods: they match an interface target that needs no
// more, and may match one that needs Unwrap. errtrace.Wrap's wrapper type
// has more methods, which errtrace does not model: it matches an
// interface target that needs only Error and may match any other
// interface target (or a type of the errtrace package itself); an Opaque
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
		if v.WrapKind == errset.WrapBraces {
			// errtrace.Wrap's wrapper type is the library's own and its
			// method set varies by version (Format, LogValue, TracePC,
			// ...), so only Error is known.
			if isIface && methodsWithin(iface, "Error") {
				return Yes, ""
			}
			if isIface || inPackage(target, bracesErrtracePath) {
				return Maybe, "wrapper type of " + string(v.WrapKind)
			}
			return No, ""
		}
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

// inPackage reports whether t, or the type it points to, is a named type
// declared in the package with the given path.
func inPackage(t types.Type, path string) bool {
	t = unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = unalias(p.Elem())
	}
	n, ok := t.(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == path
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
