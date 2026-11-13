package mark

import (
	"context"
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/flow"
)

// tags are the contexts in which a Mark holds: free (any calling
// context), or bit i+1 for "when parameter i of the enclosing function
// holds the Subject". A set with free is just free.
type tags uint64

const free tags = 1

// maxParamTag is the number of parameters that have a tag of their own;
// a Mark that depends on a later parameter is free.
const maxParamTag = 63

func paramTag(i int) tags {
	if i < 0 || i >= maxParamTag {
		return free
	}
	return 1 << uint(i+1)
}

// key names a markable value: an SSA value, or component idx of a
// tuple-typed one (idx is 0 for a value that is not a tuple).
type key struct {
	v   ssa.Value
	idx int32
}

// locKind is the kind of a memory location.
type locKind uint8

const (
	// locCell is a local variable: v is the root of the class of its
	// Alloc and the FreeVars that capture it.
	locCell locKind = iota + 1
	// locGlobal is the package-level variable v.
	locGlobal
	// locField is field idx of the struct type id.
	locField
	// locTyped is the memory of type id behind pointers, and the
	// elements of slices and arrays of that type.
	locTyped
	// locMapKey and locMapVal are the keys and values of the map type id.
	locMapKey
	locMapVal
	// locChan is the elements of the channels of element type id.
	locChan
	// locPanic is the value of a panic.
	locPanic
	// locAtomic is the contents of the sync/atomic values of type id.
	locAtomic
	// locReflect is every value passed to reflect.ValueOf.
	locReflect
)

// loc names a memory location.
type loc struct {
	kind locKind
	v    ssa.Value
	id   int32
	idx  int32
}

// refKind says what a ref names.
type refKind uint8

const (
	refNone refKind = iota
	refValue
	refLoc
	refRet
)

// ref names the marked thing a Mark came from, for traces.
type ref struct {
	kind refKind
	k    key
	l    loc
	fn   *ssa.Function
	idx  int32
	// free says that the Mark came from the free tag of the thing, so
	// that the trace follows the reason it became free.
	free bool
}

// stepKind is how a Mark came about.
type stepKind uint8

const (
	kFlow stepKind = iota // a copy: phi, conversion, extract
	kSeedSentinel
	kSeedStored
	kSeedType
	kSeedWrapper
	kSeedOpaque
	kAssert
	kLoad
	kStore
	kWrap
	kUnwrap
	kParam
	kThrough
	kRet
	kReturn
	kCapture
	kAs
)

// reason is the first way a Mark was found.
type reason struct {
	kind stepKind
	from ref
	site ssa.Instruction
	fn   *ssa.Function
	typ  types.Type
	text string
}

// info is the Mark of a value or of a function result.
type info struct {
	t tags
	// first is why it was first marked; firstFree says that it was then
	// already free. freeR is why it became free later.
	first     reason
	firstFree bool
	freeR     *reason
}

// locInfo is the state of a memory location.
type locInfo struct {
	marked  bool
	why     reason
	readers []key
}

// candidate is a possible wrapper type that the program converts to an
// interface.
type candidate struct {
	typ     types.Type
	wrapper bool
	// seeds are the conversions of values of the type.
	seeds []ssa.Value
}

type fieldKey struct {
	id  int32
	idx int
}

type item struct {
	isRet bool
	k     key
	fn    *ssa.Function
	idx   int32
	delta tags
}

type analysis struct {
	prog *ssa.Program
	res  Callees
	subj Subject

	fns     []*ssa.Function
	fnIndex map[*ssa.Function]int32
	ty      *typeIDs

	cellParent  map[ssa.Value]ssa.Value
	cellEsc     map[ssa.Value]bool // by member, then by root
	globalEsc   map[*ssa.Global]bool
	globalStore map[*ssa.Global][]*ssa.Store
	fieldEscRaw []rawField
	fieldEsc    map[fieldKey]bool
	// fieldOwner is a type that has the field, for traces.
	fieldOwner map[fieldKey]types.Type

	locs     map[loc]*locInfo
	locTypes map[loc]types.Type
	marks    map[key]*info
	rets     map[*ssa.Function][]info
	work     []item
	head     int

	cands    []*candidate
	candByID map[int32]*candidate

	typeSeeds  []ssa.Value
	opaque     []opaqueSeed
	varargs    map[*ssa.Alloc]*ssa.Call
	asPointees []types.Type
	asSeen     map[int32]bool
	asUnknown  bool

	siteOrd      map[ssa.CallInstruction]int64
	calleesCache map[ssa.CallInstruction][]*ssa.Function
	callersCache map[*ssa.Function][]ssa.CallInstruction
	models       map[*ssa.Function]model

	// class are the package-level variables that hold the Sentinel
	// Subject's value; dynTypes are the types that value may have, and
	// dynUnknown says they are not all known.
	class      []*ssa.Global
	dynTypes   []types.Type
	dynUnknown bool
	// modelIntrinsics says that static calls of the intrinsics are
	// modelled instead of following their bodies.
	modelIntrinsics bool

	// heldBy is set by isWrapper and holds (see isWrapper).
	heldBy ref

	markableCache map[types.Type]bool
	wrapperCache  map[types.Type]bool
	typeSeedCache map[types.Type]bool
	assertCache   map[types.Type]bool
}

type rawField struct {
	st  *types.Struct
	idx int
}

type opaqueSeed struct {
	call *ssa.Call
	text string
}

func newAnalysis(prog *ssa.Program, res Callees, subj Subject) *analysis {
	return &analysis{
		prog:          prog,
		res:           res,
		subj:          subj,
		fnIndex:       make(map[*ssa.Function]int32),
		ty:            newTypeIDs(),
		cellParent:    make(map[ssa.Value]ssa.Value),
		cellEsc:       make(map[ssa.Value]bool),
		globalEsc:     make(map[*ssa.Global]bool),
		globalStore:   make(map[*ssa.Global][]*ssa.Store),
		fieldEsc:      make(map[fieldKey]bool),
		fieldOwner:    make(map[fieldKey]types.Type),
		locs:          make(map[loc]*locInfo),
		locTypes:      make(map[loc]types.Type),
		marks:         make(map[key]*info),
		rets:          make(map[*ssa.Function][]info),
		candByID:      make(map[int32]*candidate),
		varargs:       make(map[*ssa.Alloc]*ssa.Call),
		asSeen:        make(map[int32]bool),
		siteOrd:       make(map[ssa.CallInstruction]int64),
		calleesCache:  make(map[ssa.CallInstruction][]*ssa.Function),
		callersCache:  make(map[*ssa.Function][]ssa.CallInstruction),
		models:        make(map[*ssa.Function]model),
		markableCache: make(map[types.Type]bool),
		wrapperCache:  make(map[types.Type]bool),
		typeSeedCache: make(map[types.Type]bool),
		assertCache:   make(map[types.Type]bool),
	}
}

func (a *analysis) run(ctx context.Context) error {
	a.setupIntrinsics()
	a.collect()
	a.scanAddresses()
	if err := ctx.Err(); err != nil {
		return err
	}
	a.setupSubject()
	a.index()
	if err := ctx.Err(); err != nil {
		return err
	}
	a.seed()
	for {
		if err := a.drain(ctx); err != nil {
			return err
		}
		// A type becomes a wrapper of the Subject once its memory or its
		// Unwrap result is marked: every conversion of it is then a seed.
		changed := false
		for _, c := range a.cands {
			if c.wrapper || !a.isWrapper(c.typ) {
				continue
			}
			c.wrapper = true
			changed = true
			for _, s := range c.seeds {
				a.mark(key{s, 0}, free, reason{kind: kSeedWrapper, from: a.heldBy, site: s.(ssa.Instruction), typ: c.typ})
			}
		}
		if !changed {
			return nil
		}
	}
}

// isTemplate reports whether fn is the body of a generic function (or a
// function literal inside one), which never runs: its instances do.
func isTemplate(fn *ssa.Function) bool {
	for f := fn; f != nil; f = f.Parent() {
		if f.TypeParams().Len() > 0 && len(f.TypeArgs()) == 0 {
			return true
		}
	}
	return false
}

// collect lists the functions to scan, in a deterministic order: the
// functions and declared methods of every package, and, from those, every
// function an instruction refers to (function values, closures, bound
// method wrappers) and every method of a type that an instruction converts
// to an interface (the methods a dynamic call can reach).
//
// It does not use ssautil.AllFunctions: that also holds the methods of the
// types reflection can reach from those, a set that differs from run to
// run (ssa.Program.RuntimeTypes depends on map order). Such a method runs
// only through reflection, which the pass does not follow anyway.
func (a *analysis) collect() {
	seen := make(map[*ssa.Function]bool)
	var work []*ssa.Function
	add := func(fn *ssa.Function) {
		if fn != nil && !seen[fn] {
			seen[fn] = true
			work = append(work, fn)
		}
	}
	for _, pkg := range a.prog.AllPackages() {
		for _, m := range pkg.Members {
			switch m := m.(type) {
			case *ssa.Function:
				add(m)
			case *ssa.Type:
				if named, ok := m.Type().(*types.Named); ok {
					for i := range named.NumMethods() {
						add(a.prog.FuncValue(named.Method(i)))
					}
				}
			}
		}
	}
	type named struct {
		fn   *ssa.Function
		name string
	}
	var list []named
	var rands []*ssa.Value
	for len(work) > 0 {
		fn := work[len(work)-1]
		work = work[:len(work)-1]
		if fn.Blocks == nil || isTemplate(fn) {
			continue
		}
		if _, ok := a.intrinsic(fn); ok {
			// Its static calls are modelled; its body, which moves its
			// operands through memory of type any, would mark that
			// memory for every Subject. A dynamic call that may reach
			// it is modelled too (see indexCallees).
			continue
		}
		list = append(list, named{fn, fn.String()})
		for _, anon := range fn.AnonFuncs {
			add(anon)
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				rands = instr.Operands(rands[:0])
				for _, r := range rands {
					if r == nil || *r == nil {
						continue
					}
					if f, ok := (*r).(*ssa.Function); ok {
						add(f)
					}
				}
				mi, ok := instr.(*ssa.MakeInterface)
				if !ok || isInterface(mi.X.Type()) {
					continue
				}
				ms := a.prog.MethodSets.MethodSet(mi.X.Type())
				for i := range ms.Len() {
					sel := ms.At(i)
					if sel.Obj().(*types.Func).Signature().TypeParams() == nil {
						add(a.prog.MethodValue(sel))
					}
				}
			}
		}
	}
	sort.Slice(list, func(i, j int) bool {
		x, y := list[i], list[j]
		if x.name != y.name {
			return x.name < y.name
		}
		if x.fn.Pos() != y.fn.Pos() {
			return x.fn.Pos() < y.fn.Pos()
		}
		return x.fn.Synthetic < y.fn.Synthetic
	})
	a.fns = make([]*ssa.Function, len(list))
	for i, n := range list {
		a.fns[i] = n.fn
		a.fnIndex[n.fn] = int32(i)
	}
}

func structOf(t types.Type) *types.Struct {
	if p, ok := t.Underlying().(*types.Pointer); ok {
		t = p.Elem()
	}
	st, _ := t.Underlying().(*types.Struct)
	return st
}

func pointerElem(t types.Type) types.Type {
	if p, ok := t.Underlying().(*types.Pointer); ok {
		return p.Elem()
	}
	return nil
}

func chanElem(t types.Type) types.Type {
	if c, ok := t.Underlying().(*types.Chan); ok {
		return c.Elem()
	}
	return nil
}

// scanAddresses is the first scan of the program. It unifies the types
// that conversions join, joins every captured variable with the FreeVars
// that capture it, and finds the variables and fields whose address is
// used other than to load and store them, and the stores to package-level
// variables.
func (a *analysis) scanAddresses() {
	var rands []*ssa.Value
	for _, fn := range a.fns {
		for _, fv := range fn.FreeVars {
			if a.addrEscapes(fv, true) {
				a.cellEsc[fv] = true
			}
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				switch x := instr.(type) {
				case *ssa.ChangeType:
					a.ty.unify(x.X.Type(), x.Type(), 0)
				case *ssa.Convert:
					a.ty.unify(x.X.Type(), x.Type(), 0)
				case *ssa.MultiConvert:
					a.ty.unify(x.X.Type(), x.Type(), 0)
				case *ssa.SliceToArrayPointer:
					a.ty.unify(x.X.Type(), x.Type(), 0)
				case *ssa.MakeClosure:
					cl, _ := x.Fn.(*ssa.Function)
					if cl == nil {
						break
					}
					for i, bind := range x.Bindings {
						if i >= len(cl.FreeVars) {
							break
						}
						fv := cl.FreeVars[i]
						switch bind.(type) {
						case *ssa.Alloc, *ssa.FreeVar:
							if pointerElem(fv.Type()) != nil {
								a.cellUnion(fv, bind)
								continue
							}
						}
						a.cellEsc[fv] = true
					}
				case *ssa.Alloc:
					if a.addrEscapes(x, true) {
						a.cellEsc[x] = true
					}
				case *ssa.FieldAddr:
					if st := structOf(x.X.Type()); st != nil && a.addrEscapes(x, false) {
						a.fieldEscRaw = append(a.fieldEscRaw, rawField{st, x.Field})
					}
				}
				rands = instr.Operands(rands[:0])
				for _, r := range rands {
					if r == nil || *r == nil {
						continue
					}
					g, ok := (*r).(*ssa.Global)
					if !ok {
						continue
					}
					switch x := instr.(type) {
					case *ssa.Store:
						if r == &x.Addr {
							a.globalStore[g] = append(a.globalStore[g], x)
							continue
						}
					case *ssa.UnOp:
						if x.Op == token.MUL {
							continue
						}
					case *ssa.FieldAddr, *ssa.IndexAddr, *ssa.DebugRef:
						continue
					}
					a.globalEsc[g] = true
				}
			}
		}
	}
	// Types are no longer unified from here on: locations can be named.
	for _, rf := range a.fieldEscRaw {
		a.fieldEsc[fieldKey{a.ty.id(rf.st), rf.idx}] = true
	}
	var members []ssa.Value
	for v, esc := range a.cellEsc {
		if esc {
			members = append(members, v)
		}
	}
	for _, v := range members {
		a.cellEsc[a.cellFind(v)] = true
	}
}

func (a *analysis) cellFind(v ssa.Value) ssa.Value {
	for {
		p, ok := a.cellParent[v]
		if !ok || p == v {
			return v
		}
		v = p
	}
}

// cellUnion joins the cell of the FreeVar fv with that of the variable
// bound to it.
func (a *analysis) cellUnion(fv, bind ssa.Value) {
	x, y := a.cellFind(fv), a.cellFind(bind)
	if x != y {
		a.cellParent[x] = y
	}
}

// addrEscapes reports whether the address v is used other than to load
// from it, store to it, take the address of a part of it or convert it to
// unsafe.Pointer. A cell (an
// Alloc or FreeVar) may also be captured by a closure, which joins the
// cells, and be the target of errors.As, which is modelled.
func (a *analysis) addrEscapes(v ssa.Value, cell bool) bool {
	refs := v.Referrers()
	if refs == nil {
		return true
	}
	for _, r := range *refs {
		switch r := r.(type) {
		case *ssa.Store:
			if r.Val == v {
				return true
			}
		case *ssa.UnOp:
			if r.Op != token.MUL {
				return true
			}
		case *ssa.DebugRef, *ssa.FieldAddr, *ssa.IndexAddr:
		case *ssa.Convert:
			// An address converted to unsafe.Pointer is out of sight
			// (a known limit); it is no typed pointer to the variable.
			if b, ok := r.Type().Underlying().(*types.Basic); !ok || b.Kind() != types.UnsafePointer {
				return true
			}
		case *ssa.MakeClosure:
			if !cell {
				return true
			}
		case *ssa.MakeInterface:
			if !cell || !a.onlyAsTarget(r) {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// onlyAsTarget reports whether the interface value mi is used only as the
// target of static errors.As calls.
func (a *analysis) onlyAsTarget(mi *ssa.MakeInterface) bool {
	refs := mi.Referrers()
	if refs == nil {
		return false
	}
	for _, r := range *refs {
		switch r := r.(type) {
		case *ssa.DebugRef:
		case *ssa.Call:
			if a.model(r.Call.StaticCallee()) != modelErrorsAs || len(r.Call.Args) != 2 || r.Call.Args[1] != ssa.Value(mi) || r.Call.Args[0] == ssa.Value(mi) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Locations.

func (a *analysis) named(l loc, t types.Type) loc {
	if _, ok := a.locTypes[l]; !ok {
		a.locTypes[l] = t
	}
	return l
}

func (a *analysis) typedLoc(t types.Type) loc {
	return a.named(loc{kind: locTyped, id: a.ty.id(t)}, t)
}

func (a *analysis) mapLoc(kind locKind, mapType types.Type) loc {
	return a.named(loc{kind: kind, id: a.ty.id(mapType.Underlying())}, mapType)
}

func (a *analysis) chanLoc(elem types.Type) loc {
	return a.named(loc{kind: locChan, id: a.ty.id(elem)}, elem)
}

func (a *analysis) atomicLoc(t types.Type) loc {
	return a.named(loc{kind: locAtomic, id: a.ty.id(t)}, t)
}

// fieldLocs returns the locations of field i of the struct type st: the
// field, and the memory of its type if the address of the field is taken
// anywhere.
func (a *analysis) fieldLocs(st *types.Struct, i int) (locs [2]loc, n int) {
	id := a.ty.id(st)
	locs[0] = a.named(loc{kind: locField, id: id, idx: int32(i)}, st)
	n = 1
	if a.fieldEsc[fieldKey{id, i}] {
		locs[1] = a.typedLoc(st.Field(i).Type())
		n = 2
	}
	return locs, n
}

// addrLocs returns the locations a load or store through addr touches.
func (a *analysis) addrLocs(addr ssa.Value) (locs [2]loc, n int) {
	elem := pointerElem(addr.Type())
	if elem == nil {
		return locs, 0
	}
	switch x := addr.(type) {
	case *ssa.Alloc, *ssa.FreeVar:
		root := a.cellFind(x)
		locs[0] = loc{kind: locCell, v: root}
		if a.cellEsc[root] {
			locs[1] = a.typedLoc(elem)
			return locs, 2
		}
		return locs, 1
	case *ssa.Global:
		locs[0] = loc{kind: locGlobal, v: x}
		if a.globalEsc[x] {
			locs[1] = a.typedLoc(elem)
			return locs, 2
		}
		return locs, 1
	case *ssa.FieldAddr:
		if st := structOf(x.X.Type()); st != nil && x.Field < st.NumFields() {
			a.noteOwner(x.X.Type(), st, x.Field)
			return a.fieldLocs(st, x.Field)
		}
	}
	locs[0] = a.typedLoc(elem)
	return locs, 1
}

// noteOwner records t (or the type t points to) as a type that has field
// i of st.
func (a *analysis) noteOwner(t types.Type, st *types.Struct, i int) {
	k := fieldKey{a.ty.id(st), i}
	if _, ok := a.fieldOwner[k]; ok {
		return
	}
	if p := pointerElem(t); p != nil {
		t = p
	}
	a.fieldOwner[k] = t
}

func (a *analysis) locInfo(l loc) *locInfo {
	li := a.locs[l]
	if li == nil {
		li = new(locInfo)
		a.locs[l] = li
	}
	return li
}

func (a *analysis) locMarked(l loc) bool {
	li := a.locs[l]
	return li != nil && li.marked
}

func (a *analysis) addReader(l loc, k key) {
	li := a.locInfo(l)
	li.readers = append(li.readers, k)
}

// markLoc marks the location l: every value loaded from it is marked, in
// any calling context.
func (a *analysis) markLoc(l loc, why reason) {
	li := a.locInfo(l)
	if li.marked {
		return
	}
	li.marked = true
	li.why = why
	for _, k := range li.readers {
		why := reason{kind: kLoad, from: ref{kind: refLoc, l: l, free: true}}
		if instr, ok := k.v.(ssa.Instruction); ok {
			why.site = instr
		}
		a.mark(k, free, why)
	}
}

// Marks.

func (a *analysis) explicit(k key) bool {
	in := a.marks[k]
	return in != nil && in.t != 0
}

// tagsOf returns the tags of the Mark of v, a value that is not a tuple.
func (a *analysis) tagsOf(v ssa.Value) tags {
	if in := a.marks[key{v, 0}]; in != nil && in.t != 0 {
		return in.t
	}
	if a.implicit(v.Type()) {
		return free
	}
	return 0
}

func (a *analysis) canMark(k key) bool {
	t := k.v.Type()
	if tup, ok := t.(*types.Tuple); ok {
		if int(k.idx) >= tup.Len() {
			return false
		}
		t = tup.At(int(k.idx)).Type()
	} else if k.idx != 0 {
		return false
	}
	return a.markable(t)
}

func valueRef(k key, t tags) ref {
	return ref{kind: refValue, k: k, free: t&free != 0}
}

func (a *analysis) mark(k key, t tags, why reason) {
	if t == 0 || !a.canMark(k) {
		return
	}
	if _, ok := k.v.(*ssa.Const); ok {
		return
	}
	in := a.marks[k]
	if in == nil {
		in = new(info)
		a.marks[k] = in
	}
	a.add(in, t, why, item{k: k})
}

func (a *analysis) markRet(fn *ssa.Function, idx int, t tags, why reason) {
	res := fn.Signature.Results()
	if t == 0 || idx >= res.Len() || !a.markable(res.At(idx).Type()) {
		return
	}
	rs := a.rets[fn]
	if rs == nil {
		rs = make([]info, res.Len())
		a.rets[fn] = rs
	}
	a.add(&rs[idx], t, why, item{isRet: true, fn: fn, idx: int32(idx)})
}

func (a *analysis) add(in *info, t tags, why reason, it item) {
	if in.t&free != 0 {
		return
	}
	nt := in.t | t
	if nt&free != 0 {
		nt = free
	}
	if nt == in.t {
		return
	}
	delta := nt &^ in.t
	if nt == free {
		delta = free
	}
	if in.t == 0 {
		in.first = why
		in.firstFree = nt == free
	} else if nt == free {
		w := why
		in.freeR = &w
	}
	in.t = nt
	it.delta = delta
	a.work = append(a.work, it)
}

func (a *analysis) drain(ctx context.Context) error {
	for n := 0; a.head < len(a.work); n++ {
		if n&0xfff == 0xfff {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		it := a.work[a.head]
		a.head++
		if it.isRet {
			a.retChanged(it.fn, int(it.idx), it.delta)
			continue
		}
		a.propagate(it.k, it.delta)
	}
	a.work = a.work[:0]
	a.head = 0
	return nil
}

// propagate pushes the new tags delta of the Mark of k to the users of
// the value.
func (a *analysis) propagate(k key, delta tags) {
	refs := k.v.Referrers()
	if refs == nil {
		return
	}
	from := valueRef(k, delta)
	if _, ok := k.v.Type().(*types.Tuple); ok {
		for _, r := range *refs {
			if ex, ok := r.(*ssa.Extract); ok && ex.Index == int(k.idx) {
				a.mark(key{ex, 0}, delta, reason{kind: kFlow, from: from, site: ex})
			}
		}
		return
	}
	v := k.v
	var prev ssa.Instruction
	for _, r := range *refs {
		if r == prev {
			// An instruction that uses v twice is listed twice.
			continue
		}
		prev = r
		switch x := r.(type) {
		case *ssa.Phi:
			a.mark(key{x, 0}, delta, reason{kind: kFlow, from: from, site: x})
		case *ssa.ChangeInterface:
			a.mark(key{x, 0}, delta, reason{kind: kFlow, from: from, site: x})
		case *ssa.MakeInterface:
			a.mark(key{x, 0}, delta, reason{kind: kFlow, from: from, site: x})
		case *ssa.ChangeType:
			a.mark(key{x, 0}, delta, reason{kind: kFlow, from: from, site: x})
		case *ssa.Convert:
			a.mark(key{x, 0}, delta, reason{kind: kFlow, from: from, site: x})
		case *ssa.MultiConvert:
			a.mark(key{x, 0}, delta, reason{kind: kFlow, from: from, site: x})
		case *ssa.Slice:
			if x.X == v {
				a.mark(key{x, 0}, delta, reason{kind: kFlow, from: from, site: x})
			}
		case *ssa.TypeAssert:
			if x.X == v && a.assertable(x.AssertedType) {
				a.mark(key{x, 0}, delta, reason{kind: kAssert, from: from, site: x, typ: x.AssertedType})
			}
		case *ssa.Store:
			if x.Val == v {
				a.store(x.Addr, from, x)
			}
		case *ssa.MapUpdate:
			if x.Key == v {
				a.markLoc(a.mapLoc(locMapKey, x.Map.Type()), reason{kind: kStore, from: from, site: x})
			}
			if x.Value == v {
				a.markLoc(a.mapLoc(locMapVal, x.Map.Type()), reason{kind: kStore, from: from, site: x})
			}
		case *ssa.Send:
			if et := chanElem(x.Chan.Type()); x.X == v && et != nil {
				a.markLoc(a.chanLoc(et), reason{kind: kStore, from: from, site: x})
			}
		case *ssa.Select:
			for _, st := range x.States {
				if et := chanElem(st.Chan.Type()); st.Send == v && et != nil {
					a.markLoc(a.chanLoc(et), reason{kind: kStore, from: from, site: x})
				}
			}
		case *ssa.MakeClosure:
			cl, _ := x.Fn.(*ssa.Function)
			if cl == nil {
				break
			}
			for i, bind := range x.Bindings {
				if bind == v && i < len(cl.FreeVars) {
					// A captured value is memory: free.
					a.mark(key{cl.FreeVars[i], 0}, free, reason{kind: kCapture, from: from, site: x, fn: cl})
				}
			}
		case *ssa.Return:
			for i, res := range x.Results {
				if res == v {
					a.markRet(x.Parent(), i, delta, reason{kind: kRet, from: from, site: x, fn: x.Parent()})
				}
			}
		case *ssa.Panic:
			if x.X == v {
				a.markLoc(loc{kind: locPanic}, reason{kind: kStore, from: from, site: x})
			}
		case ssa.CallInstruction:
			a.callUse(k, delta, x)
		}
	}
}

// store marks the locations a store of the marked value from through addr
// writes. A store into the argument array of a modelled intrinsic call is
// an operand of that call instead.
func (a *analysis) store(addr ssa.Value, from ref, site ssa.Instruction) {
	if ia, ok := addr.(*ssa.IndexAddr); ok {
		if alloc, ok := ia.X.(*ssa.Alloc); ok {
			if call := a.varargs[alloc]; call != nil {
				a.evalIntrinsic(call)
				return
			}
		}
	}
	locs, n := a.addrLocs(addr)
	for _, l := range locs[:n] {
		a.markLoc(l, reason{kind: kStore, from: from, site: site})
	}
}

// index is the second scan of the program: it records which values load
// from which locations, the conversions of the seed types and of the
// possible wrapper types, and the calls.
func (a *analysis) index() {
	for fi, fn := range a.fns {
		ord := int64(fi) << 32
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				switch x := instr.(type) {
				case *ssa.UnOp:
					switch x.Op {
					case token.MUL:
						if a.markable(x.Type()) {
							locs, n := a.addrLocs(x.X)
							for _, l := range locs[:n] {
								a.addReader(l, key{x, 0})
							}
						}
					case token.ARROW:
						if et := chanElem(x.X.Type()); et != nil && a.markable(et) {
							a.addReader(a.chanLoc(et), key{x, 0})
						}
					}
				case *ssa.Field:
					if st := structOf(x.X.Type()); st != nil && x.Field < st.NumFields() && a.markable(x.Type()) {
						a.noteOwner(x.X.Type(), st, x.Field)
						locs, n := a.fieldLocs(st, x.Field)
						for _, l := range locs[:n] {
							a.addReader(l, key{x, 0})
						}
					}
				case *ssa.Index:
					if arr, ok := x.X.Type().Underlying().(*types.Array); ok && a.markable(arr.Elem()) {
						a.addReader(a.typedLoc(arr.Elem()), key{x, 0})
					}
				case *ssa.Lookup:
					if mt, ok := x.X.Type().Underlying().(*types.Map); ok && a.markable(mt.Elem()) {
						a.addReader(a.mapLoc(locMapVal, x.X.Type()), key{x, 0})
					}
				case *ssa.Next:
					rng, _ := x.Iter.(*ssa.Range)
					if x.IsString || rng == nil {
						break
					}
					if mt, ok := rng.X.Type().Underlying().(*types.Map); ok {
						if a.markable(mt.Key()) {
							a.addReader(a.mapLoc(locMapKey, rng.X.Type()), key{x, 1})
						}
						if a.markable(mt.Elem()) {
							a.addReader(a.mapLoc(locMapVal, rng.X.Type()), key{x, 2})
						}
					}
				case *ssa.Select:
					n := 0
					for _, st := range x.States {
						if st.Dir != types.RecvOnly {
							continue
						}
						if et := chanElem(st.Chan.Type()); et != nil && a.markable(et) {
							a.addReader(a.chanLoc(et), key{x, int32(2 + n)})
						}
						n++
					}
				case *ssa.MakeInterface:
					ot := x.X.Type()
					if isInterface(ot) {
						break
					}
					if a.typeSeed(ot) {
						a.typeSeeds = append(a.typeSeeds, x)
					} else if a.potentialWrapper(ot) {
						id := a.ty.id(ot)
						c := a.candByID[id]
						if c == nil {
							c = &candidate{typ: ot}
							a.candByID[id] = c
							a.cands = append(a.cands, c)
						}
						c.seeds = append(c.seeds, x)
					}
					if pe := pointerElem(ot); pe != nil && a.markable(pe) && a.assertable(pe) {
						if id := a.ty.id(pe); !a.asSeen[id] {
							a.asSeen[id] = true
							a.asPointees = append(a.asPointees, pe)
						}
					}
				case *ssa.ChangeType:
					if ot := x.X.Type(); !isInterface(ot) && a.typeSeed(ot) {
						a.typeSeeds = append(a.typeSeeds, x)
					}
				case *ssa.Convert:
					if ot := x.X.Type(); !isInterface(ot) && a.typeSeed(ot) {
						a.typeSeeds = append(a.typeSeeds, x)
					}
				case ssa.CallInstruction:
					ord++
					a.siteOrd[x] = ord
					a.indexCall(x)
				}
			}
		}
	}
}

// setupIntrinsics decides whether static calls of the intrinsics are
// modelled.
func (a *analysis) setupIntrinsics() {
	a.modelIntrinsics = true
	if t := a.subj.Type; t != nil {
		if p := pointerElem(t); p != nil {
			t = p
		}
		if n, ok := types.Unalias(t).(*types.Named); ok && n.Obj().Pkg() != nil {
			switch n.Obj().Pkg().Path() {
			case "errors", "fmt", "braces.dev/errtrace":
				// The Subject is a type the intrinsics make: follow
				// their bodies.
				a.modelIntrinsics = false
			}
		}
	}
}

// setupSubject finds, for a Sentinel Subject, the variables that hold its
// value and the types that value may have.
func (a *analysis) setupSubject() {
	g := a.subj.Sentinel
	if g == nil {
		return
	}
	seen := map[*ssa.Global]bool{}
	add := func(h *ssa.Global) {
		if h != nil && !seen[h] {
			seen[h] = true
			a.class = append(a.class, h)
		}
	}
	add(g)
	add(flow.NewSentinels().Info(g).Canon)
	for i := 0; i < len(a.class); i++ {
		for _, st := range a.globalStore[a.class[i]] {
			add(globalLoad(st.Val))
		}
	}
	w := &typeWalk{a: a, seen: make(map[any]bool)}
	for _, h := range a.class {
		w.global(h, 0)
	}
	a.dynTypes, a.dynUnknown = w.types, w.unknown
}

// globalLoad returns the package-level variable that v loads, maybe
// converted, or nil.
func globalLoad(v ssa.Value) *ssa.Global {
	for {
		switch x := v.(type) {
		case *ssa.ChangeInterface:
			v = x.X
		case *ssa.MakeInterface:
			v = x.X
		case *ssa.ChangeType:
			v = x.X
		case *ssa.UnOp:
			g, _ := x.X.(*ssa.Global)
			if x.Op != token.MUL {
				return nil
			}
			return g
		default:
			return nil
		}
	}
}

// typeWalk collects the dynamic types a value may have, by walking back
// from it; unknown is set when a source is not followed.
type typeWalk struct {
	a       *analysis
	seen    map[any]bool
	types   []types.Type
	unknown bool
}

const maxTypeWalk = 12

func (w *typeWalk) add(t types.Type) {
	if isInterface(t) {
		w.unknown = true
		return
	}
	for _, d := range w.types {
		if types.Identical(d, t) {
			return
		}
	}
	w.types = append(w.types, t)
}

func (w *typeWalk) global(g *ssa.Global, depth int) {
	if w.seen[g] {
		return
	}
	w.seen[g] = true
	elem := pointerElem(g.Type())
	if elem == nil {
		w.unknown = true
		return
	}
	if !isInterface(elem) {
		w.add(elem)
		return
	}
	if w.a.globalEsc[g] {
		w.unknown = true
		return
	}
	for _, st := range w.a.globalStore[g] {
		w.value(st.Val, depth+1)
	}
}

func (w *typeWalk) value(v ssa.Value, depth int) {
	if depth > maxTypeWalk {
		w.unknown = true
		return
	}
	if w.seen[v] {
		return
	}
	w.seen[v] = true
	switch x := v.(type) {
	case *ssa.Const:
		if !x.IsNil() {
			w.add(x.Type())
		}
	case *ssa.MakeInterface:
		w.add(x.X.Type())
	case *ssa.ChangeInterface:
		w.value(x.X, depth+1)
	case *ssa.ChangeType:
		if isInterface(x.X.Type()) {
			w.value(x.X, depth+1)
		} else {
			w.add(x.Type())
		}
	case *ssa.Phi:
		for _, e := range x.Edges {
			w.value(e, depth+1)
		}
	case *ssa.UnOp:
		if g, ok := x.X.(*ssa.Global); ok && x.Op == token.MUL {
			w.global(g, depth+1)
			return
		}
		w.unknown = true
	case *ssa.Call:
		w.results(x, 0, depth)
	case *ssa.Extract:
		if call, ok := x.Tuple.(*ssa.Call); ok {
			w.results(call, x.Index, depth)
			return
		}
		w.unknown = true
	default:
		if isInterface(v.Type()) {
			w.unknown = true
		} else {
			w.add(v.Type())
		}
	}
}

func (w *typeWalk) results(call *ssa.Call, idx, depth int) {
	fn := call.Call.StaticCallee()
	if fn == nil || fn.Blocks == nil || idx >= fn.Signature.Results().Len() {
		w.unknown = true
		return
	}
	for _, b := range fn.Blocks {
		if len(b.Instrs) == 0 {
			continue
		}
		if r, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return); ok && idx < len(r.Results) {
			w.value(r.Results[idx], depth+1)
		}
	}
}

// seed makes the first Marks.
func (a *analysis) seed() {
	for _, g := range a.class {
		why := reason{kind: kSeedSentinel, text: Subject{Sentinel: g}.String()}
		locs, n := a.addrLocs(g)
		for _, l := range locs[:n] {
			a.markLoc(l, why)
		}
		// A value the program stores into the variable is its value
		// from then on.
		for _, st := range a.globalStore[g] {
			a.mark(key{st.Val, 0}, free, reason{kind: kSeedStored, site: st, text: why.text})
		}
	}
	for _, v := range a.typeSeeds {
		a.mark(key{v, 0}, free, reason{kind: kSeedType, site: v.(ssa.Instruction), typ: seedOperandType(v)})
	}
	for _, o := range a.opaque {
		res := o.call.Call.Signature().Results()
		for i := range res.Len() {
			a.markResult(o.call, i, free, reason{kind: kSeedOpaque, site: o.call, text: o.text})
		}
	}
}

func seedOperandType(v ssa.Value) types.Type {
	switch x := v.(type) {
	case *ssa.MakeInterface:
		return x.X.Type()
	case *ssa.ChangeType:
		return x.X.Type()
	case *ssa.Convert:
		return x.X.Type()
	}
	return v.Type()
}

// markResult marks result r of call.
func (a *analysis) markResult(call *ssa.Call, r int, t tags, why reason) {
	k := key{call, 0}
	if call.Call.Signature().Results().Len() > 1 {
		k.idx = int32(r)
	} else if r != 0 {
		return
	}
	a.mark(k, t, why)
}
