package summary

import (
	"fmt"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/KilledKenny/errtrace/internal/errset"
	"github.com/KilledKenny/errtrace/internal/flow"
	"github.com/KilledKenny/errtrace/internal/resolve"
)

// This file models struct fields as field locations. A field location is
// one field of one struct type, for the whole program: its value is the
// join of every value stored to that field of any value of that type. A
// load of the field (a FieldRef of flow) is replaced by the location's
// summary.
//
// The model is sound only while every way the field can be written is
// seen. The writers are found by one scan of every function with a body
// that the Resolver calls reachable:
//
//   - a Store through the field's address (p.f = v, and the fields of a
//     composite literal, which SSA stores one by one) is a writer;
//   - a struct stored, passed, sent or returned whole (*p = v) carries
//     its fields with it. They are instances of the same locations, so
//     they hold what the writers of those locations stored, or what
//     another whole struct brought, and so on back to a writer: a copy
//     adds no value to a location, wherever the struct comes from, as
//     long as the code that built it is seen. A struct built by code that
//     is not seen is covered by the exposure rule below;
//   - two struct types converted to one another share their locations;
//   - the address of the field used other than to load from it or store
//     to it (&p.f passed on) lets code write it that the scan does not
//     attribute to the field: the location keeps an Unresolved value;
//   - a struct that reflection, unsafe code or a function errtrace does
//     not see (one without a body, or the unknown callee of a dynamic call
//     with no Candidates) may reach or build (see fieldIndex.box, convert
//     and call) may be written by it: its locations keep an Unresolved
//     value;
//   - if a function the Query was asked about has no caller in the
//     reachable program and is not main or an init function, code outside
//     the reachable program calls it and may write any field: every
//     location then keeps an Unresolved value.
//
// A location has no call context, so a writer's stored value that is a
// parameter of the writer (the constructor pattern) is resolved by joining
// the arguments of every call site of the writer, up to maxCallerDepth
// callers up; see paramResolver.

// maxCallerDepth is how many levels of callers a field location follows to
// resolve a stored value that is a parameter.
const maxCallerDepth = 3

// fieldKey names one field of one struct type. A field of a named struct
// type is named by the field variable of the type's generic origin, which
// the types declared as another named struct type (type A B) share with
// it; a field of an unnamed struct type by the type's string and the
// field's index, which identical struct types share.
type fieldKey struct {
	v     *types.Var
	anon  string
	index int
}

// fieldKeyOf returns the key of field i of the struct type t.
func fieldKeyOf(t types.Type, i int) (fieldKey, bool) {
	t = types.Unalias(t)
	if n, ok := t.(*types.Named); ok {
		st, ok := n.Origin().Underlying().(*types.Struct)
		if !ok || i >= st.NumFields() {
			return fieldKey{}, false
		}
		return fieldKey{v: st.Field(i)}, true
	}
	if st, ok := t.(*types.Struct); ok && i < st.NumFields() {
		return fieldKey{anon: types.TypeString(st, nil), index: i}, true
	}
	return fieldKey{}, false
}

// structOf returns the struct type (named or not) that t is, or that t
// points to if deref is set, or nil.
func structOf(t types.Type, deref bool) types.Type {
	t = types.Unalias(t)
	if deref {
		p, ok := t.Underlying().(*types.Pointer)
		if !ok {
			return nil
		}
		t = types.Unalias(p.Elem())
	}
	if _, ok := t.Underlying().(*types.Struct); !ok {
		return nil
	}
	if _, ok := t.(*types.TypeParam); ok {
		return nil
	}
	return t
}

// holeFlags are the ways a field location may be written that the scan
// does not attribute to a writer.
type holeFlags uint8

const (
	// holeEscapes: the field's address is used other than to load from it
	// or store to it.
	holeEscapes holeFlags = 1 << iota
	// holeExposed: reflection, unsafe code or a function errtrace does not
	// see may reach a struct holding the field.
	holeExposed
)

// holeReasons are the details of the Unresolved values of holeFlags, after
// "field <f> of <T> ", in the order they are reported.
var holeReasons = []struct {
	flag   holeFlags
	reason string
}{
	{holeEscapes, "may be written through an escaped pointer"},
	{holeExposed, "may be written by reflection, unsafe code or a function that is not seen"},
}

// openWorldReason is the detail of the Unresolved value every location
// keeps when the Query's roots have callers outside the reachable program.
const openWorldReason = "may be written by code that is not reachable from the Query's roots"

// location is a field location: a node of the demand graph.
type location struct {
	// name is "field <f> of <T>", for details.
	name string
	// writers are the Stores through the field's address, in scan order.
	writers []*ssa.Store
	// flags are the ways the field may be written that are not writers.
	flags holeFlags
	// demand caches what the writers' stored values depend on.
	demand *demand
	// sum is the location's summary, or nil until it is solved.
	sum *locSummary
}

// locSummary is the summary of a field location.
type locSummary struct {
	// facts are the values the field can hold. Each leaf's sources are
	// Stored sources without a Site, which the load fills in.
	facts []Fact
	// holes are the Unresolved values every load of the field keeps.
	holes []locHole
}

// locHole is one reason a field location is not completely known.
type locHole struct {
	kind   errset.UnresolvedKind
	reason string
}

// rawLocation is what the scan found for one fieldKey, before the keys
// that conversions unite are merged.
type rawLocation struct {
	name    string
	writers []*ssa.Store
	flags   holeFlags
}

// fieldIndex is the index of the field locations of the reachable
// program, with their writers, and of the call sites of its functions.
type fieldIndex struct {
	res resolve.Resolver
	// parent unites the keys of struct types converted to one another
	// (union-find).
	parent map[fieldKey]fieldKey
	raw    map[fieldKey]*rawLocation
	// order lists the keys of raw in the order the scan met them.
	order []fieldKey
	// locs are the locations by the root of their keys.
	locs map[fieldKey]*location
	// callers are the call sites that may call each function, in scan
	// order.
	callers map[*ssa.Function][]ssa.CallInstruction
	// addrTaken are the functions used as a value other than as the
	// callee of a call, and dynCalled those that are a Candidate of a
	// dynamic call.
	addrTaken, dynCalled map[*ssa.Function]bool
	// exposed are the types whose locations were given holeExposed.
	exposed map[types.Type]bool
}

// index returns the Solver's field index, building it from prog if it is
// not built yet.
func (s *Solver) index(prog *ssa.Program) *fieldIndex {
	if s.fields != nil {
		return s.fields
	}
	ix := &fieldIndex{
		res:       s.res,
		parent:    make(map[fieldKey]fieldKey),
		raw:       make(map[fieldKey]*rawLocation),
		locs:      make(map[fieldKey]*location),
		callers:   make(map[*ssa.Function][]ssa.CallInstruction),
		addrTaken: make(map[*ssa.Function]bool),
		dynCalled: make(map[*ssa.Function]bool),
		exposed:   make(map[types.Type]bool),
	}
	for _, fn := range universe(prog, s.res) {
		ix.scan(fn)
	}
	for _, k := range ix.order {
		r := ix.raw[k]
		l := ix.at(k)
		if l.name == "" {
			l.name = r.name
		}
		l.writers = append(l.writers, r.writers...)
		l.flags |= r.flags
	}
	s.fields = ix
	return ix
}

// universe returns the functions with a body that res calls reachable, in
// a deterministic order.
func universe(prog *ssa.Program, res resolve.Resolver) []*ssa.Function {
	var fns []*ssa.Function
	if f, ok := res.(interface{ Functions() []*ssa.Function }); ok {
		fns = f.Functions()
	} else {
		for fn := range ssautil.AllFunctions(prog) {
			fns = append(fns, fn)
		}
		sort.Slice(fns, func(i, j int) bool {
			if a, b := fns[i].String(), fns[j].String(); a != b {
				return a < b
			}
			return fns[i].Pos() < fns[j].Pos()
		})
	}
	var out []*ssa.Function
	for _, fn := range fns {
		if fn != nil && fn.Blocks != nil && res.Reachable(fn) {
			out = append(out, fn)
		}
	}
	return out
}

func (ix *fieldIndex) find(k fieldKey) fieldKey {
	for {
		p, ok := ix.parent[k]
		if !ok || p == k {
			return k
		}
		k = p
	}
}

func (ix *fieldIndex) union(a, b fieldKey) {
	if ra, rb := ix.find(a), ix.find(b); ra != rb {
		ix.parent[rb] = ra
	}
}

// fieldDescription returns "field <f> of <T>" for field i of the struct
// type t.
func fieldDescription(t types.Type, i int) string {
	name := fmt.Sprintf("#%d", i)
	if st, ok := t.Underlying().(*types.Struct); ok && i < st.NumFields() {
		name = st.Field(i).Name()
	}
	return fmt.Sprintf("field %s of %s", name, types.TypeString(t, nil))
}

// rawAt returns the scan's record of key k, which is field i of the
// struct type t.
func (ix *fieldIndex) rawAt(k fieldKey, t types.Type, i int) *rawLocation {
	r := ix.raw[k]
	if r == nil {
		r = &rawLocation{name: fieldDescription(t, i)}
		ix.raw[k] = r
		ix.order = append(ix.order, k)
	}
	return r
}

// at returns the location of key k: one that nothing was found for has no
// writers, so its field is always the zero value.
func (ix *fieldIndex) at(k fieldKey) *location {
	root := ix.find(k)
	l := ix.locs[root]
	if l == nil {
		l = new(location)
		ix.locs[root] = l
	}
	return l
}

// scan indexes the instructions of fn.
func (ix *fieldIndex) scan(fn *ssa.Function) {
	var buf [10]*ssa.Value
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch instr := instr.(type) {
			case *ssa.FieldAddr:
				ix.fieldAddr(instr)
			case *ssa.MakeInterface:
				ix.box(instr)
			case *ssa.ChangeType:
				ix.convert(fn, instr.X.Type(), instr.Type())
			case *ssa.Convert:
				ix.convert(fn, instr.X.Type(), instr.Type())
			case *ssa.MakeClosure:
				ix.closure(instr)
			}
			site, isCall := instr.(ssa.CallInstruction)
			if isCall {
				ix.call(site)
			}
			if _, ok := instr.(*ssa.DebugRef); ok {
				// Debug information names a function without using it.
				continue
			}
			mc, _ := instr.(*ssa.MakeClosure)
			for _, op := range instr.Operands(buf[:0]) {
				f, ok := (*op).(*ssa.Function)
				if !ok || (isCall && op == &site.Common().Value) || (mc != nil && op == &mc.Fn) {
					continue
				}
				ix.addrTaken[f] = true
			}
		}
	}
}

// fieldAddr indexes the uses of the address of a field: the stores to it
// are the field's writers, and any use other than a store to it or a load
// from it lets the address escape.
func (ix *fieldIndex) fieldAddr(fa *ssa.FieldAddr) {
	st := structOf(fa.X.Type(), true)
	if st == nil {
		return
	}
	k, ok := fieldKeyOf(st, fa.Field)
	if !ok {
		return
	}
	refs := fa.Referrers()
	if refs == nil {
		return
	}
	r := ix.rawAt(k, st, fa.Field)
	for _, ref := range *refs {
		switch ref := ref.(type) {
		case *ssa.Store:
			if ref.Addr != fa || ref.Val == fa {
				r.flags |= holeEscapes
				continue
			}
			r.writers = append(r.writers, ref)
		case *ssa.UnOp:
			if ref.Op != token.MUL {
				r.flags |= holeEscapes
			}
		case *ssa.DebugRef:
		default:
			r.flags |= holeEscapes
		}
	}
}

// expose gives holeExposed to the locations of every struct that code
// holding a value of type t can reach: through pointers, slices, arrays,
// maps, channels and struct fields. It stops at interfaces and functions,
// whose dynamic values the type does not say.
func (ix *fieldIndex) expose(t types.Type) {
	t = types.Unalias(t)
	if ix.exposed[t] {
		return
	}
	ix.exposed[t] = true
	if _, param := t.(*types.TypeParam); param {
		return
	}
	switch u := t.Underlying().(type) {
	case *types.Struct:
		for i := range u.NumFields() {
			if k, ok := fieldKeyOf(t, i); ok {
				ix.rawAt(k, t, i).flags |= holeExposed
			}
			ix.expose(u.Field(i).Type())
		}
	case *types.Pointer:
		ix.expose(u.Elem())
	case *types.Slice:
		ix.expose(u.Elem())
	case *types.Array:
		ix.expose(u.Elem())
	case *types.Chan:
		ix.expose(u.Elem())
	case *types.Map:
		ix.expose(u.Key())
		ix.expose(u.Elem())
	}
}

// box indexes a conversion of a concrete value to an interface without
// methods (any). Such a value may be handed to code that inspects it by
// reflection and writes what it points to (a decoder). The rule is simple
// and conservative: the structs the value's type can reach are exposed
// unless every use of the interface value is a comparison, a type
// assertion, a panic, or an argument of a call whose Candidates are all
// known not to write through their arguments (see readOnlyCallee). A
// value stored, returned, sent or captured as any is exposed: so is one
// put in a container (a sync.Pool, a context), from which it may come
// back out as an any that is decoded into. A conversion made inside one
// of the read-only packages themselves (the target that errors.AsType
// hands to As methods) is not exposed, like the same value passed to
// that package from outside.
//
// A conversion to an interface with methods (an error, a fmt.Stringer) is
// followed, in its function, to where the value is converted on to any:
// that any is then judged by the same rule.
//
// What the rule does not see, and so does not expose: a value that is
// already an interface when the function gets it (a parameter, a call
// result, a load) and that it hands to a decoder. The rule looks at the
// conversions from concrete types, not at every use of an interface
// value: the conversion that made the interface exposes the type, unless
// it was to an interface with methods that left its function as such (an
// error returned and later decoded into by its receiver).
func (ix *fieldIndex) box(mi *ssa.MakeInterface) {
	iface, ok := mi.Type().Underlying().(*types.Interface)
	if !ok || readOnlyCallee(mi.Parent()) {
		return
	}
	seen := make(map[ssa.Value]bool)
	if (iface.NumMethods() == 0 && ix.boxEscapes(mi, seen)) || (iface.NumMethods() != 0 && ix.madeAnyEscapes(mi, seen)) {
		ix.expose(mi.X.Type())
	}
}

// madeAnyEscapes reports whether the value v of an interface type with
// methods is converted to any, in its function, and that any has a use
// that may write through it (see box).
func (ix *fieldIndex) madeAnyEscapes(v ssa.Value, seen map[ssa.Value]bool) bool {
	if seen[v] {
		return false
	}
	seen[v] = true
	refs := v.Referrers()
	if refs == nil {
		return false
	}
	for _, ref := range *refs {
		switch ref := ref.(type) {
		case *ssa.ChangeInterface:
			if iface, ok := ref.Type().Underlying().(*types.Interface); ok && iface.NumMethods() == 0 {
				if ix.boxEscapes(ref, seen) {
					return true
				}
			} else if ix.madeAnyEscapes(ref, seen) {
				return true
			}
		case *ssa.Phi:
			if ix.madeAnyEscapes(ref, seen) {
				return true
			}
		}
	}
	return false
}

// boxEscapes reports whether the interface value v has a use that may
// write through it (see box).
func (ix *fieldIndex) boxEscapes(v ssa.Value, seen map[ssa.Value]bool) bool {
	if seen[v] {
		return false
	}
	seen[v] = true
	refs := v.Referrers()
	if refs == nil {
		return false
	}
	for _, ref := range *refs {
		switch ref := ref.(type) {
		case *ssa.DebugRef, *ssa.TypeAssert, *ssa.BinOp, *ssa.Panic:
		case *ssa.ChangeInterface:
			if ix.boxEscapes(ref, seen) {
				return true
			}
		case *ssa.Phi:
			if ix.boxEscapes(ref, seen) {
				return true
			}
		case *ssa.Store:
			// An operand packed into the slice of a variadic call:
			// the uses are those of the slice.
			ia, ok := ref.Addr.(*ssa.IndexAddr)
			if !ok || ref.Val != v {
				return true
			}
			alloc, ok := ia.X.(*ssa.Alloc)
			if !ok || alloc.Comment != "varargs" {
				return true
			}
			for _, ar := range *alloc.Referrers() {
				switch ar := ar.(type) {
				case *ssa.IndexAddr, *ssa.DebugRef:
				case *ssa.Slice:
					for _, sr := range *ar.Referrers() {
						site, ok := sr.(ssa.CallInstruction)
						if _, dbg := sr.(*ssa.DebugRef); dbg {
							continue
						}
						if !ok || !ix.readOnlyCall(site) {
							return true
						}
					}
				default:
					return true
				}
			}
		default:
			site, ok := ref.(ssa.CallInstruction)
			if !ok || !ix.readOnlyCall(site) {
				return true
			}
		}
	}
	return false
}

// readOnlyCall reports whether every Candidate of site is known not to
// write through its arguments. A call with no Candidates is not.
func (ix *fieldIndex) readOnlyCall(site ssa.CallInstruction) bool {
	if b, ok := site.Common().Value.(*ssa.Builtin); ok {
		return b.Name() == "print" || b.Name() == "println"
	}
	callees := ix.res.Callees(site)
	if len(callees) == 0 {
		return false
	}
	for _, g := range callees {
		if !readOnlyCallee(g) {
			return false
		}
	}
	return true
}

// readOnlyPkgs are the packages whose functions take values as any only
// to format, log or compare them, or (runtime) to register them.
var readOnlyPkgs = map[string]bool{"fmt": true, "log": true, "log/slog": true, "errors": true, "testing": true, "runtime": true}

// readOnlyCallee reports whether fn is known not to write the fields of
// what an argument of type any points to: a function or method of fmt
// (but not its Scan functions), log, log/slog, errors, testing or runtime
// (SetFinalizer, KeepAlive, AddCleanup). errors.As and errors.AsType write
// only the pointer their target points to, as do the As methods they
// call.
func readOnlyCallee(fn *ssa.Function) bool {
	if fn == nil {
		return false
	}
	for fn.Parent() != nil {
		fn = fn.Parent()
	}
	if o := fn.Origin(); o != nil {
		fn = o
	}
	path := ""
	switch {
	case fn.Pkg != nil:
		path = fn.Pkg.Pkg.Path()
	case fn.Object() != nil && fn.Object().Pkg() != nil:
		path = fn.Object().Pkg().Path()
	}
	if !readOnlyPkgs[path] {
		return false
	}
	return path != "fmt" || !strings.Contains(strings.ToLower(fn.Name()), "scan")
}

// convert indexes a conversion from type from to type to in fn. Two
// struct types converted to one another (or pointers to them) hold the
// same fields: their locations are united. A conversion between a
// pointer and unsafe.Pointer hands the structs the pointer can reach to
// code that is not checked by types: they are exposed.
func (ix *fieldIndex) convert(fn *ssa.Function, from, to types.Type) {
	if isUnsafePointer(from) != isUnsafePointer(to) {
		ix.expose(from)
		ix.expose(to)
		return
	}
	a, b := structOf(from, false), structOf(to, false)
	if a == nil || b == nil {
		a, b = structOf(from, true), structOf(to, true)
	}
	if a == nil || b == nil {
		return
	}
	n := a.Underlying().(*types.Struct).NumFields()
	if n != b.Underlying().(*types.Struct).NumFields() {
		return
	}
	for i := range n {
		ka, oka := fieldKeyOf(a, i)
		kb, okb := fieldKeyOf(b, i)
		if oka && okb && ka != kb {
			ix.union(ka, kb)
		}
	}
}

func isUnsafePointer(t types.Type) bool {
	b, ok := types.Unalias(t).Underlying().(*types.Basic)
	return ok && b.Kind() == types.UnsafePointer
}

// call indexes a call site: it is a caller of each of its Candidates. A
// Candidate without a body, and the unknown callee of a dynamic call with
// no Candidates, may write whatever its arguments reach and build whatever
// its results hold, so those structs are exposed.
func (ix *fieldIndex) call(site ssa.CallInstruction) {
	common := site.Common()
	if _, ok := common.Value.(*ssa.Builtin); ok {
		return
	}
	dynamic := ix.res.IsDynamic(site)
	callees := ix.res.Callees(site)
	unseen := len(callees) == 0
	for _, g := range callees {
		ix.callers[g] = append(ix.callers[g], site)
		if dynamic {
			ix.dynCalled[g] = true
		}
		unseen = unseen || g.Blocks == nil
	}
	if !unseen {
		return
	}
	if common.IsInvoke() {
		ix.expose(common.Value.Type())
	}
	for _, arg := range common.Args {
		ix.expose(arg.Type())
	}
	res := common.Signature().Results()
	for i := range res.Len() {
		ix.expose(res.At(i).Type())
	}
}

// closure indexes the creation of a closure: its function is used as a
// value unless the closure is only ever called where it is made.
func (ix *fieldIndex) closure(mc *ssa.MakeClosure) {
	fn, ok := mc.Fn.(*ssa.Function)
	if !ok {
		return
	}
	refs := mc.Referrers()
	if refs == nil {
		return
	}
	for _, ref := range *refs {
		if _, ok := ref.(*ssa.DebugRef); ok {
			continue
		}
		site, ok := ref.(ssa.CallInstruction)
		if !ok || site.Common().Value != mc {
			ix.addrTaken[fn] = true
			return
		}
		for _, arg := range site.Common().Args {
			if arg == mc {
				ix.addrTaken[fn] = true
				return
			}
		}
	}
}

// callersSeen returns the call sites of fn, and whether they are all of
// its callers as far as the analysis can tell: fn has a call site, and if
// it is used as a function value, a dynamic call resolves to it. A
// function without a call site is a root, or is called by code the
// analysis does not see.
func (ix *fieldIndex) callersSeen(fn *ssa.Function) ([]ssa.CallInstruction, bool) {
	sites := ix.callers[fn]
	if len(sites) == 0 || (ix.addrTaken[fn] && !ix.dynCalled[fn]) {
		return nil, false
	}
	return sites, true
}

// location returns the field location of the FieldRef v, loaded in fn.
func (s *Solver) location(fn *ssa.Function, v errset.Value) *location {
	k, ok := fieldKeyOf(v.Type, v.Index)
	if !ok || fn.Prog == nil {
		return nil
	}
	l := s.index(fn.Prog).at(k)
	if l.name == "" {
		l.name = fieldDescription(v.Type, v.Index)
	}
	return l
}

// locationDemand returns what the summary of l depends on: what the
// values its writers store depend on in the writers, and, for the stored
// values that are parameters, what the arguments of the callers depend on
// in the callers, up to maxCallerDepth callers up.
func (s *Solver) locationDemand(l *location) *demand {
	if l.demand != nil {
		return l.demand
	}
	c := s.newCollector()
	for _, st := range l.writers {
		w := st.Parent()
		for _, f := range s.argFacts(w, st.Val, st) {
			c.value(w, f.Value)
		}
	}
	c.drain()
	// Parameters and forwarded formats found while following callers are
	// appended to the lists, so the loop goes on until both are done.
	for i, j := 0, 0; i < len(c.params) || j < len(c.errorfs); {
		if i < len(c.params) {
			p := c.params[i]
			i++
			depth := c.paramDepth[p]
			callee := p.Parent()
			sites, ok := s.fields.callersSeen(callee)
			if !ok || depth >= maxCallerDepth {
				continue
			}
			c.depth = depth + 1
			for _, site := range sites {
				op := argument(site.Common(), callee, paramIndex(p))
				if op == nil {
					continue
				}
				for _, f := range s.argFacts(site.Parent(), op, site) {
					c.value(site.Parent(), f.Value)
				}
			}
			c.drain()
			continue
		}
		id := c.errorfs[j]
		j++
		depth := c.errorfDepth[id]
		v := s.tbl.Get(id)
		callee := errorfLikeFunc(v)
		if callee == nil || depth >= maxCallerDepth {
			continue
		}
		sites, ok := s.fields.callersSeen(callee)
		if !ok {
			continue
		}
		c.depth = depth + 1
		for _, site := range sites {
			for _, f := range s.forwardedFacts(site, callee, v) {
				c.value(site.Parent(), f.Value)
			}
		}
		c.drain()
	}
	l.demand = &demand{sites: c.sites, locs: c.locs}
	return l.demand
}

// errorfLikeFunc returns the function that the ErrorfLike value v belongs
// to: the one whose fmt.Errorf call takes its format from a parameter.
func errorfLikeFunc(v errset.Value) *ssa.Function {
	if instr, ok := v.Site.(ssa.Instruction); ok {
		return instr.Parent()
	}
	return nil
}

// forwardedFacts returns the local facts, in the caller, of the ErrorfLike
// value v of callee evaluated at the call site: what fmt.Errorf makes of
// the site's format argument and operands. It returns nothing if the site
// does not pass them.
func (s *Solver) forwardedFacts(site ssa.CallInstruction, callee *ssa.Function, v errset.Value) []flow.Fact {
	c := site.Common()
	format, args := argument(c, callee, v.Index), argument(c, callee, v.ArgIndex)
	if format == nil || args == nil {
		return nil
	}
	return flow.ErrorfFacts(s.tbl, site.Parent(), site, site, c.Pos(), format, args, s.opts)
}

// paramIndex returns the index of p among its function's parameters.
func paramIndex(p *ssa.Parameter) int {
	for i, q := range p.Parent().Params {
		if q == p {
			return i
		}
	}
	return -1
}

// evalLocation computes the summary of l from the current summaries of
// what it depends on: the join, over its writers, of the stored value's
// facts expanded in the writer, with the writer's symbolic values resolved
// from its callers (see paramResolver).
func (s *Solver) evalLocation(l *location) *locSummary {
	var fs factSet
	s.passes(func(st *evalState) {
		fs = factSet{}
		r := &paramResolver{s: s, st: st, name: l.name, memo: make(map[paramAt]paramFacts), errorfs: make(map[errorfAt]paramFacts)}
		for _, store := range l.writers {
			w := store.Parent()
			var vals factSet
			for _, af := range s.argFacts(w, store.Val, store) {
				s.expand(w, af, &vals, st)
			}
			for _, f := range vals.facts {
				for _, sub := range r.resolve(w, f, 0) {
					leaves := make([][]Source, len(sub.leaves))
					for j, srcs := range sub.leaves {
						leaves[j] = []Source{{Kind: Stored, ArgFn: w, Store: store, ArgSources: srcs}}
					}
					fs.add(sub.value, leaves, sub.notes)
				}
			}
		}
	})
	sum := &locSummary{facts: fs.facts}
	if len(sum.facts) > maxValues {
		sum.facts = sum.facts[:maxValues:maxValues]
		sum.holes = append(sum.holes, locHole{errset.FixpointLimit, fmt.Sprintf("can hold more than %d values", maxValues)})
	}
	for _, h := range holeReasons {
		if l.flags&h.flag != 0 {
			sum.holes = append(sum.holes, locHole{errset.Memory, h.reason})
		}
	}
	return sum
}

// errorfAt is an ErrorfLike value reached depth callers up from a writer.
type errorfAt struct {
	id    errset.ValueID
	depth int
}

// paramAt is a parameter reached depth callers up from a writer.
type paramAt struct {
	p     *ssa.Parameter
	depth int
}

// paramFacts are the values the callers of a function pass to one of its
// parameters. Each leaf's sources are Passed sources without Then.
type paramFacts struct {
	facts []Fact
	// complete reports that every caller's argument was found.
	complete bool
}

// paramResolver resolves the symbolic values left in a value that a
// writer stores to a field location. The location has no call context, so
// a Param cannot be substituted by one caller's argument: it becomes the
// join of the arguments of every call site of its function, in which the
// callers' own Params are resolved the same way, up to maxCallerDepth
// callers up. A Param beyond that, or of a function whose callers are not
// all seen (see fieldIndex.callersSeen), and a captured variable that is
// still symbolic, become Unresolved values that say so. A format forwarded
// to fmt.Errorf from the writer's parameters (an ErrorfLike value) is
// resolved like a Param, from what fmt.Errorf makes of each caller's
// format and operands.
type paramResolver struct {
	s  *Solver
	st *evalState
	// name is the location's "field <f> of <T>".
	name string
	// errorfs caches errorf, as memo caches param.
	errorfs map[errorfAt]paramFacts
	memo    map[paramAt]paramFacts
}

// resolve returns the values of the fact f of fn with its symbolic values
// resolved, depth callers up from the writer. The leaves' sources belong
// to fn.
func (r *paramResolver) resolve(fn *ssa.Function, f Fact, depth int) []subst {
	s := r.s
	subs := s.mapTree(f.Value, func(id errset.ValueID, k int) []subst {
		v := s.tbl.Get(id)
		unresolved := func(what string) subst {
			return subst{
				value: s.tbl.Intern(errset.Value{
					Kind: errset.Unresolved, Site: v.Site, Pos: v.Pos,
					Unresolved: errset.Memory,
					Detail:     fmt.Sprintf("%s written from %s of %s", r.name, what, v.Fn),
				}),
				leaves: [][]Source{f.Leaves[k]},
			}
		}
		switch v.Kind {
		case errset.FreeVar:
			return []subst{unresolved("captured variable " + v.Name)}
		case errset.ErrorfLike, errset.Param:
			what := "parameter " + v.Name
			var pf paramFacts
			if v.Kind == errset.ErrorfLike {
				what = "a format forwarded from " + what
				pf = r.errorf(id, v, depth)
			} else if p, ok := v.Site.(*ssa.Parameter); ok {
				pf = r.param(p, depth)
			}
			var out []subst
			for _, pfact := range pf.facts {
				leaves := make([][]Source, len(pfact.Leaves))
				for j, srcs := range pfact.Leaves {
					leaves[j] = make([]Source, len(srcs))
					for i, src := range srcs {
						src.Then = f.Leaves[k]
						leaves[j][i] = src
					}
				}
				out = append(out, subst{value: pfact.Value, leaves: leaves, notes: pfact.Notes})
			}
			if !pf.complete {
				out = append(out, unresolved(what))
			}
			return out
		}
		return []subst{{value: id, leaves: [][]Source{f.Leaves[k]}}}
	})
	for i := range subs {
		subs[i].notes = errset.AddNotes(append([]errset.GuardNote(nil), f.Notes...), subs[i].notes...)
	}
	return subs
}

// param returns the values that the callers of p's function pass to p,
// which is depth callers up from the writer.
func (r *paramResolver) param(p *ssa.Parameter, depth int) paramFacts {
	key := paramAt{p, depth}
	if pf, ok := r.memo[key]; ok {
		return pf
	}
	callee, index := p.Parent(), paramIndex(p)
	pf := r.fromCallers(callee, depth, func(site ssa.CallInstruction) ([]flow.Fact, bool) {
		op := argument(site.Common(), callee, index)
		if op == nil {
			return nil, false
		}
		return r.s.argFacts(site.Parent(), op, site), true
	})
	r.memo[key] = pf
	return pf
}

// errorf returns the values of the ErrorfLike value v (id), which is depth
// callers up from the writer: what fmt.Errorf makes of the format and
// operands that each caller of v's function passes.
func (r *paramResolver) errorf(id errset.ValueID, v errset.Value, depth int) paramFacts {
	key := errorfAt{id, depth}
	if pf, ok := r.errorfs[key]; ok {
		return pf
	}
	var pf paramFacts
	if callee := errorfLikeFunc(v); callee != nil {
		pf = r.fromCallers(callee, depth, func(site ssa.CallInstruction) ([]flow.Fact, bool) {
			facts := r.s.forwardedFacts(site, callee, v)
			return facts, facts != nil
		})
	}
	r.errorfs[key] = pf
	return pf
}

// fromCallers joins, over the call sites of callee, the facts that passed
// gives for each site (local facts of the calling function, or false if
// the site does not say), expanded in the caller and with the caller's
// own symbolic values resolved one level further up. Nothing is found,
// and the result is not complete, at maxCallerDepth or if callee's
// callers are not all seen.
func (r *paramResolver) fromCallers(callee *ssa.Function, depth int, passed func(ssa.CallInstruction) ([]flow.Fact, bool)) paramFacts {
	s := r.s
	var pf paramFacts
	sites, ok := s.fields.callersSeen(callee)
	if !ok || depth >= maxCallerDepth {
		return pf
	}
	pf.complete = true
	var fs factSet
	for _, site := range sites {
		caller := site.Parent()
		facts, ok := passed(site)
		if !ok {
			pf.complete = false
			continue
		}
		var args factSet
		for _, af := range facts {
			s.expand(caller, af, &args, r.st)
		}
		for _, af := range args.facts {
			for _, sub := range r.resolve(caller, af, depth+1) {
				leaves := make([][]Source, len(sub.leaves))
				for j, srcs := range sub.leaves {
					leaves[j] = []Source{{Kind: Passed, Site: site, Callee: callee, ArgFn: caller, ArgSources: srcs}}
				}
				fs.add(sub.value, leaves, sub.notes)
			}
		}
	}
	pf.facts = fs.facts
	return pf
}

// fieldValues expands the FieldRef v, loaded at site in fn: the values of
// its field location, each leaf with a Stored source (loaded at site, from
// its sources in the writer), and an Unresolved value at the load for
// every way the field may be written that the location does not hold. A
// location that was not solved is Unresolved, as a field load was before
// locations.
func (s *Solver) fieldValues(fn *ssa.Function, v errset.Value, site any) []subst {
	hole := func(kind errset.UnresolvedKind, detail string) subst {
		return subst{
			value:  s.tbl.Intern(errset.Value{Kind: errset.Unresolved, Site: v.Site, Pos: v.Pos, Unresolved: kind, Detail: detail}),
			leaves: local(1, Source{Kind: Local, Site: site}),
		}
	}
	l := s.location(fn, v)
	if l == nil || l.sum == nil {
		return []subst{hole(errset.Memory, v.Detail)}
	}
	out := make([]subst, 0, len(l.sum.facts))
	for _, f := range l.sum.facts {
		leaves := make([][]Source, len(f.Leaves))
		for j, srcs := range f.Leaves {
			leaves[j] = make([]Source, len(srcs))
			for i, src := range srcs {
				src.Site = site
				leaves[j][i] = src
			}
		}
		out = append(out, subst{value: f.Value, leaves: leaves, notes: f.Notes})
	}
	what := fmt.Sprintf("field %s of %s ", v.Name, types.TypeString(v.Type, nil))
	for _, h := range l.sum.holes {
		out = append(out, hole(h.kind, what+h.reason))
	}
	if s.openWorld() {
		out = append(out, hole(errset.Memory, what+openWorldReason))
	}
	return out
}

// openWorld reports whether a function the Query was asked about has no
// caller in the reachable program although it is not main or an init
// function: it is then called by code the analysis does not see, which
// may write any field.
func (s *Solver) openWorld() bool {
	if s.fields == nil {
		return false
	}
	for _, fn := range s.roots {
		if len(s.fields.callers[fn]) == 0 && !isEntry(fn) {
			return true
		}
	}
	return false
}

// isEntry reports whether fn is a main function or a package
// initializer, which the runtime calls.
func isEntry(fn *ssa.Function) bool {
	if fn.Pkg == nil || fn.Parent() != nil || fn.Signature.Recv() != nil {
		return false
	}
	name := fn.Name()
	return name == "init" || strings.HasPrefix(name, "init#") || (name == "main" && fn.Pkg.Pkg.Name() == "main")
}
