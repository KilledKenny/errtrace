package summary

import (
	"fmt"
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/errset"
	"github.com/KilledKenny/errtrace/internal/flow"
)

// This file models struct fields as field locations. A field location is
// one field of one struct type, for the whole program: its value is the
// join of every value stored to that field of any value of that type. A
// load of the field (a FieldRef of flow) is replaced by the location's
// summary.
//
// The writers are found by one scan of every function with a body that
// the Resolver calls reachable (fieldindex.go):
//
//   - a Store through the field's address (p.f = v, and the fields of a
//     composite literal, which SSA stores one by one) is a writer;
//   - a struct stored, passed, sent or returned whole (*p = v) carries
//     its fields with it. They are instances of the same locations, so
//     they hold what the writers of those locations stored, or what
//     another whole struct brought, and so on back to a writer: a copy
//     adds no value to a location, as long as the code that built the
//     struct is seen;
//   - two struct types converted to one another share their locations;
//   - the address of the field used other than to load from it or store
//     to it (&p.f passed on) lets code write it that the scan does not
//     attribute to the field: the location keeps an Unresolved value.
//
// What the scan cannot see (reflection, unsafe code, functions without a
// body or outside the reachable program, callers outside it) keeps an
// Unresolved value on the locations it may write: see fieldindex.go.
//
// A location has no call context, so a writer's stored value that is a
// parameter of the writer (the constructor pattern) is resolved by joining
// the arguments of every call site of the writer, up to maxCallerDepth
// callers up; see paramResolver.
//
// A location is the fallback. A load whose base is itself an error value
// (the receiver of an Unwrap method, a wrapper got from a type assertion)
// is a FieldOf of that value, and is resolved from the value once it is
// known (Solver.fieldOf): a Wrap that was built for the struct type gives
// its own inner values, exactly those of that instance. Only a value of
// the type whose literal the analysis did not follow is looked up in the
// location. What comes out of a location is "what some value of this type
// holds in this field", an over-approximation: the sources of such a value
// are marked (Source.Field), and an entry whose every source is so marked
// is reported as via the location (Solver.ViaLocation).
//
// A store that wraps what it loads from its own location (s.err =
// fmt.Errorf("...: %w", s.err)) is not iterated to the chain depth: while
// a location is being solved, a load of it stands for the leaves of its
// values, each under a Repeat marker (Solver.flat), so the store adds its
// Wrap once, over "any value of the location, under any number of
// Wraps". Within an SCC of several locations, a location reads the ones
// found before it whole (see solveSCC), so that every cycle has such a
// read but a plain copy of another field keeps its Wrap Chains.

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

// location is a field location: a node of the demand graph.
type location struct {
	// name is "field <f> of <T>", for details.
	name string
	// writers are the Stores through the field's address, in scan order.
	writers []*ssa.Store
	// flags are the ways the field may be written that are not writers.
	flags holeFlags
	// unreached names a function outside the reachable program that
	// reflection may call and that writes the field, or is empty.
	unreached string
	// demand caches what the writers' stored values depend on.
	demand *demand
	// sum is the location's summary, or nil until it is solved.
	sum *locSummary
	// open is set while the location's SCC is being solved, and order
	// is then the location's place in it (see solveSCC).
	open  bool
	order int
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

// location returns the field location of the FieldRef v, loaded in fn.
func (s *Solver) location(fn *ssa.Function, v errset.Value) *location {
	k, ok := fieldKeyOf(v.Type, v.Index)
	prog := s.prog
	if prog == nil && fn != nil {
		prog = fn.Prog
	}
	if !ok || prog == nil {
		return nil
	}
	l := s.index(prog).at(k)
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
	outer := s.curLoc
	s.curLoc = l
	defer func() { s.curLoc = outer }()
	var fs factSet
	converged := s.passes(func(st *evalState) {
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
						leaves[j] = []Source{{Kind: Stored, ArgFn: w, Store: store, ArgSources: srcs, Field: l.name, viaField: true}}
					}
					fs.add(sub.value, leaves, sub.notes)
				}
			}
		}
	})
	sum := &locSummary{facts: fs.facts}
	if !converged {
		sum.holes = append(sum.holes, locHole{errset.FixpointLimit, "holds values that depend on themselves and did not converge"})
	}
	if len(sum.facts) > maxValues {
		sum.facts = s.cutFacts(sum.facts, maxValues)
		sum.holes = append(sum.holes, locHole{errset.FixpointLimit, fmt.Sprintf("can hold more than %d values", maxValues)})
	}
	for _, h := range holeReasons {
		if l.flags&h.flag != 0 {
			sum.holes = append(sum.holes, locHole{errset.Memory, h.reason})
		}
	}
	if l.unreached != "" {
		sum.holes = append(sum.holes, locHole{errset.Memory, "may be written by " + l.unreached + ", which only reflection can call"})
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
						src.viaField = src.viaField || locOnly(f.Leaves[k])
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
					leaves[j] = []Source{{Kind: Passed, Site: site, Callee: callee, ArgFn: caller, ArgSources: srcs, viaField: locOnly(srcs)}}
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
	facts := l.sum.facts
	if l.open && (s.curLoc == nil || l.order >= s.curLoc.order) {
		// The location's SCC is being solved: what is loaded here may
		// have gone around the cycle (through a store that wraps what
		// it loads) any number of times. It stands for the leaves of
		// the values so far, each under a Repeat marker, so that the
		// cycle adds its Wraps once.
		facts = s.flat(facts)
	}
	out := make([]subst, 0, len(facts))
	for _, f := range facts {
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

// fieldOf resolves the FieldOf value v for the values bases of the value
// it loads the field of. A Wrap built for v's struct type with a custom
// Unwrap that returns that field gives its inner values: the field of
// that very instance. A value that cannot be of the struct type gives
// nothing (the load does not run on it). A value still to be substituted
// keeps the FieldOf around it, unless the Query's top is reached. An
// Unresolved value, or one whose Wrap Chain was cut, stays as it is: the
// place the analysis could not see through stands for its fields too. Any
// other value (of the type, but not built where errtrace could see its
// fields) falls back to the field's location.
func (s *Solver) fieldOf(v errset.Value, bases []subst) []subst {
	var out []subst
	fallback := false
	for _, b := range bases {
		bv := s.tbl.Get(b.value)
		switch bv.Kind {
		case errset.Guarded, errset.Param, errset.FreeVar, errset.CallRef, errset.ErrorfLike, errset.StoreRef, errset.FieldRef, errset.FieldOf:
			if s.atTop {
				fallback = true
				continue
			}
			w := v
			w.Inner = []errset.ValueID{b.value}
			out = append(out, subst{value: s.tbl.Intern(w), leaves: b.leaves, notes: b.notes})
		case errset.Wrap:
			if bv.WrapKind != errset.WrapUnwrapMethod || !sameStruct(bv.Type, v.Type) {
				continue
			}
			if f, ok := flow.UnwrapField(s.prog, bv.Type); !ok || f != v.Index {
				fallback = true
				continue
			}
			off := 0
			for _, in := range bv.Inner {
				n := s.tbl.Leaves(in)
				out = append(out, subst{value: in, leaves: b.leaves[off : off+n : off+n], notes: b.notes})
				off += n
			}
		case errset.Concrete:
			if sameStruct(bv.Type, v.Type) && !bv.TypedNil {
				fallback = true
			}
		case errset.Sentinel:
			if bv.Type == nil || sameStruct(bv.Type, v.Type) {
				fallback = true
			}
		case errset.Opaque:
		default:
			// An Unresolved value, or one whose Wrap Chain was cut:
			// what its field holds is as unknown as the value itself,
			// which stands for it.
			out = append(out, b)
		}
	}
	if fallback {
		ref := v
		ref.Kind, ref.Inner = errset.FieldRef, nil
		out = append(out, s.fieldValues(nil, ref, v.Site)...)
	}
	return out
}

// sameStruct reports whether t, or the type t points to, is the struct
// type st.
func sameStruct(t, st types.Type) bool {
	if t == nil || st == nil {
		return false
	}
	t = types.Unalias(t)
	if p, ok := t.Underlying().(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	return types.Identical(t, types.Unalias(st))
}

// openWorld reports whether a function the Query was asked about is not
// reachable from main and the init functions: it is then called by code
// the analysis does not see, which may write any field. The calls of the
// functions that are only reachable from the Query's own functions (a
// recursive Target) do not count.
func (s *Solver) openWorld() bool {
	if s.fields == nil {
		return false
	}
	for _, fn := range s.roots {
		if !s.fields.entry[fn] {
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
