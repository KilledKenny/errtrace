package mark

import (
	"go/types"
	"sort"

	"golang.org/x/tools/go/ssa"
)

// markHeld marks every memory location in which a value of the composite
// type t can hold a value that carries a Mark: the elements of its
// slices, arrays, maps and channels, what its pointers point to, and its
// struct fields, through every type it leads to. It is what the pass
// knows about a composite that comes from where it cannot see: outside
// the program, or out of reflection. The locations are marked in the held
// plane: what is loaded from one of an interface type may be such a
// composite again.
func (a *analysis) markHeld(t types.Type, why reason) {
	a.markHeldIn(t, freeH, why, make(map[types.Type]bool), 0)
}

// maxHeldDepth bounds the walk of markHeld through unnamed types; named
// types are walked once each.
const maxHeldDepth = 32

func (a *analysis) markHeldIn(t types.Type, planes tags, why reason, seen map[types.Type]bool, depth int) {
	if t == nil || depth > maxHeldDepth || seen[t] {
		return
	}
	if _, ok := types.Unalias(t).(*types.Named); ok {
		seen[t] = true
	}
	elem := func(e types.Type, l loc) {
		if a.markable(e) {
			a.markLoc(l, planes, why)
		}
		a.markHeldIn(e, planes, why, seen, depth+1)
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		elem(u.Elem(), a.typedLoc(u.Elem()))
	case *types.Slice:
		elem(u.Elem(), a.typedLoc(u.Elem()))
	case *types.Array:
		elem(u.Elem(), a.typedLoc(u.Elem()))
	case *types.Map:
		elem(u.Key(), a.mapLoc(locMapKey, t))
		elem(u.Elem(), a.mapLoc(locMapVal, t))
	case *types.Chan:
		elem(u.Elem(), a.chanLoc(u.Elem()))
	case *types.Struct:
		for i := range u.NumFields() {
			ft := u.Field(i).Type()
			if a.markable(ft) {
				// The field, and the memory of its type if its address
				// is taken anywhere.
				locs, n := a.fieldLocs(u, i)
				for _, l := range locs[:n] {
					a.markLoc(l, planes, why)
				}
			}
			a.markHeldIn(ft, planes, why, seen, depth+1)
		}
	}
}

// reach is what reaches found: the planes in which memory that a value
// of a type leads to is marked, and one such location (or none, if the
// Mark is that of a type).
type reach struct {
	planes tags
	by     ref
}

// reaches returns the planes in which a value of the composite type t
// holds a marked value anywhere in the memory it leads to: the locations
// markHeld would mark. A value of a type that is marked as such (the
// Subject's type, a wrapper) counts as by type.
func (a *analysis) reaches(t types.Type) reach {
	var r reach
	a.reachesIn(t, &r, make(map[types.Type]bool), 0)
	return r
}

func (a *analysis) reachesIn(t types.Type, r *reach, seen map[types.Type]bool, depth int) {
	if t == nil || seen[t] {
		return
	}
	if depth > maxHeldDepth {
		// Deeper is not looked at: it may hold anything.
		r.planes |= freeT | freeH
		return
	}
	if _, ok := types.Unalias(t).(*types.Named); ok {
		// A named type is walked once; the bound is on the nesting of
		// unnamed types.
		seen[t] = true
		depth = 0
	}
	at := func(e types.Type, locs ...loc) {
		if a.implicit(e) {
			r.planes |= freeT
		}
		if a.markable(e) {
			for _, l := range locs {
				if li := a.locs[l]; li != nil && li.marked != 0 {
					if r.planes == 0 {
						r.by = ref{kind: refLoc, l: l}
					}
					r.planes |= li.marked
				}
			}
		}
		a.reachesIn(e, r, seen, depth+1)
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		at(u.Elem(), a.typedLoc(u.Elem()))
	case *types.Slice:
		at(u.Elem(), a.typedLoc(u.Elem()))
	case *types.Array:
		at(u.Elem(), a.typedLoc(u.Elem()))
	case *types.Map:
		at(u.Key(), a.mapLoc(locMapKey, t))
		at(u.Elem(), a.mapLoc(locMapVal, t))
	case *types.Chan:
		at(u.Elem(), a.chanLoc(u.Elem()))
	case *types.Struct:
		for i := range u.NumFields() {
			locs, n := a.fieldLocs(u, i)
			at(u.Field(i).Type(), locs[:n]...)
		}
	}
}

// Reflection.
//
// A reflect.Value carries the Mark of what it holds, and a call of
// package reflect gives back what it is passed (see modelReflect). Beyond
// that:
//
//   - a store through reflection (Value.Set, SetMapIndex, Send, and
//     reflect.Append, AppendSlice, Copy) of a marked value marks the
//     memory of the composite the destination Value was made from
//     (reflect.ValueOf(&s).Elem().Field(0)), or the Value of the fresh
//     memory it names (reflect.New(t).Elem()); if the pass cannot tell
//     where it writes, the store is recorded and not followed (see
//     reflectDest);
//   - if the program calls functions through reflection (Value.Call,
//     CallSlice, Method, MethodByName), any exported method and any
//     function whose value is taken may be the callee: their marked
//     results mark the memory of reflect.Value (where the results of Call
//     are read), marked reflect.Values in memory reach their parameters,
//     and their panics surface at every such call.

// reflectStore reports whether fn is a function or method of package
// reflect that stores its later operands into the memory behind its
// first.
func reflectStore(fn *ssa.Function) bool {
	if fn == nil || fn.Pkg == nil || fn.Pkg.Pkg.Path() != "reflect" {
		return false
	}
	if fn.Signature.Recv() != nil {
		switch fn.Name() {
		case "Set", "SetMapIndex", "Send", "TrySend", "SetIterKey", "SetIterValue":
			return true
		}
		return false
	}
	switch fn.Name() {
	case "Append", "AppendSlice", "Copy":
		return true
	}
	return false
}

// reflectRoot returns the static type of the composite that the
// reflect.Value v was made from by reflect.ValueOf and the calls that walk
// into a value (Elem, Field, Index, ...), or nil if the pass cannot tell.
func (a *analysis) reflectRoot(v ssa.Value) types.Type {
	for range 16 {
		call, ok := v.(*ssa.Call)
		if !ok {
			return nil
		}
		sc := call.Call.StaticCallee()
		if a.model(sc) != modelReflect || len(call.Call.Args) == 0 {
			return nil
		}
		if sc.Signature.Recv() == nil {
			switch sc.Name() {
			case "ValueOf":
				mi, ok := call.Call.Args[0].(*ssa.MakeInterface)
				if !ok || isInterface(mi.X.Type()) {
					return nil
				}
				return mi.X.Type()
			case "Indirect", "Append", "AppendSlice":
			default:
				return nil
			}
		} else if !isReflectValue(sc.Signature.Recv().Type()) {
			return nil
		}
		v = call.Call.Args[0]
	}
	return nil
}

// reflectReferent reports whether the store through reflection fn writes
// what its destination Value refers to (the elements of a slice, map or
// channel) and not the Value's own slot.
func reflectReferent(fn *ssa.Function) bool {
	switch fn.Name() {
	case "Set", "SetIterKey", "SetIterValue":
		return false
	}
	return true
}

// reflectStored handles the marked operand of the store through
// reflection at call (see reflectStore), whose destination is dst: it
// marks the memory the destination names where the pass can tell (see
// reflectDest), and records the store where it cannot.
func (a *analysis) reflectStored(call ssa.CallInstruction, dst ssa.Value, planes tags, why reason) {
	d := a.reflectDest(dst, reflectReferent(call.Common().StaticCallee()))
	for _, t := range d.known {
		a.markHeld(t, why)
	}
	for _, f := range d.fresh {
		// The Value of the fresh memory holds it from now on, and so
		// does what is read back from that Value through reflection.
		why := reason{kind: kReflectFresh, from: why.from, site: why.site, fn: f.call.Call.StaticCallee()}
		if deref := a.soleDeref(f.call); f.whole && deref != nil {
			// The variable is the stored value.
			a.mark(key{deref, 0}, toT(planes), why)
			continue
		}
		// The memory is a composite with the stored value inside. What
		// reads it back through its type (v.Interface().(*T)) is
		// followed only where the Value stays in sight.
		a.mark(key{f.call, 0}, toT(planes), why)
		if !a.typedReads(f.call, why) {
			a.unfollowed[call] = true
		}
	}
	if d.unknown {
		a.unfollowed[call] = true
	}
}

// reflectTarget reports whether a call through reflection may run g: the
// program makes such calls, and g is an exported method or a function
// whose value is taken.
func (a *analysis) reflectTarget(g *ssa.Function) bool {
	if !a.reflects {
		return false
	}
	if b, ok := a.reflectTargets[g]; ok {
		return b
	}
	obj := g.Object()
	method := g.Signature.Recv() != nil && obj != nil && obj.Exported()
	b := false
	switch {
	case !a.taken[g] && !method:
	case a.reflectAny:
		b = true
	default:
		// Only what the reflect.Values that are called were made from.
		for _, root := range a.reflectRoots {
			if method {
				rt := g.Signature.Recv().Type()
				if types.Identical(rt, root) || types.Identical(rt, types.NewPointer(root)) || types.Identical(types.NewPointer(rt), root) {
					b = true
				}
			}
			if sig, ok := root.Underlying().(*types.Signature); ok && a.taken[g] && types.Identical(sig, g.Signature) {
				b = true
			}
		}
	}
	a.reflectTargets[g] = b
	return b
}

// reflectArgsMarked seeds the parameters of every function that a call
// through reflection may run: a marked reflect.Value is in memory, and
// may be an argument of Value.Call.
func (a *analysis) reflectArgsMarked(planes tags, from ref) {
	planes = toT(planes)
	if a.reflectSeeded&planes == planes {
		return
	}
	a.reflectSeeded |= planes
	for _, fn := range a.fns {
		if !a.reflectTarget(fn) {
			continue
		}
		for _, p := range fn.Params {
			a.mark(key{p, 0}, planes, reason{kind: kReflectArg, from: from, fn: fn})
		}
	}
}

// reflectReturned marks the memory of reflect.Value when a function that
// a call through reflection may run returns a marked value: the results
// of Value.Call are read from it.
func (a *analysis) reflectReturned(g *ssa.Function, r int, planes tags) {
	if planes == 0 || !a.reflectTarget(g) || a.reflectValues.kind == 0 {
		return
	}
	a.markLoc(a.reflectValues, toT(planes), reason{kind: kReflectRet, from: ref{kind: refRet, fn: g, idx: int32(r), free: true}, fn: g})
}

// Open world.

// openFunc reports whether code outside the open program may call fn with
// arguments of its own: fn is a function or method declared in an open
// package that is exported, is a method (it may satisfy an interface the
// outside calls through), or has its value taken (a closure handed out,
// a callback).
func (a *analysis) openFunc(fn *ssa.Function) bool {
	if a.open == nil {
		return false
	}
	pkg := fn.Pkg
	for f := fn; pkg == nil && f != nil; f = f.Parent() {
		pkg = f.Pkg
	}
	if pkg == nil || !a.open[pkg] {
		return false
	}
	if a.taken[fn] || fn.Signature.Recv() != nil {
		return true
	}
	obj := fn.Object()
	return fn.Parent() == nil && obj != nil && obj.Exported()
}

// sentinelName reports whether a package-level variable name follows the
// convention for Sentinels (ErrNotFound, EOF): such a variable of an open
// package, if the program only initializes it, is taken not to be
// reassigned from outside. Without this convention every Sentinel of a
// library could hold every other.
func sentinelName(name string) bool {
	if len(name) >= 3 && (name[:3] == "Err" || name[:3] == "err") {
		return true
	}
	return name == "EOF"
}

// seedOpen makes the Marks of an open world (see Options.Open): what code
// outside the program may hand to it.
func (a *analysis) seedOpen() {
	if a.open == nil {
		return
	}
	for _, fn := range a.fns {
		if !a.openFunc(fn) {
			continue
		}
		for i, p := range fn.Params {
			why := reason{kind: kSeedCaller, fn: fn}
			if a.markable(p.Type()) {
				// As if a caller passed it: by type, or a composite
				// in an interface, in its context.
				a.mark(key{p, 0}, paramSeed(i, freeT|freeH), why)
			}
			// What a composite argument holds is in memory.
			a.markHeld(p.Type(), why)
		}
	}
	for _, k := range a.openLoads {
		why := reason{kind: kSeedOpen}
		if instr, ok := k.v.(ssa.Instruction); ok {
			why.site = instr
		}
		a.mark(k, freeT|freeH, why)
	}
	// What an exported variable of a composite type holds.
	var pkgs []*ssa.Package
	for p := range a.open {
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Pkg.Path() < pkgs[j].Pkg.Path() })
	for _, p := range pkgs {
		var names []string
		for name := range p.Members {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			g, ok := p.Members[name].(*ssa.Global)
			if !ok || !a.openAddr(g) {
				continue
			}
			if elem := pointerElem(g.Type()); elem != nil {
				a.markHeld(elem, reason{kind: kSeedOpen})
			}
		}
	}
}

// outsideResults marks the results of the dynamic call at site, whose
// function value or receiver is the marked value with the tags t, in an
// open world: every value from outside is marked, so the code that runs
// may be outside the program, and return the Subject.
func (a *analysis) outsideResults(call *ssa.Call, t tags, from ref) {
	t = toT(t) | toH(t)
	why := reason{kind: kOutside, from: from, site: call}
	res := call.Call.Signature().Results()
	for r := range res.Len() {
		rt := res.At(r).Type()
		if a.markable(rt) {
			a.markResult(call, r, t, why)
		}
		a.markHeld(rt, why)
	}
	// It may also write through what it is passed.
	for _, arg := range call.Call.Args {
		a.markHeld(arg.Type(), why)
	}
}

// rootedInReflectCall reports whether the reflect.Value v, the receiver of
// a call through reflection, is itself the result of Value.Method or
// Value.MethodByName on a Value whose root is known: the root is then
// recorded at that call, and v adds nothing.
func (a *analysis) rootedInReflectCall(v ssa.Value) bool {
	call, ok := v.(*ssa.Call)
	if !ok {
		return false
	}
	sc := call.Call.StaticCallee()
	if sc == nil || !reflectCall(sc) || len(call.Call.Args) == 0 {
		return false
	}
	return a.reflectRoot(call.Call.Args[0]) != nil
}
