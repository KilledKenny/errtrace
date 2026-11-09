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
	// cut is set, inside an analyzer at the Wrap Chain depth limit, for a
	// leaf found below a further Wrap: its chain is longer than the limit.
	cut bool
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

// DefaultChainDepth is the Wrap Chain depth limit used when
// Options.ChainDepth is not set.
const DefaultChainDepth = 8

// maxCombos caps the number of Wraps one wrapping call can make from the
// combinations of its operands' values.
const maxCombos = 1024

// Options configures the intraprocedural analysis.
type Options struct {
	// ChainDepth is the most Wraps a Wrap Chain built in one function can
	// have; the values below a deeper Wrap are marked Truncated. Zero means
	// DefaultChainDepth.
	ChainDepth int
}

func (o Options) chainDepth() int {
	if o.ChainDepth <= 0 {
		return DefaultChainDepth
	}
	return o.ChainDepth
}

// Analyze computes the local facts of fn's error results by walking the
// definitions of every returned value backwards. Values are interned in
// tbl. Calls are not followed: their results become CallRef values, except
// for intrinsics (errors.New, fmt.Errorf, errors.Join, errtrace.Wrap),
// which become Error Origins and Wraps. A Concrete Type built from a
// composite literal whose type has a modelled Unwrap method becomes a Wrap
// of the value stored in the unwrapped field. A function without a body
// has no facts.
func Analyze(tbl *errset.Table, fn *ssa.Function, opts Options) *LocalFacts {
	lf := &LocalFacts{Fn: fn}
	for _, idx := range ErrorResults(fn.Signature) {
		lf.Results = append(lf.Results, ResultFacts{Index: idx})
	}
	if len(lf.Results) == 0 || fn.Blocks == nil {
		return lf
	}
	a := &analyzer{tbl: tbl, fn: fn, opts: opts}
	for _, b := range fn.Blocks {
		ret, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return)
		if !ok {
			continue
		}
		for i := range lf.Results {
			r := &lf.Results[i]
			a.at = ret
			a.seen = make(map[ssa.Value]bool)
			a.facts = r.Facts
			a.walk(ret.Results[r.Index])
			r.Facts = a.facts
		}
	}
	return lf
}

// ValueFacts returns the local facts of v, a value of fn used by the
// instruction at (such as an argument of a call): the abstract values v can
// carry, found by walking its definitions backwards exactly as Analyze does
// for a returned value. Values are interned in tbl.
func ValueFacts(tbl *errset.Table, fn *ssa.Function, v ssa.Value, at ssa.Instruction, opts Options) []Fact {
	a := &analyzer{tbl: tbl, fn: fn, at: at, seen: make(map[ssa.Value]bool), opts: opts}
	a.walk(v)
	return a.facts
}

// analyzer walks value definitions for one operand of the instruction at
// (a return operand or a call argument).
type analyzer struct {
	tbl   *errset.Table
	fn    *ssa.Function
	at    ssa.Instruction
	seen  map[ssa.Value]bool
	facts []Fact
	opts  Options
	// depth is the number of Wraps above the values this analyzer finds.
	depth int
	// flat is set below the Wrap Chain depth limit: wrapping calls are
	// then walked through to their operands' leaves, which the caller
	// marks Truncated.
	flat bool
	// cut is set in flat mode while walking the operands of a wrapping
	// call: the leaves found then lie below the depth limit and are
	// marked Truncated. seenCut is the seen set of that walk.
	cut     bool
	seenCut map[ssa.Value]bool
}

func (a *analyzer) add(id errset.ValueID, src any) {
	for _, f := range a.facts {
		if f.Value == id && f.Source == src && f.cut == a.cut {
			return
		}
	}
	a.facts = append(a.facts, Fact{Value: id, Source: src, cut: a.cut})
}

// posOf returns a valid position for v: its own, its operand's for an
// implicit conversion, or the position of the instruction that uses it.
func (a *analyzer) posOf(v ssa.Value) token.Pos {
	if p := v.Pos(); p.IsValid() {
		return p
	}
	if mi, ok := v.(*ssa.MakeInterface); ok {
		if p := mi.X.Pos(); p.IsValid() {
			return p
		}
	}
	return a.at.Pos()
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
		a.constructed(v.Type(), v)
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
			a.constructed(x.Type(), alloc)
			return
		}
	case *ssa.Const:
		if x.IsNil() {
			// The implicit conversion has no position; name the operand
			// expression of the return statement when debug info has it.
			var site any = mi
			if ref := a.debugRefAtUse(x); ref != nil {
				site = ref
			}
			id := a.tbl.Intern(errset.Value{Kind: errset.Concrete, Site: site, Pos: a.posOf(mi), Type: unalias(x.Type()), TypedNil: true})
			a.add(id, site)
			return
		}
	case *ssa.Alloc:
		a.constructed(x.Type(), x)
		return
	case *ssa.Call:
		// A call returning a concrete error type (func f() *NumError):
		// its summary says what it builds, Wraps included.
		if IsErrorType(x.Type()) {
			a.call(x, 0)
			return
		}
	case *ssa.Extract:
		if call, ok := x.Tuple.(*ssa.Call); ok && IsErrorType(x.Type()) {
			a.call(call, x.Index)
			return
		}
	}
	a.add(a.concrete(mi.X.Type(), mi, false), mi)
}

// debugRefAtUse returns the first DebugRef of v at or after the position
// of the instruction that uses it, in its block, or nil.
func (a *analyzer) debugRefAtUse(v ssa.Value) *ssa.DebugRef {
	for _, instr := range a.at.Block().Instrs {
		if d, ok := instr.(*ssa.DebugRef); ok && d.X == v && d.Pos() >= a.at.Pos() {
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
		a.constructed(load.Type(), alloc)
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

// call records result index of call: an Opaque Error for errors.New, the
// Wraps or Opaque Error of the other intrinsics, and a CallRef otherwise.
func (a *analyzer) call(call *ssa.Call, index int) {
	common := call.Common()
	if name, ok := Intrinsic(common.StaticCallee()); ok && index == 0 {
		switch name {
		case "errors.New":
			v := errset.Value{Kind: errset.Opaque, Site: call, Pos: a.posOf(call), Constructor: "errors.New"}
			if c, ok := common.Args[0].(*ssa.Const); ok && c.Value != nil {
				v.Message = constMessage(c)
				v.HasMessage = true
			}
			a.add(a.tbl.Intern(v), call)
		case "fmt.Errorf":
			a.errorf(call, a.posOf(call), common.Args[0], common.Args[1])
		case "errors.Join":
			a.join(call, common.Args[0])
		case "errtrace.Wrap":
			a.bracesWrap(call, common.Args[0])
		}
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
	switch v := c.Value.(type) {
	case *ssa.UnOp:
		if g, ok := v.X.(*ssa.Global); ok && v.Op == token.MUL {
			return "function value " + g.String()
		}
	case *ssa.Const:
		return "function value nil"
	case *ssa.Parameter, *ssa.FreeVar:
		return "function value " + v.Name()
	}
	return "function value of type " + typeString(c.Value.Type())
}

// intrinsics are the functions whose effect on errors errtrace models
// itself instead of analysing their bodies, keyed on package path and
// name, with the name they are shown under.
var intrinsics = map[[2]string]string{
	{"errors", "New"}:               "errors.New",
	{"errors", "Join"}:              "errors.Join",
	{"fmt", "Errorf"}:               "fmt.Errorf",
	{"braces.dev/errtrace", "Wrap"}: "errtrace.Wrap",
}

// Intrinsic reports whether fn is an intrinsic: a package-level function,
// such as errors.New, that the analysis models directly. An intrinsic never
// has its body summarised. It returns the name the intrinsic is shown
// under.
func Intrinsic(fn *ssa.Function) (string, bool) {
	if fn == nil || fn.Pkg == nil || fn.Signature.Recv() != nil {
		return "", false
	}
	name, ok := intrinsics[[2]string{fn.Pkg.Pkg.Path(), fn.Name()}]
	return name, ok
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
