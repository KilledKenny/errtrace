package flow

import (
	"fmt"
	"go/constant"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
	"github.com/killedKenny/errtrace/internal/fmtverb"
)

// This file models the calls and values that make Wraps: fmt.Errorf with
// %w verbs, errors.Join, braces.dev/errtrace.Wrap and composite literals of
// types with a custom Unwrap method.

// ErrorfFacts returns the values of a call to fmt.Errorf (or of a call to a
// function that forwards its format and operands to fmt.Errorf) at the
// instruction at in fn, with the given format and variadic operands slice:
// a Wrap of the %w operands, an Opaque Error when nothing is wrapped, a
// Wrap holding an Unresolved dynamic-format value when the format is not a
// constant, or an ErrorfLike value of fn when format and operands are fn's
// own parameters. site and pos are the site and position of the values
// made. Values are interned in tbl.
func ErrorfFacts(tbl *errset.Table, fn *ssa.Function, at ssa.Instruction, site any, pos token.Pos, format, args ssa.Value, opts Options) []Fact {
	a := &analyzer{tbl: tbl, fn: fn, at: at, pt: pointOf(at), seen: make(map[seenKey]bool), opts: opts}
	a.errorf(site, pos, format, args)
	return a.facts
}

// VarargsOperands returns the operands packed into the variadic slice v of
// a call (a slice of an array allocated for the call's "..." parameter),
// by index, with conversions to the element interface type removed. An
// operand that is never stored is nil. A nil slice has no operands. It
// reports false if v is not such a slice, as for a forwarded args...
// slice.
func VarargsOperands(v ssa.Value) ([]ssa.Value, bool) {
	switch v := v.(type) {
	case *ssa.Const:
		return nil, v.IsNil()
	case *ssa.Slice:
		if alloc, ok := v.X.(*ssa.Alloc); ok && alloc.Comment == "varargs" && v.Low == nil && v.High == nil {
			return arrayStores(alloc)
		}
	}
	return nil, false
}

// arrayStores returns the values stored at each constant index of the
// array allocated by alloc, with ChangeInterface conversions removed. It
// reports false if the array is used in another way or an element is
// stored more than once.
func arrayStores(alloc *ssa.Alloc) ([]ssa.Value, bool) {
	ptr, ok := alloc.Type().Underlying().(*types.Pointer)
	if !ok {
		return nil, false
	}
	arr, ok := ptr.Elem().Underlying().(*types.Array)
	if !ok {
		return nil, false
	}
	out := make([]ssa.Value, arr.Len())
	for _, r := range *alloc.Referrers() {
		switch r := r.(type) {
		case *ssa.IndexAddr:
			c, ok := r.Index.(*ssa.Const)
			if !ok {
				return nil, false
			}
			i, exact := constant.Int64Val(constant.ToInt(c.Value))
			if !exact || i < 0 || i >= int64(len(out)) {
				return nil, false
			}
			for _, rr := range *r.Referrers() {
				st, ok := rr.(*ssa.Store)
				if !ok || st.Addr != r {
					continue
				}
				if out[i] != nil {
					return nil, false
				}
				val := st.Val
				if ci, ok := val.(*ssa.ChangeInterface); ok {
					val = ci.X
				}
				out[i] = val
			}
		case *ssa.Slice, *ssa.DebugRef:
		default:
			return nil, false
		}
	}
	return out, true
}

// MayCarryError reports whether the operand v can hold an error: its type
// is an error type or another interface, or it converts a type that
// implements error to an interface. A value of type T whose pointer type
// alone implements error does not: fmt.Errorf's a[i].(error) fails on it.
func MayCarryError(v ssa.Value) bool {
	if mi, ok := v.(*ssa.MakeInterface); ok {
		return types.Implements(mi.X.Type(), errorIface)
	}
	return IsErrorType(v.Type()) || types.IsInterface(v.Type())
}

// wrapped is one value a Wrap can hold as an inner value, with the place
// where each of its leaves enters the function (errset.Flatten's leaf
// order).
type wrapped struct {
	id     errset.ValueID
	leaves []any
}

// madeAt returns ids as inner values whose leaves all enter the function
// at src.
func (a *analyzer) madeAt(src any, ids ...errset.ValueID) []wrapped {
	out := make([]wrapped, len(ids))
	for i, id := range ids {
		leaves := make([]any, a.tbl.Leaves(id))
		for k := range leaves {
			leaves[k] = src
		}
		out[i] = wrapped{id: id, leaves: leaves}
	}
	return out
}

// operand returns the values v can carry as the inner value of a Wrap made
// by this analyzer at site, each with where its leaves enter the function
// and under the guards that hold for v at site. Leaves
// that lie below more Wraps than the Wrap Chain depth limit allows are
// returned under Truncated markers; a chain of exactly the limit is kept
// whole, as errset.Table.Cap does. In flat mode (at the limit) the
// operand's leaves are added to the analyzer's own facts, marked as cut,
// and operand returns nothing.
func (a *analyzer) operand(v ssa.Value, site any) []wrapped {
	pt := a.sitePoint(site)
	if a.flat {
		a.inner(pt, func() { a.walk(v) })
		return nil
	}
	c := &analyzer{tbl: a.tbl, fn: a.fn, at: a.at, pt: pt, gd: a.gd, seen: make(map[seenKey]bool), opts: a.opts, depth: a.depth + 1, captured: a.captured, active: a.active}
	c.flat = c.depth >= a.opts.chainDepth()
	c.walk(v)
	var out []wrapped
	seen := make(map[errset.ValueID]bool)
	for _, f := range c.facts {
		id := f.Value
		leaves := make([]any, a.tbl.Leaves(id))
		for k := range leaves {
			leaves[k] = f.LeafSource(k)
		}
		if f.cut {
			l := a.tbl.Get(id)
			id = a.tbl.Intern(errset.Value{Kind: errset.Truncated, Site: l.Site, Pos: l.Pos, Inner: []errset.ValueID{id}})
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, wrapped{id: id, leaves: leaves})
		}
	}
	return out
}

// sitePoint returns the point of the instruction site, or the analyzer's
// own point.
func (a *analyzer) sitePoint(site any) point {
	if instr, ok := site.(ssa.Instruction); ok && instr.Block() != nil {
		return pointOf(instr)
	}
	return a.pt
}

// inner runs walk, in flat mode, for the inner values of a Wrap used at
// pt: the leaves it adds are cut. The guards of the Wrap are not guards of
// its inner values, so walk starts without them; the guards that hold at
// pt for an inner value apply to it, as they do above the depth limit.
// Each value is walked at most once inside and once outside of Wraps,
// which keeps wrapping loops finite.
func (a *analyzer) inner(pt point, walk func()) {
	seen, cut, guards, savedPt := a.seen, a.cut, a.guards, a.pt
	defer func() { a.seen, a.cut, a.guards, a.pt = seen, cut, guards, savedPt }()
	if !a.cut {
		if a.seenCut == nil {
			a.seenCut = make(map[seenKey]bool)
		}
		a.seen, a.cut = a.seenCut, true
	}
	a.guards, a.pt = nil, pt
	walk()
}

// addInner adds, in flat mode, leaves that are the inner values of a Wrap.
func (a *analyzer) addInner(ids []errset.ValueID, src any) {
	a.inner(a.pt, func() { a.addAll(ids, src) })
}

// wraps returns one Wrap like base for each combination of the values of
// operands, one value per operand, at most maxCombos, as facts made at
// base.Site whose leaves enter the function where the operands' leaves
// do. When combinations are dropped an unresolved(fixpoint-limit) value is
// returned with them, so the Error Set does not claim to be complete.
// Operands without values (always nil) are left out; if none is left there
// is no Wrap.
func (a *analyzer) wraps(base errset.Value, operands [][]wrapped) []Fact {
	type combo struct {
		inner  []errset.ValueID
		leaves []any
	}
	combos := []combo{{}}
	dropped := false
	for _, op := range operands {
		if len(op) == 0 {
			continue
		}
		var next []combo
		for _, c := range combos {
			for _, w := range op {
				if len(next) == maxCombos {
					dropped = true
					break
				}
				next = append(next, combo{
					inner:  append(c.inner[:len(c.inner):len(c.inner)], w.id),
					leaves: append(c.leaves[:len(c.leaves):len(c.leaves)], w.leaves...),
				})
			}
		}
		combos = next
	}
	if len(combos) == 1 && len(combos[0].inner) == 0 {
		return nil
	}
	out := make([]Fact, len(combos), len(combos)+1)
	for i, c := range combos {
		w := base
		w.Kind = errset.Wrap
		w.Inner = c.inner
		out[i] = Fact{Value: a.tbl.Intern(w), Source: base.Site, Leaves: c.leaves}
	}
	if dropped {
		out = append(out, Fact{Value: a.unresolvedAt(base.Site, base.Pos, errset.FixpointLimit,
			fmt.Sprintf("more than %d combinations of wrapped values", maxCombos)), Source: base.Site})
	}
	return out
}

func (a *analyzer) addFacts(facts []Fact) {
	for _, f := range facts {
		a.addFact(f)
	}
}

func (a *analyzer) addAll(ids []errset.ValueID, src any) {
	for _, id := range ids {
		a.add(id, src)
	}
}

func (a *analyzer) unresolvedAt(site any, pos token.Pos, kind errset.UnresolvedKind, detail string) errset.ValueID {
	return a.tbl.Intern(errset.Value{Kind: errset.Unresolved, Site: site, Pos: pos, Unresolved: kind, Detail: detail})
}

// errorf models fmt.Errorf(format, args...) made at site, following
// fmt/errors.go: no %w gives an Opaque Error, one or more %w a Wrap of the
// wrapped operands that can hold errors (sorted by operand index, without
// duplicates). %v of an error does not wrap it.
func (a *analyzer) errorf(site any, pos token.Pos, format, args ssa.Value) {
	if c, ok := format.(*ssa.Const); ok && c.Value != nil && c.Value.Kind() == constant.String {
		a.constErrorf(site, pos, constant.StringVal(c.Value), args)
		return
	}
	if fp, ok := format.(*ssa.Parameter); ok {
		if ap, ok := args.(*ssa.Parameter); ok {
			fi, ai := paramIndex(a.fn, fp), paramIndex(a.fn, ap)
			if fi >= 0 && ai >= 0 {
				a.add(a.tbl.Intern(errset.Value{
					Kind: errset.ErrorfLike, Site: site, Pos: pos,
					Fn: a.fn.String(), Index: fi, ArgIndex: ai, Name: fp.Name(),
				}), site)
				return
			}
		}
	}
	u := a.unresolvedAt(site, pos, errset.DynamicFormat, "format of fmt.Errorf is not a constant")
	if a.flat {
		a.addInner([]errset.ValueID{u}, site)
	}
	operands := [][]wrapped{a.madeAt(site, u)}
	ops, ok := VarargsOperands(args)
	if !ok {
		nf := a.notFollowed(site, pos, "operands of fmt.Errorf")
		if a.flat {
			a.addInner(nf, site)
		}
		operands = append(operands, a.madeAt(site, nf...))
	}
	mayNil := false
	for _, op := range ops {
		if op != nil && MayCarryError(op) {
			operands = append(operands, a.operand(op, site))
			mayNil = mayNil || a.mayBeNil(op, a.sitePoint(site))
		}
	}
	if !a.flat {
		a.addFacts(a.wraps(errset.Value{Site: site, Pos: pos, WrapKind: errset.WrapErrorf, MayHoldNil: mayNil}, operands))
	}
}

func (a *analyzer) constErrorf(site any, pos token.Pos, format string, args ssa.Value) {
	opaque := a.tbl.Intern(errset.Value{
		Kind: errset.Opaque, Site: site, Pos: pos,
		Constructor: "fmt.Errorf", Message: format, HasMessage: true,
	})
	ops, ok := VarargsOperands(args)
	if !ok {
		if len(fmtverb.Wrapped(fmtverb.Parse(format, -1))) == 0 {
			a.add(opaque, site)
			return
		}
		if a.flat {
			a.addInner(a.notFollowed(site, pos, "operands of fmt.Errorf"), site)
			return
		}
		a.addFacts(a.wraps(errset.Value{Site: site, Pos: pos, WrapKind: errset.WrapErrorf, Format: format},
			[][]wrapped{a.madeAt(site, a.notFollowed(site, pos, "operands of fmt.Errorf")...)}))
		return
	}
	verbs := fmtverb.Wrapped(fmtverb.Parse(format, len(ops)))
	if len(verbs) == 0 {
		a.add(opaque, site)
		return
	}
	var operands [][]wrapped
	mayNil := false
	for _, i := range verbs {
		if op := ops[i]; op != nil && MayCarryError(op) && !isNilConst(op) {
			operands = append(operands, a.operand(op, site))
			// fmt.Errorf drops a nil %w operand: the Wrap then holds
			// nothing for it.
			mayNil = mayNil || a.mayBeNil(op, a.sitePoint(site))
		}
	}
	if a.flat {
		if len(operands) == 0 {
			// As below: nothing is wrapped.
			a.add(opaque, site)
		}
		return
	}
	facts := a.wraps(errset.Value{Site: site, Pos: pos, WrapKind: errset.WrapErrorf, Format: format, MayHoldNil: mayNil}, operands)
	if len(facts) == 0 {
		// Every %w operand is nil or not an error: the result wraps
		// nothing and is as opaque as errors.New.
		a.add(opaque, site)
		return
	}
	a.addFacts(facts)
}

func isNilConst(v ssa.Value) bool {
	c, ok := v.(*ssa.Const)
	return ok && c.IsNil()
}

// notFollowed is the inner value of a Wrap whose operands are passed in a
// slice that cannot be followed.
func (a *analyzer) notFollowed(site any, pos token.Pos, what string) []errset.ValueID {
	return []errset.ValueID{a.unresolvedAt(site, pos, errset.Memory, what+" passed in a slice that is not followed")}
}

// join models errors.Join(errs...): a Wrap of the non-nil operands, or no
// value at all (nil) when every operand is nil.
func (a *analyzer) join(call *ssa.Call, args ssa.Value) {
	pos := a.posOf(call)
	base := errset.Value{Site: call, Pos: pos, WrapKind: errset.WrapJoin}
	ops, ok := VarargsOperands(args)
	if !ok {
		if a.flat {
			a.addInner(a.notFollowed(call, pos, "operands of errors.Join"), call)
			return
		}
		a.addFacts(a.wraps(base, [][]wrapped{a.madeAt(call, a.notFollowed(call, pos, "operands of errors.Join")...)}))
		return
	}
	var operands [][]wrapped
	n, mayNil := 0, false
	for _, op := range ops {
		if op != nil {
			operands = append(operands, a.operand(op, call))
		}
		if op != nil && !isNilConst(op) {
			n++
			mayNil = mayNil || a.mayBeNil(op, pointOf(call))
		}
	}
	// errors.Join drops nil operands. With one operand left that may be
	// nil, the result is nil or holds it; with more, the Wrap may hold
	// only some of them.
	base.MayHoldNil = n > 1 && mayNil
	if !a.flat {
		a.addFacts(a.wraps(base, operands))
	}
}

// bracesWrap models braces.dev/errtrace.Wrap(err): a Wrap of err, or nil.
func (a *analyzer) bracesWrap(call *ssa.Call, err ssa.Value) {
	vals := a.operand(err, call)
	if !a.flat {
		a.addFacts(a.wraps(errset.Value{Site: call, Pos: a.posOf(call), WrapKind: errset.WrapBraces}, [][]wrapped{vals}))
	}
}

// constructed records a value of type typ built by alloc (a composite
// literal or new(T)): a Concrete Type Origin, or, for a composite literal
// of a type with a custom Unwrap method, a Wrap of what Unwrap returns.
func (a *analyzer) constructed(typ types.Type, alloc *ssa.Alloc) {
	typ = unalias(typ)
	conc := a.tbl.Intern(errset.Value{Kind: errset.Concrete, Site: alloc, Pos: a.posOf(alloc), Type: typ, Allocated: true})
	if alloc.Comment != "complit" {
		a.add(conc, alloc)
		return
	}
	m := unwrapMethod(a.fn.Prog, typ)
	if m.kind == unwrapNone {
		a.add(conc, alloc)
		return
	}
	pos := a.posOf(alloc)
	base := errset.Value{Site: alloc, Pos: pos, WrapKind: errset.WrapUnwrapMethod, Type: unalias(typ)}
	if m.kind == unwrapOther {
		u := a.unresolvedAt(alloc, pos, errset.Memory, typeString(unalias(typ))+".Unwrap not modelled")
		if a.flat {
			a.addInner([]errset.ValueID{u}, alloc)
			return
		}
		a.addFacts(a.wraps(base, [][]wrapped{a.madeAt(alloc, u)}))
		return
	}
	before, revisits := len(a.facts), a.revisits
	var operands [][]wrapped
	stores := fieldStores(alloc, m.field)
	// The field is nil on a path with no store to it. With more than one
	// store, the slice of Unwrap() []error is one of them, not all.
	base.MayHoldNil = !a.storedOnEveryPath(stores) || (m.multi && len(stores) > 1)
	// A pointer to the value, or to the field, that escapes (to a call,
	// a closure, memory) may be used to write the field: it may then hold
	// nil or a value errtrace does not track.
	var escaped []wrapped
	if fieldMayBeWritten(alloc, m.field) {
		base.MayHoldNil = true
		u := a.unresolvedAt(alloc, pos, errset.Memory, "field of "+typeString(unalias(typ))+" may be written through an escaped pointer")
		if a.flat {
			a.addInner([]errset.ValueID{u}, alloc)
		}
		escaped = a.madeAt(alloc, u)
	}
	if !m.multi {
		var vals []wrapped
		for _, st := range stores {
			vals = append(vals, a.operand(st.Val, alloc)...)
			base.MayHoldNil = base.MayHoldNil || a.mayBeNil(st.Val, pointOf(st))
		}
		operands = append(operands, append(vals, escaped...))
	} else {
		if escaped != nil {
			operands = append(operands, escaped)
		}
		for _, st := range stores {
			if elems, ok := sliceElems(st.Val); ok {
				for _, e := range elems {
					if e != nil {
						operands = append(operands, a.operand(e, alloc))
						base.MayHoldNil = base.MayHoldNil || a.mayBeNil(e, pointOf(st))
					}
				}
				continue
			}
			u := a.unresolvedAt(alloc, pos, errset.Memory, "slice returned by "+typeString(unalias(typ))+".Unwrap not followed")
			if a.flat {
				a.addInner([]errset.ValueID{u}, alloc)
			}
			operands = append(operands, a.madeAt(alloc, u))
		}
	}
	if a.flat {
		// Unwrap returns nil only when the walk found nothing and did not
		// stop at values already walked (as in a wrapping loop).
		if len(a.facts) == before && a.revisits == revisits {
			a.add(conc, alloc)
		}
		return
	}
	facts := a.wraps(base, operands)
	if len(facts) == 0 {
		// Unwrap returns nil: the value wraps nothing.
		a.add(conc, alloc)
		return
	}
	a.addFacts(facts)
}

// fieldMayBeWritten reports whether field f of the struct that alloc
// allocates may be written other than by a direct store to its address:
// alloc, or the field's address, is used other than to load it, to make
// an interface of it or return it (the error value, directly or through
// a ChangeType), or by a DebugRef. A call (a method like w.clear() or a helper taking w), a
// store of the pointer, a closure capture or a Phi lets the pointer
// escape; a store to *alloc overwrites the whole struct.
func fieldMayBeWritten(alloc *ssa.Alloc, f int) bool {
	for _, r := range *alloc.Referrers() {
		switch r := r.(type) {
		case *ssa.FieldAddr:
			if r.Field != f {
				continue
			}
			for _, rr := range *r.Referrers() {
				switch rr := rr.(type) {
				case *ssa.Store:
					if rr.Addr != r || rr.Val == r {
						return true
					}
				case *ssa.UnOp:
					if rr.Op != token.MUL {
						return true
					}
				case *ssa.DebugRef:
				default:
					return true
				}
			}
		case *ssa.ChangeType:
			for _, rr := range *r.Referrers() {
				switch rr.(type) {
				case *ssa.MakeInterface, *ssa.Return, *ssa.DebugRef:
				default:
					return true
				}
			}
		case *ssa.MakeInterface, *ssa.Return, *ssa.DebugRef:
		case *ssa.UnOp:
			if r.Op != token.MUL {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// fieldStores returns the stores to field f of the struct that alloc
// allocates.
func fieldStores(alloc *ssa.Alloc, f int) []*ssa.Store {
	var out []*ssa.Store
	for _, r := range *alloc.Referrers() {
		fa, ok := r.(*ssa.FieldAddr)
		if !ok || fa.X != alloc || fa.Field != f {
			continue
		}
		for _, rr := range *fa.Referrers() {
			if st, ok := rr.(*ssa.Store); ok && st.Addr == fa {
				out = append(out, st)
			}
		}
	}
	return out
}

// storedOnEveryPath reports whether one of stores is made on every path
// to the analyzer's point: its block dominates the point's, and, in the
// block of the instruction the analyzer walks for, comes before it.
func (a *analyzer) storedOnEveryPath(stores []*ssa.Store) bool {
	b := a.pt.b
	if b == nil {
		return false
	}
	for _, st := range stores {
		sb := st.Block()
		if sb == nil || !sb.Dominates(b) {
			continue
		}
		if sb == b && a.pt.edgeTo == nil && a.at != nil && a.at.Block() == b && !before(st, a.at) {
			continue
		}
		return true
	}
	return false
}

// before reports whether instruction x comes before y in their block.
func before(x, y ssa.Instruction) bool {
	for _, instr := range x.Block().Instrs {
		switch instr {
		case x:
			return true
		case y:
			return false
		}
	}
	return false
}

// mayBeNil reports whether v may be nil when it is used at pt, unless
// errtrace can prove it is not: nil itself, a Phi with an edge that may be
// nil, a call that is not an intrinsic which never returns nil, a
// parameter, a load other than of a Sentinel that is never nil, and any
// other value errtrace does not model. A check that holds at pt can
// prove v non-nil (err != nil, errors.Is(err, io.EOF); see provesNonNil).
// A conversion to an interface (a typed nil
// included), a new allocation, errors.New and fmt.Errorf are never nil.
func (a *analyzer) mayBeNil(v ssa.Value, pt point) bool {
	return a.nilWalk(v, pt, make(map[nilKey]bool), 0)
}

type nilKey struct {
	v  ssa.Value
	pt point
}

func (a *analyzer) nilWalk(v ssa.Value, pt point, seen map[nilKey]bool, depth int) bool {
	if depth > 4*maxDynDepth {
		return true
	}
	k := nilKey{v, pt}
	if seen[k] {
		// A cycle (of Phis or Sentinels) adds no value of its own.
		return false
	}
	seen[k] = true
	if a.fn != nil && a.fn.Blocks != nil && v.Parent() == a.fn && a.funcGuards().isNonNil(pt, v) {
		return false
	}
	switch v := v.(type) {
	case *ssa.Const:
		return v.IsNil()
	case *ssa.Phi:
		for i, e := range v.Edges {
			if a.nilWalk(e, point{b: v.Block().Preds[i], edgeTo: v.Block()}, seen, depth+1) {
				return true
			}
		}
		return false
	case *ssa.ChangeInterface:
		return a.nilWalk(v.X, pt, seen, depth+1)
	case *ssa.ChangeType:
		return a.nilWalk(v.X, pt, seen, depth+1)
	case *ssa.MakeInterface, *ssa.Alloc:
		return false
	case *ssa.UnOp:
		if g, ok := v.X.(*ssa.Global); ok && v.Op == token.MUL {
			return a.globalMayBeNil(g, seen, depth+1)
		}
	case *ssa.Call:
		common := v.Common()
		name, ok := Intrinsic(common.StaticCallee())
		if !ok {
			return true
		}
		switch name {
		case "errors.New", "fmt.Errorf":
			return false
		case "errtrace.Wrap":
			return a.nilWalk(common.Args[0], pointOf(v), seen, depth+1)
		case "errors.Join":
			ops, ok := VarargsOperands(common.Args[0])
			if !ok {
				return true
			}
			for _, op := range ops {
				if op != nil && !a.nilWalk(op, pointOf(v), seen, depth+1) {
					return false
				}
			}
			return true
		}
	}
	return true
}

// globalMayBeNil reports whether the package-level variable g may hold
// nil: unless its value is a Fresh pointer, or every store of its
// initialization gives a value that is never nil, and it is not stored to
// elsewhere.
func (a *analyzer) globalMayBeNil(g *ssa.Global, seen map[nilKey]bool, depth int) bool {
	s := a.sentinels()
	inf := s.Info(g)
	switch {
	case inf.Unstable:
		return true
	case inf.Fresh:
		return false
	}
	canon := inf.Canon
	if canon == nil {
		canon = g
	}
	stores := s.initStores[canon]
	if len(stores) == 0 {
		return true
	}
	for _, st := range stores {
		if a.nilWalk(st.Val, pointOf(st), seen, depth) {
			return true
		}
	}
	return false
}

// sliceElems returns the elements of a slice literal ([]error{a, b}).
func sliceElems(v ssa.Value) ([]ssa.Value, bool) {
	if s, ok := v.(*ssa.Slice); ok && s.Low == nil && s.High == nil {
		if alloc, ok := s.X.(*ssa.Alloc); ok {
			return arrayStores(alloc)
		}
	}
	return nil, false
}

type unwrapKind uint8

const (
	// unwrapNone: the type has no Unwrap() error or Unwrap() []error.
	unwrapNone unwrapKind = iota
	// unwrapField: Unwrap returns a field of its receiver.
	unwrapField
	// unwrapOther: Unwrap has a shape that is not modelled.
	unwrapOther
)

type unwrapShape struct {
	kind  unwrapKind
	field int
	// multi is set for Unwrap() []error.
	multi bool
}

// unwrapMethod describes the Unwrap method of typ, as errors.Is and
// errors.As find it: whether there is one, and whether it returns a field
// of its receiver.
func unwrapMethod(prog *ssa.Program, typ types.Type) unwrapShape {
	if prog == nil {
		return unwrapShape{}
	}
	sel := prog.MethodSets.MethodSet(typ).Lookup(nil, "Unwrap")
	if sel == nil {
		return unwrapShape{}
	}
	sig, ok := sel.Type().(*types.Signature)
	if !ok || sig.Params().Len() != 0 || sig.Results().Len() != 1 {
		return unwrapShape{}
	}
	shape := unwrapShape{kind: unwrapOther}
	switch rt := sig.Results().At(0).Type(); {
	case types.Identical(rt, types.Universe.Lookup("error").Type()):
	case isErrorSlice(rt):
		shape.multi = true
	default:
		return unwrapShape{}
	}
	obj, ok := sel.Obj().(*types.Func)
	if !ok {
		return shape
	}
	m := prog.FuncValue(obj)
	if m == nil || m.Blocks == nil || len(m.Params) == 0 {
		return shape
	}
	field := -1
	for _, b := range m.Blocks {
		ret, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return)
		if !ok {
			continue
		}
		f, ok := receiverField(m.Params[0], ret.Results[0])
		if !ok || (field >= 0 && f != field) {
			return shape
		}
		field = f
	}
	if field < 0 {
		return shape
	}
	shape.kind, shape.field = unwrapField, field
	return shape
}

func isErrorSlice(t types.Type) bool {
	s, ok := t.Underlying().(*types.Slice)
	return ok && types.Identical(s.Elem(), types.Universe.Lookup("error").Type())
}

// receiverField reports whether v loads a field of the receiver recv, and
// which: *(&recv.f) for a pointer receiver, recv.f or a load through a
// local copy of recv for a value receiver.
func receiverField(recv *ssa.Parameter, v ssa.Value) (int, bool) {
	switch v := v.(type) {
	case *ssa.Field:
		if v.X == recv {
			return v.Field, true
		}
	case *ssa.UnOp:
		fa, ok := v.X.(*ssa.FieldAddr)
		if !ok || v.Op != token.MUL {
			return 0, false
		}
		if fa.X == recv {
			return fa.Field, true
		}
		if alloc, ok := fa.X.(*ssa.Alloc); ok && copiesOf(alloc, recv) {
			return fa.Field, true
		}
	}
	return 0, false
}

// copiesOf reports whether alloc is a local variable that only ever holds
// recv (the copy a value receiver gets when its fields are addressed).
func copiesOf(alloc *ssa.Alloc, recv *ssa.Parameter) bool {
	stored := false
	for _, r := range *alloc.Referrers() {
		if st, ok := r.(*ssa.Store); ok && st.Addr == alloc {
			if st.Val != recv {
				return false
			}
			stored = true
		}
	}
	return stored
}

// paramIndex returns the index of p among fn's parameters, or -1.
func paramIndex(fn *ssa.Function, p *ssa.Parameter) int {
	for i, q := range fn.Params {
		if q == p {
			return i
		}
	}
	return -1
}
