package flow

import (
	"fmt"
	"go/constant"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
)

// Fact is one abstract value that an error result of a function can carry,
// with the SSA value where it enters the function (hop 0 of its
// Propagation Path): the load of a Sentinel, the composite literal or
// conversion of a Concrete Type, the errors.New call, the call whose result
// is returned, the parameter, or the load that could not be followed.
type Fact struct {
	// Value is the abstract value, interned in the analysis's table.
	Value errset.ValueID
	// Source is the ssa.Value (or, for an implicit conversion, the
	// ssa.DebugRef of its operand) where Value enters the function.
	Source any
}

// ResultFacts are the local facts of one error result.
type ResultFacts struct {
	// Index is the result's index in the function's signature.
	Index int
	// Facts are the values the result can carry, in the order they were
	// found (by return statement, then by definition).
	Facts []Fact
}

// LocalFacts is what the intraprocedural analysis knows about the error
// results of one function. Its values may be Error Origins, Unresolved
// values, or symbolic values (Param, FreeVar, CallRef) that callers and
// callees resolve.
type LocalFacts struct {
	// Fn is the analysed function.
	Fn *ssa.Function
	// Results has one element per error result, in signature order.
	Results []ResultFacts
}

// Analyze computes the local facts of fn's error results by walking the
// definitions of every returned value backwards. Values are interned in
// tbl. Calls are not followed: their results become CallRef values. A
// function without a body has no facts.
func Analyze(tbl *errset.Table, fn *ssa.Function) *LocalFacts {
	lf := &LocalFacts{Fn: fn}
	for _, idx := range ErrorResults(fn.Signature) {
		lf.Results = append(lf.Results, ResultFacts{Index: idx})
	}
	if len(lf.Results) == 0 || fn.Blocks == nil {
		return lf
	}
	a := &analyzer{tbl: tbl, fn: fn}
	for _, b := range fn.Blocks {
		ret, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return)
		if !ok {
			continue
		}
		for i := range lf.Results {
			r := &lf.Results[i]
			a.ret = ret
			a.seen = make(map[ssa.Value]bool)
			a.facts = r.Facts
			a.walk(ret.Results[r.Index])
			r.Facts = a.facts
		}
	}
	return lf
}

// analyzer walks value definitions for one return operand.
type analyzer struct {
	tbl   *errset.Table
	fn    *ssa.Function
	ret   *ssa.Return
	seen  map[ssa.Value]bool
	facts []Fact
}

func (a *analyzer) add(id errset.ValueID, src any) {
	for _, f := range a.facts {
		if f.Value == id && f.Source == src {
			return
		}
	}
	a.facts = append(a.facts, Fact{Value: id, Source: src})
}

// posOf returns a valid position for v: its own, its operand's for an
// implicit conversion, or the enclosing return statement's.
func (a *analyzer) posOf(v ssa.Value) token.Pos {
	if p := v.Pos(); p.IsValid() {
		return p
	}
	if mi, ok := v.(*ssa.MakeInterface); ok {
		if p := mi.X.Pos(); p.IsValid() {
			return p
		}
	}
	return a.ret.Pos()
}

func (a *analyzer) walk(v ssa.Value) {
	if a.seen[v] {
		return
	}
	a.seen[v] = true
	switch v := v.(type) {
	case *ssa.Const:
		if v.IsNil() {
			return // no error
		}
	case *ssa.Phi:
		for _, e := range v.Edges {
			a.walk(e)
		}
		return
	case *ssa.ChangeInterface:
		a.walk(v.X)
		return
	case *ssa.ChangeType:
		a.walk(v.X)
		return
	case *ssa.MakeInterface:
		a.makeInterface(v)
		return
	case *ssa.Alloc:
		// A pointer-typed error result built in place: &T{...} or new(T).
		a.add(a.concrete(v.Type(), v, false), v)
		return
	case *ssa.UnOp:
		if a.load(v) {
			return
		}
	case *ssa.Call:
		a.call(v, 0)
		return
	case *ssa.Extract:
		if call, ok := v.Tuple.(*ssa.Call); ok {
			a.call(call, v.Index)
			return
		}
		a.memory(v, extractDetail(v))
		return
	case *ssa.Parameter:
		a.param(v)
		return
	case *ssa.FreeVar:
		a.freeVar(v, v)
		return
	case *ssa.Field:
		a.memory(v, fmt.Sprintf("load of field %s of %s", fieldName(v.X.Type(), v.Field), typeString(v.X.Type())))
		return
	case *ssa.Index:
		a.memory(v, "load of array element")
		return
	case *ssa.Lookup:
		a.memory(v, "load of map element")
		return
	}
	a.memory(v, fmt.Sprintf("value of %s not modelled", ssaOp(v)))
}

// makeInterface classifies a conversion of a concrete value to an
// interface: a pointer Sentinel, a typed nil, or a Concrete Type.
func (a *analyzer) makeInterface(mi *ssa.MakeInterface) {
	switch x := mi.X.(type) {
	case *ssa.UnOp:
		if g, ok := x.X.(*ssa.Global); ok && x.Op == token.MUL {
			a.add(a.sentinel(g, x.Type()), x)
			return
		}
		if alloc, ok := x.X.(*ssa.Alloc); ok && x.Op == token.MUL && alloc.Comment == "complit" {
			// A value-typed composite literal: local T (complit); *t.
			a.add(a.concrete(x.Type(), alloc, false), alloc)
			return
		}
	case *ssa.Const:
		if x.IsNil() {
			// The implicit conversion has no position; name the operand
			// expression of the return statement when debug info has it.
			var site any = mi
			if ref := a.debugRefAtReturn(x); ref != nil {
				site = ref
			}
			id := a.tbl.Intern(errset.Value{Kind: errset.Concrete, Site: site, Pos: a.posOf(mi), Type: unalias(x.Type()), TypedNil: true})
			a.add(id, site)
			return
		}
	case *ssa.Alloc:
		a.add(a.concrete(x.Type(), x, false), x)
		return
	}
	a.add(a.concrete(mi.X.Type(), mi, false), mi)
}

// debugRefAtReturn returns the first DebugRef of v at or after the
// current return statement's position in its block, or nil.
func (a *analyzer) debugRefAtReturn(v ssa.Value) *ssa.DebugRef {
	for _, instr := range a.ret.Block().Instrs {
		if d, ok := instr.(*ssa.DebugRef); ok && d.X == v && d.Pos() >= a.ret.Pos() {
			return d
		}
	}
	return nil
}

// load classifies a unary operation; it reports false if v is not a load
// (or receive) it knows.
func (a *analyzer) load(v *ssa.UnOp) bool {
	switch v.Op {
	case token.ARROW:
		a.memory(v, "channel receive")
		return true
	case token.MUL:
	default:
		return false
	}
	switch x := v.X.(type) {
	case *ssa.Global:
		var typ types.Type
		if !types.IsInterface(v.Type()) {
			typ = v.Type() // a pointer Sentinel returned as a concrete error result
		}
		a.add(a.sentinel(x, typ), v)
	case *ssa.FieldAddr:
		a.memory(v, fmt.Sprintf("load of field %s of %s", fieldName(x.X.Type(), x.Field), typeString(x.X.Type())))
	case *ssa.IndexAddr:
		a.memory(v, "load of slice or array element")
	case *ssa.FreeVar:
		a.freeVar(x, v)
	case *ssa.Alloc:
		a.local(v, x)
	default:
		a.memory(v, "load through pointer")
	}
	return true
}

// local follows a load of a local variable that was not lifted to a
// register (such as a named result in a function with defers) to the
// values stored into it. If the variable's address escapes, or it is a
// composite literal of a concrete error type, the load is classified
// accordingly.
func (a *analyzer) local(load *ssa.UnOp, alloc *ssa.Alloc) {
	if alloc.Comment == "complit" {
		a.add(a.concrete(load.Type(), alloc, false), alloc)
		return
	}
	if alloc.Heap {
		a.memory(load, "load of escaping variable "+alloc.Comment)
		return
	}
	escapes := false
	for _, r := range *alloc.Referrers() {
		switch r := r.(type) {
		case *ssa.Store:
			if r.Addr == alloc {
				a.walk(r.Val)
				continue
			}
			escapes = true
		case *ssa.UnOp, *ssa.DebugRef:
		default:
			escapes = true
		}
	}
	if escapes {
		a.memory(load, "load of variable "+alloc.Comment+" whose address escapes")
	}
}

// call records result index of call: an Opaque Error for errors.New, and
// a CallRef otherwise.
func (a *analyzer) call(call *ssa.Call, index int) {
	common := call.Common()
	if callee := common.StaticCallee(); callee != nil && isFunc(callee, "errors", "New") {
		v := errset.Value{Kind: errset.Opaque, Site: call, Pos: a.posOf(call), Constructor: "errors.New"}
		if c, ok := common.Args[0].(*ssa.Const); ok && c.Value != nil {
			v.Message = constMessage(c)
			v.HasMessage = true
		}
		a.add(a.tbl.Intern(v), call)
		return
	}
	a.add(a.tbl.Intern(errset.Value{
		Kind:   errset.CallRef,
		Site:   call,
		Pos:    a.posOf(call),
		Detail: CalleeName(common),
		Index:  index,
	}), call)
}

func (a *analyzer) param(p *ssa.Parameter) {
	idx := 0
	for i, q := range a.fn.Params {
		if q == p {
			idx = i
		}
	}
	a.add(a.tbl.Intern(errset.Value{
		Kind:  errset.Param,
		Site:  p,
		Pos:   p.Pos(),
		Fn:    a.fn.String(),
		Type:  p.Type(),
		Index: idx,
		Name:  p.Name(),
	}), p)
}

// freeVar records the captured variable fv, read at src.
func (a *analyzer) freeVar(fv *ssa.FreeVar, src ssa.Value) {
	idx := 0
	for i, q := range a.fn.FreeVars {
		if q == fv {
			idx = i
		}
	}
	typ := fv.Type()
	if src != fv {
		typ = src.Type()
	}
	a.add(a.tbl.Intern(errset.Value{
		Kind:  errset.FreeVar,
		Site:  src,
		Pos:   fv.Pos(),
		Fn:    a.fn.String(),
		Type:  typ,
		Index: idx,
		Name:  fv.Name(),
	}), src)
}

func (a *analyzer) memory(v ssa.Value, detail string) {
	a.add(a.tbl.Intern(errset.Value{
		Kind:       errset.Unresolved,
		Site:       v,
		Pos:        a.posOf(v),
		Unresolved: errset.Memory,
		Detail:     detail,
	}), v)
}

func (a *analyzer) sentinel(g *ssa.Global, typ types.Type) errset.ValueID {
	obj, _ := g.Object().(*types.Var)
	if obj == nil {
		// A synthetic global; it has no variable to name.
		return a.tbl.Intern(errset.Value{
			Kind: errset.Unresolved, Site: g, Pos: g.Pos(),
			Unresolved: errset.Memory, Detail: "load of global " + g.Name(),
		})
	}
	return a.tbl.Intern(errset.Value{Kind: errset.Sentinel, Site: obj, Pos: obj.Pos(), Var: obj, Type: typ})
}

func (a *analyzer) concrete(typ types.Type, site ssa.Value, typedNil bool) errset.ValueID {
	typ = unalias(typ)
	return a.tbl.Intern(errset.Value{Kind: errset.Concrete, Site: site, Pos: a.posOf(site), Type: typ, TypedNil: typedNil})
}

// CalleeName returns how a call names its callee in output: the SSA name of
// a static callee, the method of an interface call, or the called value.
func CalleeName(c *ssa.CallCommon) string {
	if callee := c.StaticCallee(); callee != nil {
		return callee.String()
	}
	if c.IsInvoke() {
		return "(" + typeString(c.Value.Type()) + ")." + c.Method.Name()
	}
	return "function value " + c.Value.Name()
}

func isFunc(fn *ssa.Function, pkgPath, name string) bool {
	return fn.Pkg != nil && fn.Pkg.Pkg.Path() == pkgPath && fn.Name() == name && fn.Signature.Recv() == nil
}

func constMessage(c *ssa.Const) string {
	if c.Value.Kind() == constant.String {
		return constant.StringVal(c.Value)
	}
	return c.Value.ExactString()
}

// unalias returns t, or the pointer type *T for *A, with aliases resolved,
// so that one Concrete Type has one name (*fs.PathError, not *os.PathError).
func unalias(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		if elem := types.Unalias(p.Elem()); elem != p.Elem() {
			return types.NewPointer(elem)
		}
		return p
	}
	return types.Unalias(t)
}

func typeString(t types.Type) string { return types.TypeString(t, nil) }

// fieldName returns the name of field i of the struct that t (or the type t
// points to) is.
func fieldName(t types.Type, i int) string {
	if p, ok := t.Underlying().(*types.Pointer); ok {
		t = p.Elem()
	}
	if s, ok := t.Underlying().(*types.Struct); ok && i < s.NumFields() {
		return s.Field(i).Name()
	}
	return fmt.Sprintf("#%d", i)
}

func extractDetail(v *ssa.Extract) string {
	switch t := v.Tuple.(type) {
	case *ssa.Lookup:
		return "load of map element"
	case *ssa.UnOp:
		if t.Op == token.ARROW {
			return "channel receive"
		}
	case *ssa.Select:
		return "channel receive in select"
	}
	return fmt.Sprintf("value of %s not modelled", ssaOp(v.Tuple))
}

// ssaOp names the SSA operation that defines v, for details.
func ssaOp(v ssa.Value) string {
	return fmt.Sprintf("%T", v)[len("*ssa."):]
}
