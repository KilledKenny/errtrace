package mark

import (
	"context"
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/flow"
	"github.com/KilledKenny/errtrace/internal/fmtverb"
)

// tags are the contexts in which a Mark holds, in three planes that say
// how the value came to hold the Subject, and one more bit.
//
// By value (the low third): the value is the Subject itself, or an
// immutable construction over such values (fmt.Errorf, errors.Join): what
// errors.Is says about it never changes. By type (the middle third): the
// value has a type that wraps the Subject, comes from memory or code the
// pass does not see, or is derived from such a value: what errors.Is says
// about it may change after a check. Held (the high third): the value is
// an interface or a reflect.Value that came out of reflection or from
// outside the program, and may be a composite (a struct, a slice, a
// pointer) with the Subject somewhere in its memory. The errors.Is guard
// drops only the by-value plane; only a held Mark makes a type assertion
// to a composite type mark the memory of that type (an error that flows
// through an any is not a slice).
//
// In each plane, bit 0 is free (true in any calling context) and bit i+1
// is "when parameter i of the enclosing function holds the Subject" (in
// that plane). A plane with free is just free.
//
// The last bit, boxed, is a held Mark of which more is known: the value
// may be a pointer to, or a slice of, memory that holds a marked value
// itself (what Value.Addr and reflect.Append make of a Value that holds
// one). Asserted to a pointer or slice type, it marks the memory of the
// element type and nothing deeper. It has no calling context: it is true
// in any.
type tags uint64

// planeBits is the width of a plane.
const planeBits = 21

const (
	// free is the by-value free tag, freeT the by-type one, freeH the
	// held one.
	free  tags = 1
	freeT tags = 1 << planeBits
	freeH tags = 1 << (2 * planeBits)
	// boxed is the boxed tag.
	boxed tags = 1 << (3 * planeBits)
	// planeV, planeT and planeH are the bits of the three planes.
	planeV tags = 1<<planeBits - 1
	planeT tags = planeV << planeBits
	planeH tags = planeV << (2 * planeBits)
	// anyFree is free in any plane, or boxed.
	anyFree = free | freeT | freeH | boxed
)

// planeShifts are the positions of the planes.
var planeShifts = [...]uint{0, planeBits, 2 * planeBits}

// maxParamTag is the number of parameters that have a tag of their own;
// a Mark that depends on a later parameter is free.
const maxParamTag = planeBits - 1

// paramTag returns the by-value tag of parameter i.
func paramTag(i int) tags {
	if i < 0 || i >= maxParamTag {
		return free
	}
	return 1 << uint(i+1)
}

// paramBits returns the tags of parameter i in every plane.
func paramBits(i int) tags {
	t := paramTag(i)
	return t | t<<planeBits | t<<(2*planeBits)
}

// toT moves the by-value tags of t to the by-type plane.
func toT(t tags) tags { return (t&planeV)<<planeBits | t&^planeV }

// toH moves every tag of t to the held plane: a boxed Mark is held in
// any context.
func toH(t tags) tags {
	h := (t&planeV)<<(2*planeBits) | (t&planeT)<<planeBits | t&planeH
	if t&boxed != 0 {
		h |= freeH
	}
	return h
}

// planesOf returns free in every plane in which t has a tag (and boxed,
// if t is): what a location gets when a value with the tags t is stored
// to it.
func planesOf(t tags) tags {
	p := t & boxed
	for _, sh := range planeShifts {
		if t&(planeV<<sh) != 0 {
			p |= free << sh
		}
	}
	return p
}

// paramSeed returns the tags that parameter p of a callee gets from an
// argument with the tags t: its Param tag in each plane t has a tag in
// (and boxed, if t is).
func paramSeed(p int, t tags) tags {
	out := t & boxed
	for _, sh := range planeShifts {
		if t&(planeV<<sh) != 0 {
			out |= paramTag(p) << sh
		}
	}
	return out
}

// throughParam returns the tags a call gets from a callee result with the
// Param(p) bits ret (see paramBits) when the argument has the tags arg:
// the argument's tags, in the planes the result depends on the parameter
// in. (A boxed result is free: it marks every call, see retChanged.)
func throughParam(ret, arg tags) tags {
	var out tags
	for _, sh := range planeShifts {
		if ret&(planeV<<sh) != 0 {
			out |= arg & (planeV << sh)
		}
	}
	return out
}

// normTags makes a plane that has free just free.
func normTags(t tags) tags {
	for _, sh := range planeShifts {
		if t&(free<<sh) != 0 {
			t = t&^(planeV<<sh) | free<<sh
		}
	}
	return t
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
	// locVarargs is the elements of the slices passed to the slice
	// parameter v of a function that are argument arrays of static
	// variadic calls (directly, or passed on from parameter to
	// parameter): memory only that function reads, unless it, or a
	// function the slice is passed on to, lets the slice escape.
	locVarargs
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
	kSeedCaller
	kSeedOpen
	kReflectArg
	kReflectRet
	kOutside
	kReflectFresh
	kSeedReflect
	kFlood
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
	// marked is free in the planes in which the location is marked.
	marked  tags
	why     reason
	readers []key
}

// candidate is a possible wrapper type that the program converts to an
// interface. Candidates are keyed by the type itself (typeIDs.raw), never
// by its conversion class.
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

	typeSeeds []ssa.Value
	// reflectMade are the results of the calls of package reflect that
	// make a value of a type chosen at run time (see reflectMakes).
	reflectMade []key
	opaque      []opaqueSeed
	varargs     map[*ssa.Alloc]*ssa.Call
	// varargsParam maps the argument array of a static call of a
	// variadic function to the locVarargs of the callee's parameter;
	// forwards are the locVarargs a function passes its own on to
	// (f(args...)), backwards the reverse, vaEsc caches vaEscapes and
	// passedEsc passedOnEscapes.
	varargsParam map[*ssa.Alloc]loc
	forwards     map[loc][]loc
	backwards    map[loc][]loc
	vaEsc        map[loc]bool
	passedEsc    map[loc]bool
	// taken are the functions used as values, not only called: the
	// only functions a call of a function value can run.
	taken map[*ssa.Function]bool
	// staticSites are the calls that can only run the function (see
	// soleCallee); closedMemo caches closed.
	staticSites map[*ssa.Function][]ssa.CallInstruction
	closedMemo  map[loc]int8
	asPointees  []types.Type
	asSeen      map[int32]bool
	asUnknown   tags

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
	// modelReflect says that the calls of package reflect are modelled
	// instead of following their bodies.
	modelReflect bool

	// heldBy is set by isWrapper and holds (see isWrapper).
	heldBy ref

	// open are the packages code outside the program uses (see
	// Options.Open); openLoads are the loads that code may have written.
	open      map[*ssa.Package]bool
	openLoads []key
	// recovers are the calls of recover, by function.
	recovers []recoverCall
	// locOrder are the locations in the order they were first named.
	locOrder []loc
	// unfollowed are the stores through reflection of a marked value
	// whose destination the pass cannot tell (see reflectDest); dests
	// caches the walks back from destinations and destIDs numbers the
	// values they look at.
	unfollowed map[ssa.CallInstruction]bool
	// flood says that this is the flooding pass (see Result.Flood): a
	// store that is not followed marks every location such a store can
	// write. flooded are the planes in which that happened, and floodWhy
	// is the first store that did it.
	flood    bool
	flooded  tags
	floodWhy reason
	// reflectReads are the calls of package reflect in the scanned
	// functions that are passed a reflect.Value (see reflectReads), in
	// the order of the scan: in the flooding pass, what they hand out
	// may be read from flooded memory (see floodLocs).
	reflectReads []*ssa.Call
	// boxes are the calls of package reflect that return a composite
	// around what they are passed (see reflectBoxes).
	boxes map[*ssa.Call]bool
	// typedRead caches typedReads.
	typedRead map[*ssa.Call]bool
	dests     map[destKey]*reflectDest
	destIDs   map[ssa.Value]uint32
	// reflects says that the program calls functions through reflection
	// (see reflectCall); reflectCallers are the functions that do,
	// reflectValues the memory of type reflect.Value, and reflectSeeded
	// the planes in which reflectArgsMarked ran.
	reflects bool
	// reflectRoots are the static types of the values that the called
	// reflect.Values were made from (reflect.ValueOf(f).Call), and
	// reflectAny says that one of them is not known: then any exported
	// method and any function whose value is taken may be called.
	reflectRoots   []types.Type
	reflectAny     bool
	reflectTargets map[*ssa.Function]bool
	reflectCallers []*ssa.Function
	reflectValues  loc
	reflectSeeded  tags
	// reflectArgs are the types of the composite values passed to
	// reflect.ValueOf.
	reflectArgs []reflectArg
	// guards caches deadBlocks; byValue says that the Sentinel's value is
	// one fixed value (see setupSubject): its Marks are then by value,
	// and guardOK says that a failed errors.Is excludes it.
	guards  map[ssa.Value][]*ssa.BasicBlock
	byValue bool
	guardOK bool
	// targets are the Is and As methods that targetMatches found to
	// match; noTargets are those it found not to, once the pass is
	// settled (no Mark is added any more).
	targets   map[*ssa.Function]*targetMatch
	noTargets map[*ssa.Function]bool
	settled   bool
	// dynNilable are the pointer types the Sentinel's value may have
	// while another pointer of the type may be equal to it: it may be
	// nil, the program may have kept a copy of the pointer before it
	// became the Sentinel (see typeWalk), or the program converts
	// pointers of another type to the type (convertedTo, the pointer
	// types that are the result of such a conversion in scanned code).
	dynNilable  []types.Type
	convertedTo []types.Type

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

type recoverCall struct {
	fn *ssa.Function
	k  key
}

func newAnalysis(prog *ssa.Program, res Callees, subj Subject) *analysis {
	return &analysis{
		prog:           prog,
		res:            res,
		subj:           subj,
		fnIndex:        make(map[*ssa.Function]int32),
		ty:             newTypeIDs(),
		cellParent:     make(map[ssa.Value]ssa.Value),
		cellEsc:        make(map[ssa.Value]bool),
		globalEsc:      make(map[*ssa.Global]bool),
		globalStore:    make(map[*ssa.Global][]*ssa.Store),
		fieldEsc:       make(map[fieldKey]bool),
		fieldOwner:     make(map[fieldKey]types.Type),
		locs:           make(map[loc]*locInfo),
		locTypes:       make(map[loc]types.Type),
		marks:          make(map[key]*info),
		rets:           make(map[*ssa.Function][]info),
		candByID:       make(map[int32]*candidate),
		varargs:        make(map[*ssa.Alloc]*ssa.Call),
		varargsParam:   make(map[*ssa.Alloc]loc),
		forwards:       make(map[loc][]loc),
		backwards:      make(map[loc][]loc),
		vaEsc:          make(map[loc]bool),
		passedEsc:      make(map[loc]bool),
		taken:          make(map[*ssa.Function]bool),
		staticSites:    make(map[*ssa.Function][]ssa.CallInstruction),
		closedMemo:     make(map[loc]int8),
		asSeen:         make(map[int32]bool),
		siteOrd:        make(map[ssa.CallInstruction]int64),
		calleesCache:   make(map[ssa.CallInstruction][]*ssa.Function),
		callersCache:   make(map[*ssa.Function][]ssa.CallInstruction),
		models:         make(map[*ssa.Function]model),
		reflectTargets: make(map[*ssa.Function]bool),
		unfollowed:     make(map[ssa.CallInstruction]bool),
		boxes:          make(map[*ssa.Call]bool),
		typedRead:      make(map[*ssa.Call]bool),
		dests:          make(map[destKey]*reflectDest),
		destIDs:        make(map[ssa.Value]uint32),
		guards:         make(map[ssa.Value][]*ssa.BasicBlock),
		targets:        make(map[*ssa.Function]*targetMatch),
		noTargets:      make(map[*ssa.Function]bool),
		markableCache:  make(map[types.Type]bool),
		wrapperCache:   make(map[types.Type]bool),
		typeSeedCache:  make(map[types.Type]bool),
		assertCache:    make(map[types.Type]bool),
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
	a.indexStatic()
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
				a.mark(key{s, 0}, freeT, reason{kind: kSeedWrapper, from: a.heldBy, site: s.(ssa.Instruction), typ: c.typ})
			}
		}
		// A composite value that holds a marked value and is passed to
		// package reflect (reflect.ValueOf(s)) may come out of the call:
		// by type, for what the program stored in its memory (a typed
		// read finds that in the memory itself), and held, if its
		// memory holds a composite that no store of the program wrote.
		rest := a.reflectArgs[:0]
		reached := make(map[types.Type]reach)
		for _, ra := range a.reflectArgs {
			r, ok := reached[ra.typ]
			if !ok {
				r = a.reaches(ra.typ)
				reached[ra.typ] = r
			}
			var t tags
			if r.planes != 0 {
				t |= freeT
			}
			if r.planes&(freeH|boxed) != 0 {
				// (A box in its memory: held, which says less.)
				t |= freeH
			}
			if t &^= ra.done; t != 0 {
				ra.done |= t
				a.reflectResults(ra.call, t, reason{kind: kThrough, from: r.by, site: ra.call, fn: ra.call.Call.StaticCallee()})
				changed = true
			}
			if ra.done != freeT|freeH {
				rest = append(rest, ra)
			}
		}
		a.reflectArgs = rest
		if !changed {
			a.settled = true
			return nil
		}
	}
}

// reflectCall reports whether fn calls a function or method chosen at run
// time through reflection: reflect.Value.Call, CallSlice, Method or
// MethodByName.
func reflectCall(fn *ssa.Function) bool {
	if fn.Pkg == nil || fn.Pkg.Pkg.Path() != "reflect" || fn.Signature.Recv() == nil {
		return false
	}
	switch fn.Name() {
	case "Call", "CallSlice", "Method", "MethodByName":
		return true
	}
	return false
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
	// helpers are the functions the bodies of the intrinsics refer to,
	// and referenced the functions that scanned bodies refer to.
	helpers := make(map[*ssa.Function]bool)
	referenced := make(map[*ssa.Function]bool)
	// refs are the functions each function refers to: its static
	// callees, closures and function values.
	refs := make(map[*ssa.Function][]*ssa.Function)
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
			for _, b := range fn.Blocks {
				for _, instr := range b.Instrs {
					rands = instr.Operands(rands[:0])
					for _, r := range rands {
						if r == nil || *r == nil {
							continue
						}
						if f, ok := (*r).(*ssa.Function); ok {
							helpers[f] = true
						}
					}
				}
			}
			continue
		}
		if a.reflectBody(fn) {
			// Package reflect is modelled at its calls (see
			// modelReflect): its bodies move values through unsafe
			// memory, where the pass loses or floods them.
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
						referenced[f] = true
						refs[fn] = append(refs[fn], f)
						if site, isCall := instr.(ssa.CallInstruction); !isCall || r != &site.Common().Value {
							a.taken[f] = true
						}
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
	// An unexported function that only the body of an intrinsic refers
	// to (fmt.errorf, which does the work of fmt.Errorf) is part of that
	// body: nothing the pass follows can call it.
	kept := list[:0]
	for _, n := range list {
		if helpers[n.fn] && !referenced[n.fn] && n.fn.Signature.Recv() == nil && !token.IsExported(n.fn.Name()) {
			continue
		}
		kept = append(kept, n)
	}
	list = kept
	// Dead code: in a whole program, only what the roots reach can run.
	// What they reach is taken with the call edges of the pass itself:
	// from the functions the Callees report reachable, every function a
	// live function refers to (a static callee, a closure, a function
	// value) and every callee of its dynamic calls (the CHA callees
	// where VTA has none, as for a method that only reflection puts
	// behind an interface). If live code calls through reflect.Value,
	// every exported method may run.
	if reach, ok := a.res.(interface{ Reachable(*ssa.Function) bool }); ok {
		inList := make(map[*ssa.Function]bool, len(list))
		for _, n := range list {
			inList[n.fn] = true
		}
		live := make(map[*ssa.Function]bool)
		var stack []*ssa.Function
		push := func(fn *ssa.Function) {
			if fn != nil && inList[fn] && !live[fn] {
				live[fn] = true
				stack = append(stack, fn)
			}
		}
		for _, n := range list {
			if reach.Reachable(n.fn) {
				push(n.fn)
			}
		}
		reflects := false
		for len(stack) > 0 {
			fn := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, g := range refs[fn] {
				push(g)
			}
			for _, b := range fn.Blocks {
				for _, instr := range b.Instrs {
					site, ok := instr.(ssa.CallInstruction)
					if !ok {
						continue
					}
					sc := site.Common().StaticCallee()
					if sc == nil {
						for _, g := range a.callees(site) {
							push(g)
						}
						continue
					}
					if !reflects && reflectCall(sc) {
						reflects = true
						for _, n := range list {
							if obj := n.fn.Object(); n.fn.Signature.Recv() != nil && obj != nil && obj.Exported() {
								push(n.fn)
							}
						}
					}
				}
			}
		}
		kept = list[:0]
		for _, n := range list {
			if live[n.fn] {
				kept = append(kept, n)
			}
		}
		list = kept
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
					a.noteConverted(x.X.Type(), x.Type())
				case *ssa.Convert:
					a.ty.unify(x.X.Type(), x.Type(), 0)
					a.noteConverted(x.X.Type(), x.Type())
				case *ssa.MultiConvert:
					a.ty.unify(x.X.Type(), x.Type(), 0)
					a.noteConverted(x.X.Type(), x.Type())
				case *ssa.SliceToArrayPointer:
					a.ty.unify(x.X.Type(), x.Type(), 0)
					a.noteConverted(x.X.Type(), x.Type())
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

// noteConverted records the result type to of a conversion from another
// concrete type, if it is a pointer type: the pointer it makes is one the
// program had before under a type whose values may carry no Mark (a
// *plain, a named pointer type, a slice of the array it points to), or
// whose Mark a type assertion on the way dropped. If the Sentinel's value
// has that pointer type, every pointer of the type may be it (see
// setupSubject).
func (a *analysis) noteConverted(from, to types.Type) {
	if _, isPtr := to.Underlying().(*types.Pointer); !isPtr || isInterface(from) || types.Identical(from, to) {
		return
	}
	for _, t := range a.convertedTo {
		if types.Identical(t, to) {
			return
		}
	}
	a.convertedTo = append(a.convertedTo, to)
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
		a.locOrder = append(a.locOrder, l)
		if a.flooded != 0 && floodable(l.kind) {
			// A location first named after the flood is flooded too.
			a.markLoc(l, a.flooded, a.floodWhy)
		}
	}
	return li
}

// lookLoc returns the state of the location l, or nil if nothing is
// known of it. (After a flood, a location that a store through
// reflection can write is marked even if the pass never named it.)
func (a *analysis) lookLoc(l loc) *locInfo {
	if a.flooded != 0 && floodable(l.kind) {
		return a.locInfo(l)
	}
	return a.locs[l]
}

func (a *analysis) locMarked(l loc) bool {
	li := a.lookLoc(l)
	return li != nil && li.marked != 0
}

func (a *analysis) addReader(l loc, k key) {
	li := a.locInfo(l)
	li.readers = append(li.readers, k)
}

// markLoc marks the location l in the given planes (see planesOf): every
// value loaded from it is marked, in any calling context.
func (a *analysis) markLoc(l loc, planes tags, why reason) {
	li := a.locInfo(l)
	fresh := planes & anyFree &^ li.marked
	if fresh == 0 {
		return
	}
	if li.marked == 0 {
		li.why = why
	}
	li.marked |= fresh
	for _, k := range li.readers {
		why := reason{kind: kLoad, from: ref{kind: refLoc, l: l, free: true}}
		if instr, ok := k.v.(ssa.Instruction); ok {
			why.site = instr
		}
		a.mark(k, fresh, why)
	}
	a.locMarkedHook(l, fresh)
}

// locMarkedHook does what follows from the new marks fresh of l beyond
// its readers.
func (a *analysis) locMarkedHook(l loc, fresh tags) {
	from := ref{kind: refLoc, l: l, free: true}
	switch l.kind {
	case locPanic:
		a.wakeRecovers(l, fresh)
	case locVarargs:
		for _, f := range a.forwards[l] {
			a.markLoc(f, fresh, reason{kind: kFlow, from: from})
		}
		// A function that lets its slice parameter escape makes the
		// elements memory of their type.
		if st, ok := l.v.Type().Underlying().(*types.Slice); ok && a.vaEscapes(l) {
			a.markLoc(a.typedLoc(st.Elem()), fresh, reason{kind: kFlow, from: from})
		}
	case locTyped:
		if a.reflects && l == a.reflectValues {
			a.reflectArgsMarked(fresh, from)
		}
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
		// By type: what errors.Is says about it may change.
		return freeT
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
	return ref{kind: refValue, k: k, free: t&anyFree != 0}
}

func (a *analysis) mark(k key, t tags, why reason) {
	if t == 0 || !a.canMark(k) {
		return
	}
	switch x := k.v.(type) {
	case *ssa.Const:
		return
	case *ssa.Call:
		if a.boxes[x] {
			// A pointer to what it was passed, a slice with it. Around
			// a composite with the Subject somewhere inside (or around
			// another box), it is such a composite: held. Around a
			// value that is marked itself, it is a box: the memory it
			// points to holds that value, and nothing deeper is new.
			if t&(planeH|boxed) != 0 {
				t |= toH(t)
			} else {
				t |= boxed
			}
		}
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
	// (A plane that is free already takes nothing more.)
	nt := normTags(in.t | t)
	if nt == in.t {
		return
	}
	delta := nt &^ in.t
	if in.t == 0 {
		in.first = why
		in.firstFree = nt&anyFree != 0
	} else if delta&anyFree != 0 && in.freeR == nil && !in.firstFree {
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
	dead := a.deadBlocks(v)
	var prev ssa.Instruction
	for _, r := range *refs {
		if r == prev {
			// An instruction that uses v twice is listed twice.
			continue
		}
		prev = r
		a.use(k, delta, dead, r)
	}
}

// use pushes the new tags delta of the Mark of k to its user r.
func (a *analysis) use(k key, delta tags, dead []*ssa.BasicBlock, r ssa.Instruction) {
	v := k.v
	if len(dead) > 0 && guarded(dead, v, r) {
		// errors.Is(v, X) was false on every path to this use: v is not
		// X by value there. What it holds by type may have changed.
		delta &^= planeV
		if delta == 0 {
			return
		}
	}
	from := valueRef(k, delta)
	planes := planesOf(delta)
	{
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
			if x.X != v {
				break
			}
			if a.assertable(x.AssertedType) {
				a.mark(key{x, 0}, delta, reason{kind: kAssert, from: from, site: x, typ: x.AssertedType})
			}
			if !isInterface(x.AssertedType) && planes&freeH != 0 {
				// The interface may hold a composite (it came out of
				// reflection, or from outside): what was marked is
				// inside it. That goes for a composite that is an
				// error too (a *MyErr with an error in a field),
				// whether or not it can wrap the Subject.
				a.markHeld(x.AssertedType, reason{kind: kAssert, from: from, site: x, typ: x.AssertedType})
			} else if delta&boxed != 0 {
				a.markBoxed(x.AssertedType, reason{kind: kAssert, from: from, site: x, typ: x.AssertedType})
			}
		case *ssa.Store:
			if x.Val == v {
				a.store(x.Addr, from, planes, x)
			}
		case *ssa.MapUpdate:
			if x.Key == v {
				a.markLoc(a.mapLoc(locMapKey, x.Map.Type()), planes, reason{kind: kStore, from: from, site: x})
			}
			if x.Value == v {
				a.markLoc(a.mapLoc(locMapVal, x.Map.Type()), planes, reason{kind: kStore, from: from, site: x})
			}
		case *ssa.Send:
			if et := chanElem(x.Chan.Type()); x.X == v && et != nil {
				a.markLoc(a.chanLoc(et), planes, reason{kind: kStore, from: from, site: x})
			}
		case *ssa.Select:
			for _, st := range x.States {
				if et := chanElem(st.Chan.Type()); st.Send == v && et != nil {
					a.markLoc(a.chanLoc(et), planes, reason{kind: kStore, from: from, site: x})
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
					a.mark(key{cl.FreeVars[i], 0}, planes, reason{kind: kCapture, from: from, site: x, fn: cl})
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
				a.markLoc(a.panicLoc(x.Parent()), planes, reason{kind: kStore, from: from, site: x})
			}
		case ssa.CallInstruction:
			a.callUse(k, delta, x)
		}
	}
}

// store marks the locations a store of the marked value from through addr
// writes. A store into the argument array of a modelled intrinsic call is
// an operand of that call instead.
func (a *analysis) store(addr ssa.Value, from ref, planes tags, site ssa.Instruction) {
	if ia, ok := addr.(*ssa.IndexAddr); ok {
		if alloc, ok := ia.X.(*ssa.Alloc); ok {
			if call := a.varargs[alloc]; call != nil {
				a.evalIntrinsic(call)
				return
			}
			if l, ok := a.varargsParam[alloc]; ok {
				// The array is the variadic argument of one callee.
				a.markLoc(l, planes, reason{kind: kStore, from: from, site: site})
				return
			}
		}
	}
	if ia, ok := addr.(*ssa.IndexAddr); ok {
		if l, ok := sliceParam(ia.X); ok {
			// A store into an element of a slice parameter.
			a.storeSliceParam(l, planes, reason{kind: kStore, from: from, site: site})
			if a.closed(l) {
				// Its elements are in no other memory.
				return
			}
		}
	}
	locs, n := a.addrLocs(addr)
	for _, l := range locs[:n] {
		a.markLoc(l, planes, reason{kind: kStore, from: from, site: site})
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
							if ia, ok := x.X.(*ssa.IndexAddr); ok {
								if l, ok := sliceParam(ia.X); ok {
									a.addReader(l, key{x, 0})
									if a.closed(l) {
										// Its elements are in no other
										// memory.
										break
									}
								}
							}
							locs, n := a.addrLocs(x.X)
							for _, l := range locs[:n] {
								a.addReader(l, key{x, 0})
							}
							if a.openAddr(x.X) {
								a.openLoads = append(a.openLoads, key{x, 0})
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
						if a.openField(x.X.Type(), st, x.Field) {
							a.openLoads = append(a.openLoads, key{x, 0})
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
						// By the type itself: the methods of a class-mate
						// (int and syscall.Errno, for type Code int) say
						// nothing about this type.
						id := a.ty.raw(ot)
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
	a.modelReflect = true
	if t := a.subj.Type; t != nil {
		if p := pointerElem(t); p != nil {
			t = p
		}
		if n, ok := types.Unalias(t).(*types.Named); ok && n.Obj().Pkg() != nil {
			switch n.Obj().Pkg().Path() {
			case "reflect":
				a.modelReflect = false
			case "errors", "fmt", "braces.dev/errtrace":
				// The Subject is a type the intrinsics make: follow
				// their bodies.
				a.modelIntrinsics = false
			}
		}
	}
}

// reflectBody reports whether fn is a function of package reflect whose
// body is not scanned because its calls are modelled. (They are scanned
// when the Subject is a type of package reflect itself.)
func (a *analysis) reflectBody(fn *ssa.Function) bool {
	for f := fn; f != nil; f = f.Parent() {
		pkg := f.Pkg
		if pkg == nil && f.Origin() != nil {
			// An instance of a generic function (reflect.TypeAssert).
			pkg = f.Origin().Pkg
		}
		if pkg != nil {
			return a.modelReflect && pkg.Pkg.Path() == "reflect"
		}
	}
	return false
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
	a.dynTypes, a.dynUnknown, a.dynNilable = w.types, w.unknown, w.nilable
	if w.copied {
		// The program may hold the pointer the Sentinel is in a place
		// no Mark comes to: every pointer of its type may be it.
		for _, d := range w.types {
			if _, isPtr := d.Underlying().(*types.Pointer); isPtr {
				a.dynNilable = append(a.dynNilable, d)
			}
		}
	}
	// So too if the program converts a pointer of another type to the
	// type of the Sentinel's pointer: the Sentinel's own pointer may have
	// gone through that other type, where no Mark follows it
	// ((*T)((*plain)(ErrX.(*T)))). The conversion makes a type seed, not
	// a marked value: the scan is of the whole program, whether or not a
	// Mark comes near the conversion.
	for _, d := range w.types {
		for _, t := range a.convertedTo {
			if types.Identical(d, t) {
				a.dynNilable = append(a.dynNilable, d)
			}
		}
	}
	// The Sentinel is held by value only if what its variables hold is
	// one value that never changes: every variable of the class is only
	// initialized, with a value that holds nothing that can change (see
	// fixedValue). Otherwise its Marks are by type, and a failed
	// errors.Is(v, X) excludes nothing.
	a.byValue = true
	fw := &fixedWalk{a: a, seen: make(map[any]bool)}
	for _, h := range a.class {
		a.byValue = a.byValue && fw.global(h, 0)
	}
	a.guardOK = a.byValue
}

// fixedWalk decides whether the value of a Sentinel is fixed: errors.Is
// says the same about it, and about what is built over it, at any time.
// That holds for a value made by errors.New, by fmt.Errorf with a
// constant format whose %w operands are fixed, and for a value of a
// comparable type that can hold no value that carries a Mark (errors.Is
// finds it by ==, and it has nothing inside that could change). It does
// not hold for a wrapper such as &W{inner: err}: the program may change
// what it wraps after a check.
type fixedWalk struct {
	a    *analysis
	seen map[any]bool
}

// maxFixedWalk bounds the walk through calls and variables.
const maxFixedWalk = 8

// global reports whether the program only initializes g, with fixed
// values.
func (w *fixedWalk) global(g *ssa.Global, depth int) bool {
	if w.seen[g] {
		return true
	}
	w.seen[g] = true
	if !w.a.initOnly(g) {
		return false
	}
	for _, st := range w.a.globalStore[g] {
		if !w.value(st.Val, depth+1) {
			return false
		}
	}
	return true
}

func (w *fixedWalk) value(v ssa.Value, depth int) bool {
	if depth > maxFixedWalk {
		return false
	}
	if w.seen[v] {
		return true
	}
	w.seen[v] = true
	switch x := v.(type) {
	case *ssa.MakeInterface:
		t := x.X.Type()
		if isInterface(t) || !types.Comparable(t) {
			return false
		}
		// What the value has inside: for a pointer, what it points to.
		body := t
		if p := pointerElem(t); p != nil {
			body = p
		}
		return !w.a.canHold(body, make(map[types.Type]bool), 0)
	case *ssa.ChangeInterface:
		return w.value(x.X, depth+1)
	case *ssa.Phi:
		for _, e := range x.Edges {
			if !w.value(e, depth+1) {
				return false
			}
		}
		return true
	case *ssa.UnOp:
		if g, ok := x.X.(*ssa.Global); ok && x.Op == token.MUL {
			return w.global(g, depth+1)
		}
	case *ssa.Call:
		sc := x.Call.StaticCallee()
		if sc == nil {
			return false
		}
		if name, ok := flow.Intrinsic(sc); ok {
			switch name {
			case "errors.New":
				return true
			case "fmt.Errorf":
				args := x.Call.Args
				if len(args) != 2 {
					return false
				}
				format, ok := constFormat(args[0])
				if !ok {
					return false
				}
				ops, ok := flow.VarargsOperands(args[1])
				if !ok {
					return false
				}
				for _, i := range fmtverb.Wrapped(fmtverb.Parse(format, len(ops))) {
					if i >= len(ops) || !w.value(ops[i], depth+1) {
						return false
					}
				}
				return true
			}
			return false
		}
		if sc.Blocks == nil || sc.Signature.Results().Len() != 1 {
			return false
		}
		// A function that returns the value (io/fs.errNotExist).
		for _, b := range sc.Blocks {
			if len(b.Instrs) == 0 {
				continue
			}
			if r, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return); ok {
				if len(r.Results) != 1 || !w.value(r.Results[0], depth+1) {
					return false
				}
			}
		}
		return true
	}
	return false
}

// maxCanHold bounds the walk of canHold through unnamed types.
const maxCanHold = 16

// canHold reports whether a value of type t can have, in its own storage
// or behind its pointers, slices, maps and channels, a value that carries
// a Mark or that the pass cannot look into: an interface, a value of a
// type that can be an error, a reflect.Value, a function, an unsafe
// pointer.
func (a *analysis) canHold(t types.Type, seen map[types.Type]bool, depth int) bool {
	if depth > maxCanHold {
		return true
	}
	if seen[t] {
		return false
	}
	if _, ok := types.Unalias(t).(*types.Named); ok {
		seen[t] = true
	}
	inner := func(e types.Type) bool {
		return a.markable(e) || a.canHold(e, seen, depth+1)
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return u.Kind() == types.UnsafePointer
	case *types.Pointer:
		return inner(u.Elem())
	case *types.Slice:
		return inner(u.Elem())
	case *types.Array:
		return inner(u.Elem())
	case *types.Chan:
		return inner(u.Elem())
	case *types.Map:
		return inner(u.Key()) || inner(u.Elem())
	case *types.Struct:
		for i := range u.NumFields() {
			if inner(u.Field(i).Type()) {
				return true
			}
		}
		return false
	}
	// An interface, a function, a type parameter.
	return true
}

// initOnly reports whether the program only initializes the package-level
// variable g: every store to it is in its package's initializer, there
// is one, and its address is not used.
func (a *analysis) initOnly(g *ssa.Global) bool {
	if a.globalEsc[g] || len(a.globalStore[g]) == 0 || g.Pkg == nil {
		return false
	}
	init := g.Pkg.Func("init")
	for _, st := range a.globalStore[g] {
		if st.Parent() != init {
			return false
		}
	}
	return true
}

// openAddr reports whether code outside the program may have written the
// memory at addr (see Options.Open): an exported variable of an open
// package (unless it is a Sentinel by its name that the program only
// initializes), or an exported field of a struct type of an open package.
func (a *analysis) openAddr(addr ssa.Value) bool {
	if a.open == nil {
		return false
	}
	switch x := addr.(type) {
	case *ssa.Global:
		if x.Pkg == nil || !a.open[x.Pkg] || x.Object() == nil || !x.Object().Exported() {
			return false
		}
		// A Sentinel by its name that the program only initializes is
		// taken not to be reassigned from outside (see sentinelName).
		return !(a.initOnly(x) && sentinelName(x.Name()))
	case *ssa.FieldAddr:
		if st := structOf(x.X.Type()); st != nil && x.Field < st.NumFields() {
			return a.openField(x.X.Type(), st, x.Field)
		}
	}
	return false
}

func (a *analysis) openField(owner types.Type, st *types.Struct, i int) bool {
	if a.open == nil || !st.Field(i).Exported() {
		return false
	}
	if p := pointerElem(owner); p != nil {
		owner = p
	}
	pkg := st.Field(i).Pkg()
	if n, ok := types.Unalias(owner).(*types.Named); ok && n.Obj().Pkg() != nil {
		pkg = n.Obj().Pkg()
	}
	return pkg != nil && a.open[a.prog.Package(pkg)]
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
	// nilable are the pointer types among types of which another value
	// may be equal to the Sentinel's: the value may be nil (it is not an
	// allocation made for the Sentinel), or the program may have kept a
	// copy of the pointer before it became the Sentinel (see copied).
	nilable []types.Type
	// copied says that a value on the way from an allocation to the
	// Sentinel's variable has another use: the pointer may be somewhere
	// the Marks, which start at the variable, do not reach.
	copied bool
}

// addValue adds the type of the concrete value v, which the instruction
// user makes the Sentinel's value of.
func (w *typeWalk) addValue(v ssa.Value, user ssa.Instruction) {
	w.add(v.Type())
	if _, isPtr := v.Type().Underlying().(*types.Pointer); !isPtr {
		return
	}
	if alloc, isAlloc := v.(*ssa.Alloc); isAlloc && !allocCopied(alloc, user) {
		return
	}
	w.nilable = append(w.nilable, v.Type())
}

// allocCopied reports whether the pointer to the allocation alloc may be
// anywhere but in the value that user makes of it: it has a use other
// than user, a load, and the stores that fill its fields and elements.
func allocCopied(alloc *ssa.Alloc, user ssa.Instruction) bool {
	refs := alloc.Referrers()
	if refs == nil {
		return false
	}
	for _, r := range *refs {
		if r == user {
			continue
		}
		switch x := r.(type) {
		case *ssa.DebugRef:
		case *ssa.Store:
			if x.Val == ssa.Value(alloc) {
				return true
			}
		case *ssa.UnOp:
			// A load of the variable copies its value, not the pointer.
		case *ssa.FieldAddr, *ssa.IndexAddr:
			inner := x.(ssa.Value).Referrers()
			if inner == nil {
				continue
			}
			for _, u := range *inner {
				switch y := u.(type) {
				case *ssa.DebugRef, *ssa.UnOp:
				case *ssa.Store:
					if y.Val == x.(ssa.Value) {
						return true
					}
				default:
					return true
				}
			}
		default:
			return true
		}
	}
	return false
}

// only notes whether v, a value on the way to the Sentinel's variable,
// has a use other than user, the one that leads there.
func (w *typeWalk) only(v ssa.Value, user ssa.Instruction) {
	refs := v.Referrers()
	if refs == nil {
		return
	}
	for _, r := range *refs {
		if _, dbg := r.(*ssa.DebugRef); !dbg && r != user {
			w.copied = true
		}
	}
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
		if _, isPtr := elem.Underlying().(*types.Pointer); isPtr {
			stores := w.a.globalStore[g]
			nilable := len(stores) == 0 || w.a.globalEsc[g]
			for _, st := range stores {
				if alloc, isAlloc := st.Val.(*ssa.Alloc); !isAlloc || allocCopied(alloc, st) {
					nilable = true
				}
			}
			if nilable {
				w.nilable = append(w.nilable, elem)
			}
		}
		return
	}
	if w.a.globalEsc[g] {
		w.unknown = true
		return
	}
	for _, st := range w.a.globalStore[g] {
		w.value(st.Val, depth+1, st)
	}
}

// value walks back from v, which the instruction user passes on towards
// the Sentinel's variable.
func (w *typeWalk) value(v ssa.Value, depth int, user ssa.Instruction) {
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
		w.only(x, user)
		w.addValue(x.X, x)
	case *ssa.ChangeInterface:
		w.only(x, user)
		w.value(x.X, depth+1, x)
	case *ssa.ChangeType:
		if isInterface(x.X.Type()) {
			w.only(x, user)
			w.value(x.X, depth+1, x)
		} else {
			w.addValue(x, user)
		}
	case *ssa.Phi:
		w.only(x, user)
		for _, e := range x.Edges {
			w.value(e, depth+1, x)
		}
	case *ssa.UnOp:
		if g, ok := x.X.(*ssa.Global); ok && x.Op == token.MUL {
			// (Every load of the variable is a seed.)
			w.global(g, depth+1)
			return
		}
		w.unknown = true
	case *ssa.Call:
		w.only(x, user)
		w.results(x, 0, depth)
	case *ssa.Extract:
		if call, ok := x.Tuple.(*ssa.Call); ok {
			w.only(x, user)
			w.results(call, x.Index, depth)
			return
		}
		w.unknown = true
	default:
		if isInterface(v.Type()) {
			w.unknown = true
		} else {
			w.addValue(v, user)
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
			w.value(r.Results[idx], depth+1, r)
		}
	}
}

// seed makes the first Marks.
func (a *analysis) seed() {
	// The value of the Sentinel: by value, if it is fixed.
	plane := free
	if !a.byValue {
		plane = freeT
	}
	for _, g := range a.class {
		why := reason{kind: kSeedSentinel, text: Subject{Sentinel: g}.String()}
		locs, n := a.addrLocs(g)
		for _, l := range locs[:n] {
			a.markLoc(l, plane, why)
		}
		// A value the program stores into the variable is its value
		// from then on.
		for _, st := range a.globalStore[g] {
			a.mark(key{st.Val, 0}, plane, reason{kind: kSeedStored, site: st, text: why.text})
		}
	}
	a.seedOpen()
	for _, v := range a.typeSeeds {
		a.mark(key{v, 0}, freeT, reason{kind: kSeedType, site: v.(ssa.Instruction), typ: seedOperandType(v)})
	}
	if a.seedsByType() {
		// A value that reflection makes of a type it is told at run
		// time (reflect.New(t), reflect.Zero(t), v.Convert(t)) may be
		// of a type every value of which is marked. No conversion to
		// an interface shows it: Value.Interface does that, or a store
		// through reflection into an interface variable.
		for _, k := range a.reflectMade {
			call := k.v.(*ssa.Call)
			a.mark(k, freeT, reason{kind: kSeedReflect, site: call, fn: call.Call.StaticCallee()})
		}
	}
	for _, o := range a.opaque {
		res := o.call.Call.Signature().Results()
		for i := range res.Len() {
			a.markResult(o.call, i, freeT, reason{kind: kSeedOpaque, site: o.call, text: o.text})
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
