package mark

import (
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// The destination of a store through reflection.
//
// A store through reflection (see reflectStore) writes into the memory
// behind a reflect.Value. The pass follows it only where it can tell which
// memory that is, by walking back from the destination Value:
//
//   - to reflect.ValueOf(x), with x a composite of a known static type:
//     the store marks the memory that type can hold (markHeld);
//   - to a call that makes fresh memory (reflect.New, MakeSlice, MakeMap,
//     MakeChan, Zero), as long as the Value still names that memory and not
//     something a pointer, slice or map stored in it leads to: the store
//     marks the reflect.Value the call returned, and with it everything
//     read back from that Value through reflection (Interface, Elem,
//     Field, ...). Where the only use of reflect.New(t) is one Elem and
//     the store fills that variable (reflect.New(t).Elem().Set(x)), the
//     Value of the variable is marked instead: it is the stored value, not
//     a composite around it (its Addr is one, see reflectBoxes). In any
//     other fresh memory the stored value sits inside a composite of a
//     type the pass does not know. A read of it through that type
//     (p.Interface().(*T).F) is followed where the Value goes through
//     calls of package reflect only (see typedReads); if the Value goes
//     anywhere else, the store is recorded as not followed;
//   - anywhere else (a Value out of memory, out of a dynamic call, behind
//     more helpers than the walk follows): the store is not followed. It is
//     recorded (Result.ReflectStores), and the Verdict says so.
//
// The walk goes through the calls of package reflect that derive one Value
// from another, phis, the results of static calls of scanned functions,
// the parameters of functions all of whose callers the pass sees, and
// local struct variables that only hold the Value in a field (the
// addressableValue of encoding/json).

// ReflectStore is a store through reflection of a value that may hold the
// Subject, that the pass did not follow, or not all the way: it could not
// tell the destination, or the destination is fresh memory that holds the
// value inside a composite and goes where the pass cannot see every read
// of it through its type.
type ReflectStore struct {
	// Site is the call of package reflect that stores.
	Site ssa.CallInstruction
	// Fn is the function the call is in.
	Fn *ssa.Function
	// FloodOnly says that only the flooding pass recorded the store
	// (see Result.Flood): the Subject does not reach it, but what it
	// stores may be what another store wrote.
	FloodOnly bool
}

// ReflectStores returns the stores through reflection that the Subject
// reaches and that the pass did not follow (all the way), anywhere in
// the scanned program, in the order of the scan (the same on every run).
// Whether they can matter to a Target is what the flooding pass tells
// (see Flood). On the Result of the flooding pass they come with the
// stores that only the flood reaches (FloodOnly), which are listed after
// the others, in the order of the scan too.
func (r *Result) ReflectStores() []ReflectStore {
	a := r.a
	sites := make([]ssa.CallInstruction, 0, len(r.unfollowed))
	for site := range r.unfollowed {
		sites = append(sites, site)
	}
	sort.Slice(sites, func(i, j int) bool {
		if oi, oj := a.siteOrd[sites[i]], a.siteOrd[sites[j]]; oi != oj {
			return oi < oj
		}
		return sites[i].Pos() < sites[j].Pos()
	})
	out := make([]ReflectStore, 0, len(sites))
	for _, site := range sites {
		if !r.floodOnly[site] {
			out = append(out, ReflectStore{Site: site, Fn: site.Parent()})
		}
	}
	for _, site := range sites {
		if r.floodOnly[site] {
			out = append(out, ReflectStore{Site: site, Fn: site.Parent(), FloodOnly: true})
		}
	}
	return out
}

// reflectDest is what the walk back from the destination of a store
// through reflection found.
type reflectDest struct {
	// known are the static types of the composites the Value was made
	// from by reflect.ValueOf.
	known []types.Type
	// fresh are the calls of package reflect that made the memory.
	fresh []freshDest
	// unknown says that a way to the destination was not followed.
	unknown bool
}

// freshDest is fresh memory that a destination names.
type freshDest struct {
	// call is the call of package reflect that made the memory.
	call *ssa.Call
	// whole says that the destination is all of the memory of
	// reflect.New(t), reached by one Elem or Indirect: the store fills
	// the variable, it does not put something inside it.
	whole bool
}

// reflectOp is a call of package reflect that derives a Value from
// another, by what it does to the memory the Value names.
type reflectOp byte

const (
	// opDeref follows a pointer: Value.Elem, reflect.Indirect.
	opDeref reflectOp = 'e'
	// opField stays inside the same memory: Value.Field.
	opField reflectOp = 'f'
	// opAddr takes the address of the memory: Value.Addr.
	opAddr reflectOp = 'a'
	// opIndex is Value.Index: an element of an array (the same memory)
	// or of a slice (what the slice points to).
	opIndex reflectOp = 'i'
	// opSlice is Value.Slice, Slice3, reflect.Append, AppendSlice: the
	// same elements, or a fresh array.
	opSlice reflectOp = 's'
	// opOther is any other method of reflect.Value that returns one
	// (MapIndex, FieldByName, Convert, ...): it may follow pointers that
	// were stored in the memory.
	opOther reflectOp = 'o'
)

// reflectDerive returns what the call of the function or method fn of
// package reflect, whose first operand is a reflect.Value and which
// returns one, does to it.
func reflectDerive(fn *ssa.Function) reflectOp {
	if fn.Signature.Recv() == nil {
		switch fn.Name() {
		case "Indirect":
			return opDeref
		case "Append", "AppendSlice":
			return opSlice
		}
		return 0
	}
	switch fn.Name() {
	case "Elem":
		return opDeref
	case "Field":
		return opField
	case "Addr":
		return opAddr
	case "Index":
		return opIndex
	case "Slice", "Slice3":
		return opSlice
	}
	return opOther
}

// reflectFresh reports whether fn is a function of package reflect that
// returns a Value of memory nothing else names yet.
func reflectFresh(fn *ssa.Function) bool {
	if fn.Signature.Recv() != nil {
		return false
	}
	switch fn.Name() {
	case "New", "Zero", "MakeSlice", "MakeMap", "MakeMapWithSize", "MakeChan":
		return true
	}
	return false
}

// freshNamed reports whether a Value derived from the fresh memory that
// the function named ctor made, by the calls ops (the first applied
// first), still names that memory, so that a store through it writes
// nowhere else. referent says that the store writes what the Value
// refers to (the elements of its slice, map or channel: SetMapIndex,
// Send, Copy, Append) and not the Value's own slot (Set).
func freshNamed(ctor string, ops []reflectOp, referent bool) bool {
	// What the Value is: a pointer to the fresh memory, a slot inside
	// it, or the fresh slice, map or channel itself.
	const (
		ptr = iota
		slot
		slice
		ref
	)
	var st int
	switch ctor {
	case "Zero":
		// A zero value holds only nil pointers, slices, maps and
		// channels, and is not addressable: every store through it
		// panics, blocks or writes nothing.
		return true
	case "New":
		st = ptr
	case "MakeSlice":
		st = slice
	default:
		st = ref
	}
	for _, op := range ops {
		switch {
		case st == ptr && op == opDeref:
			st = slot
		case st == slot && op == opField:
		case st == slot && op == opAddr:
			st = ptr
		case st == slice && op == opSlice:
		case st == slice && op == opIndex:
			st = slot
		default:
			// A pointer, slice or map stored in the memory may lead
			// anywhere.
			return false
		}
	}
	if referent {
		// A slot may hold a slice, map or channel from elsewhere.
		return st == slice || st == ref
	}
	return true
}

// Bounds of the walk back from a destination.
const (
	// maxDestCalls is how many static calls (into a helper's results,
	// out to a function's callers) one way of the walk goes through.
	maxDestCalls = 4
	// maxDestSteps is how many values one walk looks at.
	maxDestSteps = 512
	// maxDestOps is how many calls of package reflect lie between the
	// destination and where its memory was made.
	maxDestOps = 16
)

type destWalk struct {
	a        *analysis
	out      reflectDest
	referent bool
	seen     map[string]bool
	steps    int
}

// destState is one way of the walk: the calls of package reflect passed
// so far (the last applied first), and the fields of the struct values
// passed that the Value sits in (the innermost first).
type destState struct {
	ops    []reflectOp
	fields []int
	calls  int
	// boxed says that the walk follows the interface value passed to
	// reflect.ValueOf, not a reflect.Value.
	boxed bool
}

// key names the state of the walk at the value numbered id.
func (s destState) key(id uint32) string {
	var b strings.Builder
	b.WriteString(strconv.FormatUint(uint64(id), 16))
	b.WriteByte('|')
	for _, op := range s.ops {
		b.WriteByte(byte(op))
	}
	b.WriteByte('|')
	for _, f := range s.fields {
		b.WriteString(strconv.Itoa(f))
		b.WriteByte(',')
	}
	if s.boxed {
		b.WriteByte('b')
	}
	return b.String()
}

func (s destState) withOp(op reflectOp) destState {
	s.ops = append(append([]reflectOp(nil), s.ops...), op)
	return s
}

func (s destState) push(field int) destState {
	s.fields = append(append([]int(nil), s.fields...), field)
	return s
}

func (s destState) pop() destState {
	s.fields = s.fields[:len(s.fields)-1]
	return s
}

func (s destState) call() destState {
	s.calls++
	return s
}

// reflectDest walks back from dst, the destination Value of a store
// through reflection.
func (a *analysis) reflectDest(dst ssa.Value, referent bool) *reflectDest {
	k := destKey{dst, referent}
	if d, ok := a.dests[k]; ok {
		return d
	}
	w := &destWalk{a: a, referent: referent, seen: make(map[string]bool)}
	w.value(dst, destState{})
	a.dests[k] = &w.out
	return &w.out
}

type destKey struct {
	v        ssa.Value
	referent bool
}

// id numbers the values the walks look at, for the keys of their seen
// sets.
func (w *destWalk) id(v ssa.Value) uint32 {
	a := w.a
	id, ok := a.destIDs[v]
	if !ok {
		id = uint32(len(a.destIDs) + 1)
		a.destIDs[v] = id
	}
	return id
}

func (w *destWalk) unknown() { w.out.unknown = true }

func (w *destWalk) value(v ssa.Value, s destState) {
	if w.steps > maxDestSteps {
		return
	}
	// (The walk goes on after a way it cannot follow: what it can tell
	// of the other ways is marked all the same.)
	key := s.key(w.id(v))
	if w.seen[key] {
		return
	}
	w.seen[key] = true
	if w.steps++; w.steps > maxDestSteps || len(s.ops) > maxDestOps {
		w.unknown()
		return
	}
	switch x := v.(type) {
	case *ssa.Phi:
		for _, e := range x.Edges {
			w.value(e, s)
		}
	case *ssa.ChangeType:
		w.value(x.X, s)
	case *ssa.ChangeInterface:
		w.value(x.X, s)
	case *ssa.MakeInterface:
		if !s.boxed || len(s.fields) > 0 || isInterface(x.X.Type()) {
			w.unknown()
			return
		}
		// reflect.ValueOf(x): the memory x holds, whatever the calls
		// that walked into it.
		w.known(x.X.Type())
		// But three calls that follow a pointer may have crossed an
		// interface value inside x (Elem into x, Elem of the
		// interface, Elem of the pointer it holds), to memory that the
		// static type of x does not show.
		crossing := 0
		for _, op := range s.ops {
			switch op {
			case opDeref, opIndex, opOther:
				crossing++
			}
		}
		if crossing >= 3 && w.a.canHold(x.X.Type(), make(map[types.Type]bool), 0) {
			w.unknown()
		}
	case *ssa.Field:
		w.value(x.X, s.push(x.Field))
	case *ssa.Extract:
		call, ok := x.Tuple.(*ssa.Call)
		if !ok {
			w.unknown()
			return
		}
		w.call(call, x.Index, s)
	case *ssa.Call:
		w.call(x, 0, s)
	case *ssa.Parameter:
		w.param(x, s)
	case *ssa.UnOp:
		if x.Op != token.MUL {
			w.unknown()
			return
		}
		w.load(x.X, s)
	case *ssa.Const:
		// A nil interface or a zero Value: no memory to store to.
	default:
		w.unknown()
	}
}

func (w *destWalk) known(t types.Type) {
	for _, k := range w.out.known {
		if types.Identical(k, t) {
			return
		}
	}
	w.out.known = append(w.out.known, t)
}

// call follows result idx of call.
func (w *destWalk) call(call *ssa.Call, idx int, s destState) {
	a := w.a
	sc := call.Call.StaticCallee()
	if sc == nil {
		w.unknown()
		return
	}
	if a.model(sc) == modelReflect {
		args := call.Call.Args
		if s.boxed || len(s.fields) > 0 || idx != 0 || !isReflectValue(call.Type()) {
			w.unknown()
			return
		}
		switch {
		case sc.Signature.Recv() == nil && sc.Name() == "ValueOf" && len(args) == 1:
			s.boxed = true
			w.value(args[0], s)
		case reflectFresh(sc):
			// The calls were collected the last applied first.
			ops := make([]reflectOp, len(s.ops))
			for i, op := range s.ops {
				ops[len(ops)-1-i] = op
			}
			if !freshNamed(sc.Name(), ops, w.referent) {
				w.unknown()
				return
			}
			if sc.Name() == "Zero" {
				// Nothing can be stored through it.
				return
			}
			whole := sc.Name() == "New" && len(ops) == 1 && !w.referent
			for i, f := range w.out.fresh {
				if f.call == call {
					w.out.fresh[i].whole = f.whole && whole
					return
				}
			}
			w.out.fresh = append(w.out.fresh, freshDest{call, whole})
		default:
			op := reflectDerive(sc)
			if op == 0 || len(args) == 0 || !isReflectValue(args[0].Type()) {
				w.unknown()
				return
			}
			w.value(args[0], s.withOp(op))
		}
		return
	}
	if _, ok := a.intrinsic(sc); ok || !a.known(sc) || s.calls >= maxDestCalls {
		w.unknown()
		return
	}
	// A helper: what it returns.
	s = s.call()
	for _, b := range sc.Blocks {
		if len(b.Instrs) == 0 {
			continue
		}
		ret, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return)
		if !ok {
			continue
		}
		if idx >= len(ret.Results) {
			w.unknown()
			return
		}
		w.value(ret.Results[idx], s)
	}
}

// param follows the parameter p to the operands of the calls of its
// function, if the pass sees them all.
func (w *destWalk) param(p *ssa.Parameter, s destState) {
	a := w.a
	fn := p.Parent()
	if fn == nil || s.calls >= maxDestCalls || a.openFunc(fn) || a.reflectTarget(fn) {
		// Code outside the program, or a call through reflection, may
		// pass anything.
		w.unknown()
		return
	}
	idx := -1
	for i, q := range fn.Params {
		if q == p {
			idx = i
		}
	}
	if idx < 0 {
		w.unknown()
		return
	}
	s = s.call()
	sites := a.callers(fn)
	if len(sites) == 0 {
		// Nothing the pass sees calls it: do not conclude from that.
		w.unknown()
		return
	}
	for _, site := range sites {
		arg := argAt(site, fn, idx)
		if arg == nil {
			w.unknown()
			return
		}
		w.value(arg, s)
	}
}

// load follows a load from addr: a local variable that nothing but its
// loads and stores uses, or a field of one.
func (w *destWalk) load(addr ssa.Value, s destState) {
	for {
		fa, ok := addr.(*ssa.FieldAddr)
		if !ok {
			break
		}
		s = s.push(fa.Field)
		addr = fa.X
	}
	alloc, ok := addr.(*ssa.Alloc)
	if !ok || !privateAddr(alloc, 0) {
		w.unknown()
		return
	}
	w.stores(alloc, s)
}

// stores follows the values stored through addr, the address of a private
// local variable or of a field of one, that reach the fields of s.
func (w *destWalk) stores(addr ssa.Value, s destState) {
	refs := addr.Referrers()
	if refs == nil {
		w.unknown()
		return
	}
	for _, r := range *refs {
		switch r := r.(type) {
		case *ssa.Store:
			w.value(r.Val, s)
		case *ssa.FieldAddr:
			if n := len(s.fields); n > 0 && s.fields[n-1] == r.Field {
				w.stores(r, s.pop())
			}
		}
	}
}

// privateAddr reports whether addr, the address of a local variable or of
// a field of one, is used only to load, to store to, and to take the
// addresses of fields that are used so.
func privateAddr(addr ssa.Value, depth int) bool {
	refs := addr.Referrers()
	if refs == nil || depth > 8 {
		return false
	}
	for _, r := range *refs {
		switch r := r.(type) {
		case *ssa.DebugRef:
		case *ssa.Store:
			if r.Val == addr {
				return false
			}
		case *ssa.UnOp:
			if r.Op != token.MUL {
				return false
			}
		case *ssa.FieldAddr:
			if !privateAddr(r, depth+1) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// soleDeref returns the call of Value.Elem or reflect.Indirect that is the
// only use of the reflect.Value v, or nil.
func (a *analysis) soleDeref(v *ssa.Call) *ssa.Call {
	refs := v.Referrers()
	if refs == nil {
		return nil
	}
	var deref *ssa.Call
	for _, r := range *refs {
		if _, dbg := r.(*ssa.DebugRef); dbg {
			continue
		}
		call, ok := r.(*ssa.Call)
		if !ok || deref != nil {
			return nil
		}
		sc := call.Call.StaticCallee()
		if sc == nil || a.model(sc) != modelReflect || reflectDerive(sc) != opDeref || len(call.Call.Args) != 1 || call.Call.Args[0] != ssa.Value(v) {
			return nil
		}
		deref = call
	}
	return deref
}

// reflectBoxes reports whether the function or method fn of package
// reflect returns a composite around what it is passed: a pointer to it
// (Value.Addr) or a slice with it (reflect.Append, AppendSlice). What
// held the Subject itself is then held by the result.
func reflectBoxes(fn *ssa.Function) bool {
	if fn.Signature.Recv() != nil {
		return fn.Name() == "Addr"
	}
	switch fn.Name() {
	case "Append", "AppendSlice":
		return true
	}
	return false
}

// typedReads handles the fresh memory of the reflect.Value root, into
// which a store through reflection put a marked value: where the program
// reads that memory back through its type, by asserting what
// Value.Interface returns to a composite type (p.Interface().(*T)), the
// memory of that type is marked. It follows root through the calls of
// package reflect only, and reports whether that shows every such read:
// it is false if root, or a Value or interface made from it, goes
// anywhere else (into memory, to another function, into another store).
func (a *analysis) typedReads(root *ssa.Call, why reason) bool {
	if done, ok := a.typedRead[root]; ok {
		return done
	}
	all := true
	seen := map[ssa.Value]bool{root: true}
	work := []ssa.Value{root}
	push := func(v ssa.Value) {
		if !seen[v] {
			seen[v] = true
			work = append(work, v)
		}
	}
	for len(work) > 0 {
		v := work[len(work)-1]
		work = work[:len(work)-1]
		refs := v.Referrers()
		if refs == nil {
			all = false
			continue
		}
		for _, r := range *refs {
			switch x := r.(type) {
			case *ssa.DebugRef:
			case *ssa.TypeAssert:
				// (v is an interface that Value.Interface returned.)
				if isInterface(x.AssertedType) {
					push(x)
					continue
				}
				// A composite of that type, which may be an error
				// itself (a *MyErr with an error in a field).
				a.markHeld(x.AssertedType, reason{kind: kAssert, from: ref{kind: refValue, k: key{root, 0}}, site: x, typ: x.AssertedType})
			case *ssa.Extract:
				if _, ok := x.Tuple.(*ssa.TypeAssert); ok {
					// The value of v.(I), or its ok.
					if x.Index == 0 && isInterface(x.Type()) {
						push(x)
					}
					continue
				}
				all = false
			case *ssa.Call:
				if x.Call.IsInvoke() && x.Call.Value == v {
					// A method called on the interface: its receiver
					// is the composite, as after a type assertion.
					for _, arg := range x.Call.Args {
						if arg == v {
							all = false
						}
					}
					for _, g := range a.callees(x) {
						if len(g.Params) == 0 || paramShift(x, g) != 0 {
							all = false
							continue
						}
						pt := g.Params[0].Type()
						a.markHeld(pt, reason{kind: kAssert, from: ref{kind: refValue, k: key{root, 0}}, site: x, typ: pt})
					}
					continue
				}
				sc := x.Call.StaticCallee()
				if sc == nil || a.model(sc) != modelReflect || len(x.Call.Args) == 0 || x.Call.Args[0] != v {
					all = false
					continue
				}
				// The receiver (or first operand) of a call of
				// package reflect; as any other operand it is stored.
				for _, arg := range x.Call.Args[1:] {
					if arg == v {
						all = false
					}
				}
				res := x.Call.Signature().Results()
				for i := range res.Len() {
					if rt := res.At(i).Type(); reflectGives(sc, i) && !reflectCarrier(rt) {
						// reflect.TypeAssert[T](v): a read through
						// the type T that is not followed from here.
						all = false
						if !isInterface(rt) {
							a.markHeld(rt, reason{kind: kAssert, from: ref{kind: refValue, k: key{root, 0}}, site: x, typ: rt})
						}
						continue
					}
					if !reflectCarrier(res.At(i).Type()) {
						if st, ok := res.At(i).Type().Underlying().(*types.Slice); ok && reflectCarrier(st.Elem()) {
							all = false
						}
						continue
					}
					if res.Len() != 1 {
						all = false
						continue
					}
					push(x)
				}
			default:
				all = false
			}
		}
	}
	a.typedRead[root] = all
	return all
}
