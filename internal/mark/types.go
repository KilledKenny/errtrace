package mark

import (
	"go/types"

	"golang.org/x/tools/go/types/typeutil"

	"github.com/KilledKenny/errtrace/internal/flow"
)

// typeIDs names types by small integers, equal for identical types, and
// lets two types be unified so that they share memory locations (the two
// sides of a conversion between types that differ only in struct tags or
// in name).
type typeIDs struct {
	m      typeutil.Map
	fast   map[types.Type]int32
	parent []int32
}

func newTypeIDs() *typeIDs {
	return &typeIDs{fast: make(map[types.Type]int32)}
}

func (t *typeIDs) raw(typ types.Type) int32 {
	if id, ok := t.fast[typ]; ok {
		return id
	}
	var id int32
	if v := t.m.At(typ); v != nil {
		id = v.(int32)
	} else {
		id = int32(len(t.parent))
		t.parent = append(t.parent, id)
		t.m.Set(typ, id)
	}
	t.fast[typ] = id
	return id
}

func (t *typeIDs) find(id int32) int32 {
	for t.parent[id] != id {
		t.parent[id] = t.parent[t.parent[id]]
		id = t.parent[id]
	}
	return id
}

// id returns the name of typ's class.
func (t *typeIDs) id(typ types.Type) int32 { return t.find(t.raw(typ)) }

func (t *typeIDs) union(a, b types.Type) {
	x, y := t.id(a), t.id(b)
	if x == y {
		return
	}
	if x > y {
		x, y = y, x
	}
	t.parent[y] = x
}

// unify makes the memory of a and b, the two sides of a conversion, the
// same locations, down through their element and field types.
func (t *typeIDs) unify(a, b types.Type, depth int) {
	if depth > 8 || types.Identical(a, b) {
		return
	}
	t.union(a, b)
	ua, ub := a.Underlying(), b.Underlying()
	if !types.Identical(ua, ub) {
		t.union(ua, ub)
	}
	switch ua := ua.(type) {
	case *types.Pointer:
		if ub, ok := ub.(*types.Pointer); ok {
			t.unify(ua.Elem(), ub.Elem(), depth+1)
		}
	case *types.Slice:
		switch ub := ub.(type) {
		case *types.Slice:
			t.unify(ua.Elem(), ub.Elem(), depth+1)
		case *types.Pointer:
			// A slice converted to an array pointer.
			if arr, ok := ub.Elem().Underlying().(*types.Array); ok {
				t.unify(ua.Elem(), arr.Elem(), depth+1)
			}
		case *types.Array:
			t.unify(ua.Elem(), ub.Elem(), depth+1)
		}
	case *types.Array:
		if ub, ok := ub.(*types.Array); ok {
			t.unify(ua.Elem(), ub.Elem(), depth+1)
		}
	case *types.Map:
		if ub, ok := ub.(*types.Map); ok {
			t.unify(ua.Key(), ub.Key(), depth+1)
			t.unify(ua.Elem(), ub.Elem(), depth+1)
		}
	case *types.Chan:
		if ub, ok := ub.(*types.Chan); ok {
			t.unify(ua.Elem(), ub.Elem(), depth+1)
		}
	case *types.Struct:
		if ub, ok := ub.(*types.Struct); ok && ua.NumFields() == ub.NumFields() {
			for i := range ua.NumFields() {
				t.unify(ua.Field(i).Type(), ub.Field(i).Type(), depth+1)
			}
		}
	}
}

// markable reports whether a value of static type t can carry a Mark: it
// is an interface (or a type parameter), or a concrete type that can be an
// error.
func (a *analysis) markable(t types.Type) bool {
	if t == nil {
		return false
	}
	if b, ok := a.markableCache[t]; ok {
		return b
	}
	var b bool
	switch t.(type) {
	case *types.Tuple, *types.Basic:
	default:
		if isReflectValue(t) || isReflectIter(t) {
			// A reflect.Value carries the Mark of what it holds, and
			// a *reflect.MapIter that of the map it ranges over.
			a.markableCache[t] = true
			return true
		}
		switch t.Underlying().(type) {
		case *types.Interface:
			b = true
		case *types.Signature:
			// In an open world a function value is marked when it may
			// come from outside: calling it may run code that is not
			// loaded.
			b = a.open != nil || types.Implements(t, errorIface)
		case *types.Slice, *types.Map, *types.Chan, *types.Array:
			// Only a named one can implement error.
			b = types.Implements(t, errorIface)
		default:
			b = flow.IsErrorType(t)
		}
	}
	a.markableCache[t] = b
	return b
}

// isReflectValue reports whether t is reflect.Value.
func isReflectValue(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Name() == "Value" && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "reflect"
}

// isReflectIter reports whether t is *reflect.MapIter.
func isReflectIter(t types.Type) bool {
	p, ok := types.Unalias(t).(*types.Pointer)
	if !ok {
		return false
	}
	n, ok := types.Unalias(p.Elem()).(*types.Named)
	return ok && n.Obj().Name() == "MapIter" && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "reflect"
}

var errorIface = types.Universe.Lookup("error").Type().Underlying().(*types.Interface)

func isInterface(t types.Type) bool {
	_, ok := t.Underlying().(*types.Interface)
	return ok
}

// potentialWrapper reports whether a value of the concrete type t could be
// a wrapper of some error: t can be an error and its method set has an
// Unwrap, Is or As method.
func (a *analysis) potentialWrapper(t types.Type) bool {
	if b, ok := a.wrapperCache[t]; ok {
		return b
	}
	b := false
	if !isInterface(t) && flow.IsErrorType(t) {
		ms := a.prog.MethodSets.MethodSet(t)
		for i := range ms.Len() {
			switch ms.At(i).Obj().Name() {
			case "Unwrap", "Is", "As":
				b = true
			}
		}
	}
	a.wrapperCache[t] = b
	return b
}

// typeSeed reports whether every value of static type t is marked from
// the start: t is the Concrete Type Subject, or a type whose values may
// be equal to the Sentinel Subject's value.
func (a *analysis) typeSeed(t types.Type) bool {
	if b, ok := a.typeSeedCache[t]; ok {
		return b
	}
	b := false
	switch {
	case isInterface(t):
	case a.subj.Type != nil:
		b = types.Identical(t, a.subj.Type)
	default:
		b = a.eqSeed(t)
	}
	a.typeSeedCache[t] = b
	return b
}

// eqSeed reports whether a value of the concrete type t may be equal to
// the Sentinel Subject's value without being a copy of it: t is
// comparable, is not a pointer to memory of non-zero size (two such
// pointers are equal only if they are the same pointer, or both nil), and
// is a type the Sentinel's value may have.
func (a *analysis) eqSeed(t types.Type) bool {
	if !types.Comparable(t) {
		return false
	}
	if a.dynUnknown && flow.IsErrorType(t) {
		// The value is not known: it may be a nil pointer of any type.
		return true
	}
	for _, d := range a.dynNilable {
		// Every nil pointer of the type is equal to a nil Sentinel.
		if types.Identical(t, d) {
			return true
		}
	}
	if p, ok := t.Underlying().(*types.Pointer); ok && !mayBeZeroSize(p.Elem(), 0) {
		return false
	}
	for _, d := range a.dynTypes {
		if types.Identical(t, d) {
			return true
		}
	}
	return false
}

// mayBeZeroSize reports whether a variable of type t may have size zero.
func mayBeZeroSize(t types.Type, depth int) bool {
	if depth > 8 {
		return true
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for i := range u.NumFields() {
			if !mayBeZeroSize(u.Field(i).Type(), depth+1) {
				return false
			}
		}
		return true
	case *types.Array:
		return u.Len() == 0 || mayBeZeroSize(u.Elem(), depth+1)
	case *types.Interface:
		_, isParam := t.(*types.TypeParam)
		return isParam
	}
	return false
}

// implicit reports whether every value of static type t is marked now:
// t is a type seed or a known wrapper of the Subject.
func (a *analysis) implicit(t types.Type) bool {
	if t == nil || isInterface(t) {
		return false
	}
	if _, ok := t.(*types.Tuple); ok {
		return false
	}
	if a.typeSeed(t) {
		return true
	}
	if !a.potentialWrapper(t) {
		return false
	}
	if c := a.candByID[a.ty.id(t)]; c != nil {
		return c.wrapper
	}
	return false
}

// implicitQuery is implicit for a query after the pass: it also decides a
// possible wrapper type that no instruction of the program converts to an
// interface.
func (a *analysis) implicitQuery(t types.Type) bool {
	if a.implicit(t) {
		return true
	}
	if t == nil || isInterface(t) || !a.potentialWrapper(t) {
		return false
	}
	if _, ok := t.(*types.Tuple); ok {
		return false
	}
	return a.isWrapper(t)
}

// assertable reports whether a marked interface value can have the dynamic
// type t, the type of a type assertion or of an errors.As target.
func (a *analysis) assertable(t types.Type) bool {
	if isInterface(t) {
		return true
	}
	if b, ok := a.assertCache[t]; ok {
		return b
	}
	b := false
	_, isFunc := t.Underlying().(*types.Signature)
	switch {
	case a.typeSeed(t), a.potentialWrapper(t), isFunc && a.open != nil:
		b = true
	case a.subj.Sentinel != nil && flow.IsErrorType(t):
		b = a.dynUnknown
		for _, d := range a.dynTypes {
			b = b || types.Identical(t, d)
		}
	}
	a.assertCache[t] = b
	return b
}

// isWrapper reports whether the possible wrapper type t is a wrapper of
// the Subject now: an Unwrap method of t can return a marked value, or t
// holds a marked value. When it does, heldBy is set to the marked result
// or location that makes it one, if there is one to name.
func (a *analysis) isWrapper(t types.Type) bool {
	ms := a.prog.MethodSets.MethodSet(t)
	// seen says that the pass scanned every Unwrap, Is and As method of
	// t: what they can report is then all that errors.Is and errors.As
	// can find in a value of t.
	seen := true
	for i := range ms.Len() {
		switch ms.At(i).Obj().Name() {
		case "Unwrap", "Is", "As":
			fn := a.prog.MethodValue(ms.At(i))
			if fn == nil {
				seen = false
			} else if _, scanned := a.fnIndex[fn]; !scanned {
				seen = false
			}
		}
	}
	for i := range ms.Len() {
		sel := ms.At(i)
		if sel.Obj().Name() != "Unwrap" {
			continue
		}
		fn := a.prog.MethodValue(sel)
		if fn == nil {
			continue
		}
		if _, scanned := a.fnIndex[fn]; !scanned {
			// No body, or a wrapper made after the scan: not known.
			a.heldBy = ref{}
			return true
		}
		res := fn.Signature.Results()
		for r := range res.Len() {
			if rs := a.rets[fn]; r < len(rs) && rs[r].t&anyFree != 0 {
				a.heldBy = ref{kind: refRet, fn: fn, idx: int32(r), free: true}
				return true
			}
			if rt := res.At(r).Type(); !a.markable(rt) && a.holds(rt, 0) {
				return true
			}
		}
	}
	// A custom Is (for a Sentinel) or As (for a Concrete Type) method
	// that may report the Subject.
	name := "As"
	if a.subj.Sentinel != nil {
		name = "Is"
	}
	for i := range ms.Len() {
		sel := ms.At(i)
		if sel.Obj().Name() != name {
			continue
		}
		if a.targetMatches(a.prog.MethodValue(sel)) {
			return true
		}
	}
	if seen {
		// A marked value in a field that no Unwrap returns and no Is or
		// As method reports (the Val of a ValueError{Val any; Err
		// error}) is not found in the value.
		return false
	}
	base := t
	if p, ok := t.Underlying().(*types.Pointer); ok {
		base = p.Elem()
	}
	return a.holds(base, 0)
}

// maxHoldDepth is how deep holds looks into structs nested by value.
const maxHoldDepth = 8

// holds reports whether a value of the composite type t holds a marked
// value in its own storage: in a field (through embedded structs), or as
// an element of a slice, array, map, channel or pointer that is t or a
// field of t.
func (a *analysis) holds(t types.Type, depth int) bool {
	if depth > maxHoldDepth {
		// Deeper is not looked at: it may hold anything.
		a.heldBy = ref{}
		return true
	}
	scalar := func(e types.Type, l loc) bool {
		if a.implicit(e) {
			a.heldBy = ref{}
			return true
		}
		if a.markable(e) && a.locMarked(l) {
			a.heldBy = ref{kind: refLoc, l: l}
			return true
		}
		return false
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for i := range u.NumFields() {
			ft := u.Field(i).Type()
			if a.implicit(ft) {
				a.heldBy = ref{}
				return true
			}
			if a.markable(ft) {
				locs, n := a.fieldLocs(u, i)
				for _, l := range locs[:n] {
					if a.locMarked(l) {
						a.heldBy = ref{kind: refLoc, l: l}
						return true
					}
				}
				continue
			}
			if a.holds(ft, depth+1) {
				return true
			}
		}
	case *types.Pointer:
		if _, ok := u.Elem().Underlying().(*types.Struct); ok {
			return depth == 0 && a.holds(u.Elem(), depth+1)
		}
		return scalar(u.Elem(), a.typedLoc(u.Elem()))
	case *types.Slice:
		return scalar(u.Elem(), a.typedLoc(u.Elem()))
	case *types.Array:
		return scalar(u.Elem(), a.typedLoc(u.Elem()))
	case *types.Map:
		return scalar(u.Key(), a.mapLoc(locMapKey, t)) || scalar(u.Elem(), a.mapLoc(locMapVal, t))
	case *types.Chan:
		return scalar(u.Elem(), a.chanLoc(u.Elem()))
	}
	return false
}

// holdsAny is holds for a value the pass was asked about and cannot mark:
// what a function value or an unsafe pointer leads to is not known, so it
// is reported as holding.
func (a *analysis) holdsAny(t types.Type) bool {
	switch u := t.Underlying().(type) {
	case *types.Signature:
		return true
	case *types.Basic:
		return u.Kind() == types.UnsafePointer
	}
	return a.holds(t, 0)
}
