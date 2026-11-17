package flow

import (
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"slices"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/errset"
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
	// ssa.DebugRef of its operand) where Value enters the function. For a
	// Wrap made in the function it is the wrapping call or literal.
	Source any
	// Leaves, if set, is where each leaf of Value (in errset.Flatten's
	// leaf order) enters the function: for a Wrap made here, the place of
	// each wrapped value. Its length is the number of leaves of Value.
	// When it is nil, every leaf enters at Source.
	Leaves []any
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
	// Sentinels caches what errtrace knows about package-level variables
	// for the run. If nil, each analysis makes its own.
	Sentinels *Sentinels
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
// which become Error Origins and Wraps. The return that follows a
// recovered panic is left out if no deferred call of fn can recover (see
// mayRecover). A Concrete Type built from a
// composite literal whose type has a modelled Unwrap method becomes a Wrap
// of the value stored in the unwrapped field. A value under guards that
// hold on its return path (err == io.EOF is false there, errors.Is
// returned true, ...) becomes a Guarded value, which the summaries filter
// once its symbolic values are known. A function without a body has no
// facts.
func Analyze(tbl *errset.Table, fn *ssa.Function, opts Options) *LocalFacts {
	lf := &LocalFacts{Fn: fn}
	for _, idx := range ErrorResults(fn.Signature) {
		lf.Results = append(lf.Results, ResultFacts{Index: idx})
	}
	if len(lf.Results) == 0 || fn.Blocks == nil {
		return lf
	}
	a := &analyzer{tbl: tbl, fn: fn, opts: opts}
	recovers := fn.Recover != nil && mayRecover(fn)
	for _, b := range fn.Blocks {
		ret, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return)
		if !ok {
			continue
		}
		if b == fn.Recover && !recovers {
			// The return after a recovered panic, in a function whose
			// deferred calls never recover: it does not run.
			continue
		}
		for i := range lf.Results {
			r := &lf.Results[i]
			a.at, a.pt = ret, point{b: ret.Block()}
			a.seen = make(map[seenKey]bool)
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
	a := &analyzer{tbl: tbl, fn: fn, at: at, pt: pointOf(at), seen: make(map[seenKey]bool), opts: opts}
	a.walk(v)
	return a.facts
}

// analyzer walks value definitions for one operand of the instruction at
// (a return operand or a call argument).
type analyzer struct {
	tbl   *errset.Table
	fn    *ssa.Function
	at    ssa.Instruction
	seen  map[seenKey]bool
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
	seenCut map[seenKey]bool
	// revisits counts the walks of a non-nil value that was already
	// walked: its facts were added before, so a walk that adds nothing
	// does not show the value is nil.
	revisits int
	// captured, if set, is a captured local variable that only its
	// creator writes (see StoredFacts): a load of it stands for its
	// stores, except those whose values are being walked (in active,
	// shared with child analyzers), which cuts err = wrap(err) cycles.
	captured *ssa.Alloc
	active   map[*ssa.Store]bool
	// initStore, if set, is the store that gives a package-level variable
	// its value, whose stored composite literal the analyzer opens (see
	// sentinelWraps).
	initStore *ssa.Store

	// pt is the point where the walked value is used, which says which
	// guards hold for it (see guardsAt); gd are the function's guards,
	// shared with child analyzers.
	pt point
	gd *funcGuards
	// guards are the guards that hold for the value being walked, sorted
	// by id: the facts found are filtered by them (as Guarded values).
	guards []guardUse
}

// seenKey is a value walked at a point under a set of guards: the same
// value reached on another path, or with other guards, gives other facts.
type seenKey struct {
	v      ssa.Value
	pt     point
	guards string
}

// pointOf returns the point at the instruction instr: the end of its
// block (an If is the last instruction of a block, so the guards that
// hold there are those that hold at instr).
func pointOf(instr ssa.Instruction) point {
	if instr == nil {
		return point{}
	}
	return point{b: instr.Block()}
}

// LeafSource returns where leaf k of the fact's value enters the function.
func (f Fact) LeafSource(k int) any {
	if k < len(f.Leaves) {
		return f.Leaves[k]
	}
	return f.Source
}

func (a *analyzer) add(id errset.ValueID, src any) {
	a.addFact(Fact{Value: id, Source: src})
}

// addFact adds f, marked as cut if the analyzer is and under the guards
// that hold for the walked value, unless it is there already.
func (a *analyzer) addFact(f Fact) {
	f.cut = a.cut
	f.Value = a.guarded(f.Value)
	for _, g := range a.facts {
		if g.Value == f.Value && g.Source == f.Source && g.cut == f.cut && slices.Equal(g.Leaves, f.Leaves) {
			return
		}
	}
	a.facts = append(a.facts, f)
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
	saved := a.guards
	defer func() { a.guards = saved }()
	a.matchGuards(v)
	k := seenKey{v, a.pt, a.guardsKey()}
	if a.seen[k] {
		if !isNilConst(v) {
			a.revisits++
		}
		return
	}
	a.seen[k] = true
	switch v := v.(type) {
	case *ssa.Const:
		if v.IsNil() {
			if _, ptr := v.Type().Underlying().(*types.Pointer); ptr && IsErrorType(v.Type()) && a.at != nil {
				// A nil pointer of a concrete error type (return nil
				// in a function returning *E): no error as it is, but
				// a non-nil error once a caller converts it to an
				// interface. A nil check of the pointer on the way
				// removes it (see GuardNil).
				var site any = a.at
				if ref := a.debugRefAtUse(v); ref != nil {
					site = ref
				}
				a.add(a.tbl.Intern(errset.Value{Kind: errset.Concrete, Site: site, Pos: a.at.Pos(), Type: unalias(v.Type()), TypedNil: true, Index: a.failingCompanions(v)}), site)
			}
			return // no error
		}
	case *ssa.Phi:
		// Each edge's value is used on its edge, where the guards of the
		// predecessor hold.
		pt := a.pt
		for i, e := range v.Edges {
			a.pt = point{b: v.Block().Preds[i], edgeTo: v.Block()}
			a.walk(e)
		}
		a.pt = pt
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
		if ta, ok := v.Tuple.(*ssa.TypeAssert); ok && v.Index == 0 {
			// v, ok := x.(T): x where the assertion holds, else nil: no
			// error for an interface T, and for a concrete T a nil
			// pointer, which is a non-nil error once it is converted
			// (by a caller, for a result of type T).
			a.typeAssert(ta)
			if !types.IsInterface(ta.AssertedType) {
				a.failedAssert(ta, v, v)
			}
			return
		}
		a.memory(v, extractDetail(v))
		return
	case *ssa.TypeAssert:
		if !v.CommaOk {
			a.typeAssert(v)
			return
		}
	case *ssa.Parameter:
		a.param(v)
		return
	case *ssa.FreeVar:
		a.freeVar(v, v)
		return
	case *ssa.Field:
		a.fieldLoad(v, v.X, v.Field)
		return
	case *ssa.FieldAddr:
		// The address of a struct inside another (o.W for a method of
		// the embedded W called on o): the struct that field holds.
		a.fieldLoad(v, v.X, v.Field)
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

// failingCompanions returns the results that the return statement the
// analyzer walks for gives a failing value together with its operand c, a
// nil pointer: an error that is never nil there, or the constant false
// (return nil, err; return nil, false), as a set with bit i for result
// i. A caller that has checked such a result (err == nil, ok) before it
// uses the pointer does not get this nil (see errset.GuardCompanion). The
// set is empty if c is not an operand of that return itself (it reached
// it through a Phi), or if the results are loaded from named results
// after the deferred calls, which may have changed them.
func (a *analyzer) failingCompanions(c *ssa.Const) int {
	ret, ok := a.at.(*ssa.Return)
	if !ok || a.pt != pointOf(ret) {
		return 0
	}
	at := -1
	for i, r := range ret.Results {
		if r == ssa.Value(c) {
			if at >= 0 {
				return 0
			}
			at = i
		}
	}
	if at < 0 {
		return 0
	}
	set := 0
	for j, r := range ret.Results {
		if j == at || j >= 30 {
			continue
		}
		switch {
		case types.IsInterface(r.Type()) && IsErrorType(r.Type()):
			if !a.mayBeNil(r, a.pt) {
				set |= 1 << j
			}
		case isFalseConst(r):
			set |= 1 << j
		}
	}
	return set
}

// isFalseConst reports whether v is the boolean constant false.
func isFalseConst(v ssa.Value) bool {
	c, ok := v.(*ssa.Const)
	if !ok || c.Value == nil || c.Value.Kind() != constant.Bool {
		return false
	}
	return !constant.BoolVal(c.Value)
}

// typeAssert follows the value of the type assertion ta, x.(T), to its
// operand: the result is x where x's dynamic type is (or implements) T,
// and otherwise nil (comma-ok) or a panic. The operand's values are walked
// under a guard saying the assertion holds, which removes the Sentinels,
// Concrete Types, Opaque Errors and Wraps whose dynamic type cannot be T
// (see typeVerdict), keeps Unresolved values and those errtrace cannot
// decide, and stays pending on symbolic values (Param, FreeVar, CallRef)
// until a caller or a callee's summary gives their values. A case of a
// type switch is such a comma-ok assertion; where its own If guard
// already holds for the operand (see guardsAt) no second guard is added.
func (a *analyzer) typeAssert(ta *ssa.TypeAssert) {
	saved := a.guards
	defer func() { a.guards = saved }()
	held := false
	if a.fn != nil && a.fn.Blocks != nil {
		for _, u := range a.funcGuards().guardsAt(a.pt) {
			held = held || (u.g.Kind == errset.GuardAssert && u.g.Site == any(ta) && u.g.Holds && u.raw == ta.X)
		}
	}
	if !held && a.fn != nil && a.fn.Blocks != nil {
		gd := a.funcGuards()
		id, ok := gd.asserts[ta]
		if !ok {
			// Negative ids keep these guards apart from those of Ifs.
			id = -1 - len(gd.asserts)
			gd.asserts[ta] = id
		}
		u := guardUse{raw: ta.X, operand: stripConv(ta.X), id: id, g: errset.Guard{
			Kind: errset.GuardAssert, Site: ta, Pos: ta.Pos(), Type: unalias(ta.AssertedType), Holds: true,
		}}
		i, found := slices.BinarySearchFunc(a.guards, u.id, func(x guardUse, id int) int { return x.id - id })
		if !found {
			a.guards = slices.Insert(slices.Clip(a.guards), i, u)
		}
	}
	a.walk(ta.X)
}

// failedAssert adds what the comma-ok type assertion ta to a concrete
// type gives where it fails: the zero value of the type, which for a
// pointer type is a typed nil, a non-nil error once converted to an
// interface. Nothing is added where the assertion cannot fail: on a path
// its ok result guards, or when its operand is a conversion of a value of
// that very type. site is where the value is made and src where it enters
// the function.
func (a *analyzer) failedAssert(ta *ssa.TypeAssert, site ssa.Value, src any) {
	if mi, ok := stripConv(ta.X).(*ssa.MakeInterface); ok && types.Identical(mi.X.Type(), ta.AssertedType) {
		return
	}
	if a.fn != nil && a.fn.Blocks != nil {
		for _, u := range a.funcGuards().guardsAt(a.pt) {
			if u.g.Kind == errset.GuardAssert && u.g.Site == any(ta) && u.g.Holds {
				return
			}
		}
	}
	// The zero value of a non-pointer type is a value like any other of
	// that type.
	_, ptr := ta.AssertedType.Underlying().(*types.Pointer)
	a.add(a.concrete(ta.AssertedType, site, ptr), src)
}

// makeInterface classifies a conversion of a concrete value to an
// interface: a pointer Sentinel, a typed nil, or a Concrete Type.
func (a *analyzer) makeInterface(mi *ssa.MakeInterface) {
	// The guards on the concrete value (p != nil) hold for what it is
	// converted to.
	a.matchGuards(mi.X)
	switch x := mi.X.(type) {
	case *ssa.UnOp:
		if g, ok := x.X.(*ssa.Global); ok && x.Op == token.MUL {
			a.addSentinel(g, x.Type(), x)
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
		if ta, ok := x.Tuple.(*ssa.TypeAssert); ok && x.Index == 0 && types.IsInterface(ta.X.Type()) {
			// e, ok := err.(*E); return e: the values of err that are
			// of type *E, with what they wrap. Where the assertion
			// fails, e is a nil *E, which the conversion makes a
			// non-nil error.
			a.typeAssert(ta)
			a.failedAssert(ta, mi, mi)
			return
		}
	case *ssa.TypeAssert:
		if !x.CommaOk && types.IsInterface(x.X.Type()) {
			a.typeAssert(x)
			return
		}
	}
	// A value got from elsewhere (a parameter, a load, a Phi, an
	// errors.As target): not a tracked one.
	a.add(a.concrete(mi.X.Type(), mi, false), mi)
	a.untracked(errset.Value{Site: mi, Pos: a.posOf(mi), Type: unalias(mi.X.Type())}, mi)
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
		a.addSentinel(x, typ, v)
	case *ssa.FieldAddr:
		a.fieldLoad(v, x.X, x.Field)
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

// fieldLoad records the load v of field number field of base, a struct
// or a pointer to one. A field of an unnamed struct type, of a type
// parameter, or whose type depends on a type parameter of its generic
// struct is Unresolved. Otherwise:
//
//   - if base is itself an error value (its type implements error: the
//     receiver of an Unwrap method, a wrapper got from a type assertion),
//     the load becomes a FieldOf of each value base can carry. The
//     summaries resolve it once that value is known: to the inner values
//     of the very Wrap it loads from, where there is one;
//   - any other load becomes a FieldRef, which the summaries resolve from
//     every store to that field in the program.
func (a *analyzer) fieldLoad(v ssa.Value, base ssa.Value, field int) {
	// A field that holds a struct by value (v is that struct, or its
	// address) has no field location of its own: its fields are stored
	// one by one, to their own locations. Such a load is only followed
	// as a FieldOf of an error value, from which the summaries build the
	// struct (see Solver.fieldOf).
	_, byValue := v.(*ssa.FieldAddr)
	if _, isStruct := v.Type().Underlying().(*types.Struct); isStruct {
		byValue = true
	}
	a.fieldValue(v, base, field, byValue)
}

// FieldFacts returns the local facts of field number field of the struct
// that base points to, a value of fn used by the instruction at: what a
// load of that field there would give (a FieldOf of base's values, or a
// FieldRef; see fieldLoad). site is the value the facts are placed at.
// The field must not hold a struct by value.
func FieldFacts(tbl *errset.Table, fn *ssa.Function, site, base ssa.Value, field int, at ssa.Instruction, opts Options) []Fact {
	a := &analyzer{tbl: tbl, fn: fn, at: at, pt: pointOf(at), seen: make(map[seenKey]bool), opts: opts}
	a.fieldValue(site, base, field, false)
	return a.facts
}

// fieldValue is fieldLoad for the load v, which gives a struct held by
// value if byValue is set.
func (a *analyzer) fieldValue(v ssa.Value, base ssa.Value, field int, byValue bool) {
	recv := base.Type()
	detail := fmt.Sprintf("load of field %s of %s", fieldName(recv, field), typeString(recv))
	st := FieldStruct(recv, field)
	if st == nil {
		a.memory(v, detail)
		return
	}
	val := errset.Value{
		Kind:   errset.FieldRef,
		Site:   v,
		Pos:    a.posOf(v),
		Type:   st,
		Index:  field,
		Name:   fieldName(recv, field),
		Fn:     a.fn.String(),
		Detail: detail,
	}
	if byValue && (a.flat || !IsErrorType(recv)) {
		a.memory(v, detail)
		return
	}
	if a.flat || !IsErrorType(recv) {
		a.add(a.tbl.Intern(val), v)
		return
	}
	// The local copy that a method with a value receiver loads its
	// receiver's fields through is the receiver.
	if alloc, ok := base.(*ssa.Alloc); ok {
		if p := copiedParam(alloc); p != nil {
			base = p
		}
	}
	// Walk base as the value it is, where the load is made.
	c := &analyzer{tbl: a.tbl, fn: a.fn, at: a.at, pt: a.pt, gd: a.gd, seen: make(map[seenKey]bool), opts: a.opts, depth: a.depth, captured: a.captured, active: a.active}
	if instr, ok := v.(ssa.Instruction); ok && instr.Block() != nil {
		c.pt = pointOf(instr)
	}
	c.walk(base)
	for _, f := range c.facts {
		if a.tbl.Get(f.Value).Kind == errset.Unresolved {
			// The base is a place the analysis could not see through
			// here (a variable that errors.As filled through its
			// address): it is some value of the struct type, whose field
			// is what the program stores to that field.
			if byValue {
				a.memory(v, detail)
			} else {
				a.add(a.tbl.Intern(val), v)
			}
			continue
		}
		of := val
		of.Kind = errset.FieldOf
		of.Inner = []errset.ValueID{f.Value}
		leaves := make([]any, a.tbl.Leaves(f.Value))
		for k := range leaves {
			leaves[k] = f.LeafSource(k)
		}
		a.addFact(Fact{Value: a.tbl.Intern(of), Source: v, Leaves: leaves})
	}
}

// FieldStruct returns the named struct type that recv (a struct or a
// pointer to one) is, if a load of its field number field is modelled as
// a FieldRef: the type is a named struct type, and the field's type does
// not depend on a type parameter of the type. Otherwise it returns nil.
func FieldStruct(recv types.Type, field int) types.Type {
	t := types.Unalias(recv)
	if p, ok := t.Underlying().(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	n, ok := t.(*types.Named)
	if !ok {
		return nil
	}
	st, ok := n.Underlying().(*types.Struct)
	if !ok || field >= st.NumFields() {
		return nil
	}
	orig, ok := n.Origin().Underlying().(*types.Struct)
	if !ok || field >= orig.NumFields() || !types.Identical(orig.Field(field).Type(), st.Field(field).Type()) {
		return nil
	}
	return n
}

// local follows a load of a local variable that was not lifted to a
// register (a named result in a function with defers, a variable a closure
// captures) to the values stored into it: the stores of the analysed
// function are walked where they are, and a store made by a closure that
// captured the variable becomes a StoreRef, which the summaries resolve in
// the closure. If the variable can be written in a way errtrace does not
// follow to a store (its address is passed to a call, stored, converted
// ...), an Unresolved value is added to the values found. A composite
// literal of a concrete error type is classified as such.
func (a *analyzer) local(load *ssa.UnOp, alloc *ssa.Alloc) {
	if alloc.Comment == "complit" {
		a.constructed(load.Type(), alloc)
		return
	}
	pt := a.pt
	defer func() { a.pt = pt }()
	w := variableWriters(alloc)
	for _, st := range a.reaching(load, alloc, w.stores) {
		switch {
		case st.Parent() != a.fn:
			a.pt = pt
			a.storeRef(st, alloc, a.located(load, alloc))
		case alloc != a.captured:
			// A stored value is used at its store.
			a.pt = pointOf(st)
			a.walk(st.Val)
		case !a.active[st]:
			a.active[st] = true
			a.pt = pointOf(st)
			a.walk(st.Val)
			delete(a.active, st)
		}
	}
	if w.escapes {
		a.pt = pt
		a.memory(load, escapedVariable(alloc))
	}
}

// reaching returns the stores to the variable alloc, of stores, whose
// value the load may read. A store of the analysed function is read only
// if it can run before the load; if the function stores to the variable
// in the load's own block before the load, the last such store has
// overwritten what the function stored earlier. A store made by a closure
// may run at any time, unless the closure is only ever deferred by the
// function: it then runs when the function returns, and is read only by
// a load that can follow a point where the deferred calls run. So the
// value that "return fmt.Errorf("f: %w", err)" stores to a named result
// is not read by the load of err it wraps, and the Wrap is not nested in
// itself. A load after a recovered panic (in the function's recover
// block) may read any store.
func (a *analyzer) reaching(load *ssa.UnOp, alloc *ssa.Alloc, stores []*ssa.Store) []*ssa.Store {
	lb := load.Block()
	if lb == nil || a.fn == nil || load.Parent() != a.fn || afterRecover(a.fn, lb) {
		return stores
	}
	var last *ssa.Store
	for _, instr := range lb.Instrs {
		if instr == ssa.Instruction(load) {
			break
		}
		if st, ok := instr.(*ssa.Store); ok && st.Addr == ssa.Value(alloc) {
			last = st
		}
	}
	exit, exitKnown := false, false
	var out []*ssa.Store
	for _, st := range stores {
		switch {
		case st.Parent() == a.fn:
			if st == last || (last == nil && mayFollow(st, load)) {
				out = append(out, st)
			}
		case onlyDeferred(a.fn, st.Parent()):
			if !exitKnown {
				exit, exitKnown = followsRunDefers(a.fn, load), true
			}
			if exit {
				out = append(out, st)
			}
		default:
			out = append(out, st)
		}
	}
	return out
}

// afterRecover reports whether block b of fn runs after a recovered
// panic: it is fn's recover block, or reachable from it.
func afterRecover(fn *ssa.Function, b *ssa.BasicBlock) bool {
	if fn.Recover == nil {
		return false
	}
	seen := make(map[*ssa.BasicBlock]bool)
	work := []*ssa.BasicBlock{fn.Recover}
	for len(work) > 0 {
		c := work[len(work)-1]
		work = work[:len(work)-1]
		if c == b {
			return true
		}
		if !seen[c] {
			seen[c] = true
			work = append(work, c.Succs...)
		}
	}
	return false
}

// onlyDeferred reports whether the closure c (or the closure of fn that
// c is nested in) is made by fn only to be deferred there: every
// MakeClosure of it in fn is the callee of a defer statement and is used
// in no other way. Its body, and the closures made in it, then run only
// while fn's deferred calls run.
func onlyDeferred(fn, c *ssa.Function) bool {
	for c != nil && c.Parent() != fn {
		c = c.Parent()
	}
	if c == nil {
		return false
	}
	found := false
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			mc, ok := instr.(*ssa.MakeClosure)
			if !ok || mc.Fn != ssa.Value(c) {
				continue
			}
			found = true
			for _, r := range *mc.Referrers() {
				switch r := r.(type) {
				case *ssa.DebugRef:
				case *ssa.Defer:
					if r.Call.Value != ssa.Value(mc) || slices.Contains(r.Call.Args, ssa.Value(mc)) {
						return false
					}
				default:
					return false
				}
			}
		}
	}
	return found
}

// followsRunDefers reports whether the instruction at of fn can run after
// a point where fn's deferred calls run.
func followsRunDefers(fn *ssa.Function, at ssa.Instruction) bool {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if rd, ok := instr.(*ssa.RunDefers); ok && mayFollow(rd, at) {
				return true
			}
		}
	}
	return false
}

// mayRecover reports whether a deferred call of fn may recover from a
// panic, after which fn returns through its recover block: unless every
// defer statement of fn calls a function or closure with a body that does
// not call recover itself (recover has no effect when a function called
// by the deferred function calls it).
func mayRecover(fn *ssa.Function) bool {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			d, ok := instr.(*ssa.Defer)
			if !ok {
				continue
			}
			callee := d.Call.StaticCallee()
			if callee == nil || callee.Blocks == nil || callsRecover(callee) {
				return true
			}
		}
	}
	return false
}

// callsRecover reports whether fn calls the builtin recover.
func callsRecover(fn *ssa.Function) bool {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if c, ok := instr.(ssa.CallInstruction); ok {
				if bi, ok := c.Common().Value.(*ssa.Builtin); ok && bi.Name() == "recover" {
					return true
				}
			}
		}
	}
	return false
}

// located returns the load v of the variable alloc as the place a value
// enters the function, or, if v has no position (the load of a named
// result that a return makes after the deferred calls), the instruction
// the analyzer walks for, or else (the return after a recovered panic)
// the variable.
func (a *analyzer) located(v ssa.Value, alloc *ssa.Alloc) any {
	switch {
	case v.Pos().IsValid():
		return v
	case a.at != nil && a.at.Pos().IsValid():
		return a.at
	case alloc.Pos().IsValid():
		return alloc
	}
	return v
}

// escapedVariable is the detail of the Unresolved value of a local
// variable that may be written other than by the stores errtrace found.
func escapedVariable(alloc *ssa.Alloc) string {
	return "variable " + alloc.Comment + " may be written through an escaped pointer"
}

// storeRef records the StoreRef of st, a store that a closure makes to
// the captured variable alloc, loaded at src.
func (a *analyzer) storeRef(st *ssa.Store, alloc *ssa.Alloc, src any) {
	a.add(a.tbl.Intern(errset.Value{
		Kind:  errset.StoreRef,
		Site:  st,
		Pos:   st.Pos(),
		Fn:    st.Parent().String(),
		Index: instrNumber(st),
		Name:  alloc.Comment,
	}), src)
}

// instrNumber returns the number of instr among the instructions of its
// function, in block order.
func instrNumber(instr ssa.Instruction) int {
	n := 0
	for _, b := range instr.Parent().Blocks {
		if b != instr.Block() {
			n += len(b.Instrs)
			continue
		}
		for _, i := range b.Instrs {
			if i == instr {
				return n
			}
			n++
		}
	}
	return n
}

// varWriters are the ways a local variable is written.
type varWriters struct {
	// stores are the Store instructions that write the variable: those of
	// the function that declares it and those of every closure that
	// captured it by reference (nested closures included), in the order
	// of the variable's referrers.
	stores []*ssa.Store
	// escapes reports that the variable's address is used other than to
	// load it, to store to it or to bind it into a closure: it may then be
	// written by code errtrace does not attribute to it.
	escapes bool
}

// variableWriters finds the writers of the local variable alloc. A
// closure's body is always known, so however the closure itself is used
// (called, deferred, run as a goroutine, passed on or stored), its writes
// to the variable are its Stores through its FreeVar.
func variableWriters(alloc *ssa.Alloc) varWriters {
	var w varWriters
	w.collect(alloc, make(map[ssa.Value]bool))
	return w
}

func (w *varWriters) collect(addr ssa.Value, seen map[ssa.Value]bool) {
	if seen[addr] {
		return
	}
	seen[addr] = true
	refs := addr.Referrers()
	if refs == nil {
		return
	}
	for _, r := range *refs {
		switch r := r.(type) {
		case *ssa.Store:
			if r.Addr != addr || r.Val == addr {
				w.escapes = true
				continue
			}
			w.stores = append(w.stores, r)
		case *ssa.UnOp:
			if r.Op != token.MUL {
				w.escapes = true
			}
		case *ssa.DebugRef:
		case *ssa.MakeClosure:
			fn, ok := r.Fn.(*ssa.Function)
			if !ok {
				w.escapes = true
				continue
			}
			for i, b := range r.Bindings {
				if b != addr {
					continue
				}
				if i >= len(fn.FreeVars) {
					w.escapes = true
					continue
				}
				w.collect(fn.FreeVars[i], seen)
			}
		default:
			w.escapes = true
		}
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
	// Fn tells apart the CallRefs of the instances of a generic
	// function, whose calls share their positions.
	a.add(a.tbl.Intern(errset.Value{
		Kind:   errset.CallRef,
		Site:   call,
		Pos:    a.posOf(call),
		Detail: CalleeName(common),
		Index:  index,
		Fn:     a.fn.String(),
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

// sentinels returns the analyzer's Sentinels, making one if its options
// have none.
func (a *analyzer) sentinels() *Sentinels {
	if a.opts.Sentinels == nil {
		a.opts.Sentinels = NewSentinels()
	}
	return a.opts.Sentinels
}

// addSentinel adds the Sentinel of the variable g, loaded at src with the
// dynamic type typ (nil if the load does not say), and what its value
// wraps (see sentinelWraps).
func (a *analyzer) addSentinel(g *ssa.Global, typ types.Type, src ssa.Value) {
	id := a.sentinel(g, typ)
	a.add(id, src)
	a.sentinelWraps(g, a.tbl.Get(id), src)
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
	inf := a.sentinels().Info(g)
	if typ == nil {
		typ = inf.Type
	}
	return a.tbl.Intern(errset.Value{Kind: errset.Sentinel, Site: obj, Pos: obj.Pos(), Var: obj, Type: typ, Canon: canonVar(inf, obj), Fresh: inf.Fresh})
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
