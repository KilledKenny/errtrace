package summary

import (
	"fmt"
	"go/types"
	"slices"
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
// A location is used in two places only. A load whose base is not an
// error value (func F(s *S) error { return s.err }) is a FieldRef: the
// base is some S, whichever, and the field holds what the location holds.
// A load whose base is itself an error value (the receiver of an Unwrap
// method, a wrapper got from a type assertion) is a FieldOf of that
// value, and is resolved from the value once it is known, with three
// outcomes (Solver.fieldOf): a tracked value of the struct type gives its
// own inner values, exactly those of that instance, plus what the later
// writers of the field store (location.later: a store through a pointer
// that came from elsewhere may have written that instance after it was
// built; the location of those writers alone is the second use); a value
// of another type gives nothing; and a value errtrace does not know to
// be of the type gives one Unresolved value at the load, not the
// location. What comes out of a location is "what some value of this type
// holds in this field", an over-approximation: the sources of such a value
// are marked (Source.via), and an entry whose every source is so marked
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
	// later are the writers that are not construction stores: a
	// construction store writes the field of a value that its function
	// builds or holds in a local variable (a composite literal, new(T)
	// and the assignments to its fields that follow); a later writer
	// stores through a pointer that came from elsewhere, and so may
	// write the field of a value that was built before, a tracked
	// wrapper among them.
	later []*ssa.Store
	// copies are the Stores of a whole struct, loaded through one
	// pointer, through another (*p = *q): the field of *p gets what the
	// field of *q holds. A copy adds no value to the location, but it
	// may write a value that was built before, like a later writer.
	copies []*ssa.Store
	// overwritten reports that a struct holding the field is stored
	// whole through such a pointer in another way (*p = f()): any value
	// of the type may then come to hold what any other holds.
	overwritten bool
	// rest is the location of the later writers alone (see
	// Solver.laterWrites), once it is made.
	rest     *location
	restMade bool
	// copied are, in such a location of later writers, the copies of
	// the field's own location: the values they store count there.
	copied []*ssa.Store
	// of is, for a location of later writers, the field's own location.
	of *location
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
	for _, st := range l.copied {
		for _, f := range s.copyFacts(l, st) {
			c.value(st.Parent(), f.Value)
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

// copyFacts returns the local facts of what the copy st (*p = *q) stores
// to the field of l: the field of the struct that q points to, as a load
// of it at the store would give it.
func (s *Solver) copyFacts(l *location, st *ssa.Store) []flow.Fact {
	k := copyKey{l, st}
	if facts, ok := s.copies[k]; ok {
		return facts
	}
	var facts []flow.Fact
	load := st.Val.(*ssa.UnOp)
	if t := structOf(load.X.Type(), true); t != nil {
		u := t.Underlying().(*types.Struct)
		for i := range u.NumFields() {
			if fk, ok := fieldKeyOf(t, i); ok && s.fields.at(fk) == l.of {
				facts = append(facts, flow.FieldFacts(s.tbl, st.Parent(), load, load.X, i, st, s.opts)...)
			}
		}
	}
	if s.copies == nil {
		s.copies = make(map[copyKey][]flow.Fact)
	}
	s.copies[k] = facts
	return facts
}

// copyKey is a copy of a struct, for one of its fields' locations.
type copyKey struct {
	l  *location
	st *ssa.Store
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
		stores := append(l.writers[:len(l.writers):len(l.writers)], l.copied...)
		for i, store := range stores {
			w := store.Parent()
			var vals factSet
			facts := s.argFacts(w, store.Val, store)
			if i >= len(l.writers) {
				facts = s.copyFacts(l, store)
			}
			for _, af := range facts {
				s.expand(w, af, &vals, st)
			}
			for _, f := range vals.facts {
				for _, sub := range r.resolve(w, f, 0) {
					leaves := make([][]Source, len(sub.leaves))
					for j, srcs := range sub.leaves {
						leaves[j] = []Source{{Kind: Stored, ArgFn: w, Store: store, ArgSources: srcs, Field: l.name, via: l.name}}
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
						src.via = later(viaOf(f.Leaves[k]), src.via)
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
					leaves[j] = []Source{{Kind: Passed, Site: site, Callee: callee, ArgFn: caller, ArgSources: srcs, via: viaOf(srcs)}}
				}
				fs.add(sub.value, leaves, sub.notes)
			}
		}
	}
	pf.facts = fs.facts
	return pf
}

// fieldValues expands the FieldRef v, loaded at site in fn: the values of
// its field location (see locationValues).
func (s *Solver) fieldValues(fn *ssa.Function, v errset.Value, site any) []subst {
	return s.locationValues(s.location(fn, v), v, site)
}

// locationValues returns the values of the location l for the load v (a
// FieldRef) made at site: each leaf with a Stored source (loaded at site,
// from its sources in the writer), and an Unresolved value at the load
// for every way the field may be written that the location does not
// hold. A location that was not solved is Unresolved, as a field load was
// before locations.
func (s *Solver) locationValues(l *location, v errset.Value, site any) []subst {
	hole := func(kind errset.UnresolvedKind, detail string) subst {
		return subst{
			value:  s.tbl.Intern(errset.Value{Kind: errset.Unresolved, Site: v.Site, Pos: v.Pos, Unresolved: kind, Detail: detail}),
			leaves: local(1, Source{Kind: Local, Site: site}),
		}
	}
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

// laterWrites returns the location of what may be written to the field
// of l after a value holding it was built: the later writers of l alone,
// l itself if a struct holding the field is overwritten whole (or every
// writer is a later one), and nil if the field is written by construction
// stores only and in no way the index does not see. A tracked wrapper's
// field then holds exactly what its composite literal stored.
func (s *Solver) laterWrites(l *location) *location {
	if l == nil {
		return nil
	}
	if !l.restMade {
		l.restMade = true
		switch {
		case l.overwritten || (len(l.later) > 0 && len(l.later) == len(l.writers) && len(l.copies) == 0):
			l.rest = l
		case len(l.later) > 0 || len(l.copies) > 0 || l.flags != 0 || l.unreached != "":
			l.rest = &location{name: l.name, writers: l.later, copied: l.copies, of: l, flags: l.flags, unreached: l.unreached, restMade: true}
		}
	}
	return l.rest
}

// fieldBase is what a value is to a field load made on it.
type fieldBase uint8

const (
	// baseNot: the value is known not to be of the struct type; the load
	// does not run on it.
	baseNot fieldBase = iota
	// baseInstance: the value is a tracked value of the struct type whose
	// field errtrace followed from its composite literal.
	baseInstance
	// baseSome: the value is known to be of the struct type, but it is
	// not a tracked one: its field holds what the field's location
	// holds.
	baseSome
	// baseStatic: a parameter or captured variable of the struct type
	// (or a pointer to it) left at the top of a Query: some value of the
	// type, like baseSome.
	baseStatic
	// baseUnknown: errtrace does not know whether the value is of the
	// struct type.
	baseUnknown
	// basePending: the value is symbolic; the load waits for it.
	basePending
)

// fieldOf resolves the FieldOf value v, a load of a field of a struct
// type T, for the values bases of the value it loads from. There are
// three outcomes for a base value, and a wait:
//
//   - it is this instance: a Wrap made for T at a composite literal that
//     errtrace opened, whose Unwrap returns the field (and a fmt.Errorf
//     Wrap with one %w for *fmt.wrapError, an errtrace.Wrap for
//     errtrace's wrapper). The result is that Wrap's inner values, plus
//     what the later writers of the field store (see laterWrites): a
//     store through a pointer may have written this instance after it
//     was built;
//   - it is not a T (its dynamic type is known and is another): nothing;
//     the load does not run on it. A struct that embeds T is another type
//     too: SSA loads the embedded struct first, with a FieldOf of its
//     own, which gives the T (see embedded);
//   - errtrace does not know: an Unresolved value, a value whose Wrap
//     Chain was cut, a Sentinel or other value of unknown dynamic type, a
//     symbolic value left at the top of a Query whose static type does
//     not say, or a value that is here only because a check errtrace
//     cannot decide held for it (errset.GuardNote.Assumed). All of them
//     together give one Unresolved value at the load, as a field load
//     gave before locations. The field's location is not expanded: it
//     would list what every T in the program holds, for a value that may
//     be no T at all.
//
// A value that is known to be a T but was not tracked (a Concrete Type or
// Sentinel of that type, a Wrap asked for another field than the one its
// Unwrap returns, a parameter or captured variable of static type T left
// at the top of a Query) gives the values of the field's location, marked
// as such: its field holds what some T holds. Not so if the value is
// itself known only through a field location (every source of it goes
// through one): such a value may never reach this load at run time, and
// expanding a second location for it lists the errors of unrelated values
// (those of cli.usageError under load.Load). It counts as not known. A
// value still to be substituted keeps the FieldOf around it.
func (s *Solver) fieldOf(v errset.Value, bases []subst) []subst {
	var out []subst
	var unknown []subst
	some, rest := false, false
	// untracked counts the values that are of the struct type but not
	// tracked.
	untracked := 0
	// around are the leaves that the field of a tracked value holds
	// under a bare Repeat marker.
	var around map[errset.ValueID]bool
	ft := fieldTypeOf(v.Type, v.Index)
	_, byValue := underlyingStruct(ft)
	bases = s.embedded(v, bases)
	for _, b := range bases {
		bv := s.tbl.Get(b.value)
		if in := bv; bv.Kind == errset.Truncated && bv.Repeat {
			if in = s.tbl.Get(bv.Inner[0]); in.Kind.IsOrigin() {
				// An Error Origin that went around a cycle of stores:
				// it is under any number of the Wraps of the cycle,
				// of which this load may take one off, so it still
				// stands for itself under any number of them. Under
				// none, the load is made on the Origin itself.
				out = append(out, b)
				bv = in
			}
		}
		kind := s.fieldBase(v, bv)
		if (kind == baseInstance || kind == baseSome) && errset.IsAssumed(b.notes) {
			kind = baseUnknown
		}
		if kind == baseSome && !byValue && viaOf(mergeSources(b.leaves)) != "" {
			// A value that is itself known only as "what some field
			// holds": it may never reach this load. The location is
			// not expanded for it, a guess on a guess.
			untracked++
			kind = baseUnknown
		}
		switch kind {
		case basePending:
			w := v
			w.Inner = []errset.ValueID{b.value}
			out = append(out, subst{value: s.tbl.Intern(w), leaves: b.leaves, notes: b.notes})
		case baseUnknown:
			unknown = append(unknown, b)
		case baseSome, baseStatic:
			if byValue {
				// A struct held by value has no location of its own:
				// it is some value of its type, as far as the load
				// of its field that follows can tell.
				id := s.tbl.Intern(errset.Value{Kind: errset.Concrete, Site: v.Site, Pos: v.Pos, Type: types.Unalias(ft)})
				out = append(out, subst{value: id, leaves: [][]Source{mergeSources(b.leaves)}, notes: b.notes})
				continue
			}
			some = true
		case baseInstance:
			for _, sub := range s.instanceField(v, bv, b, ft) {
				if in := s.tbl.Get(sub.value); in.Kind == errset.Truncated && in.Repeat {
					if around == nil {
						around = make(map[errset.ValueID]bool)
					}
					around[in.Inner[0]] = true
				}
				out = append(out, sub)
			}
			rest = true
		}
	}
	// A tracked value that holds a leaf read back around a cycle of
	// stores (a bare Repeat marker) holds whatever the cycle made of
	// that leaf: the leaf under any of the cycle's Wraps. Those Wraps
	// are the other values the load is made on that hold the leaf the
	// same way, so they are values of the field too; without them the
	// Wrap types of the cycle would not show in the result at all.
	if around != nil {
		for _, b := range bases {
			if s.tbl.Get(b.value).Kind == errset.Wrap && s.holdsRepeat(b.value, around) {
				out = append(out, b)
			}
		}
	}
	ref := v
	ref.Kind, ref.Inner = errset.FieldRef, nil
	l := s.location(nil, ref)
	switch {
	case some:
		out = append(out, s.locationValues(l, ref, v.Site)...)
	case rest && !byValue:
		if later := s.laterWrites(l); later != nil {
			out = append(out, s.locationValues(later, ref, v.Site)...)
		}
	}
	if len(unknown) > 0 {
		why := "the value loaded from is not known"
		if untracked == len(unknown) {
			why = "the value loaded from is not a tracked one"
		}
		var leaves [][]Source
		var notes []errset.GuardNote
		for i, b := range unknown {
			leaves = append(leaves, b.leaves...)
			handled := errset.HandledNotes(b.notes)
			if i == 0 {
				notes = slices.Clone(handled)
			} else {
				notes = errset.IntersectNotes(notes, handled)
			}
		}
		id := s.tbl.Intern(errset.Value{
			Kind: errset.Unresolved, Site: v.Site, Pos: v.Pos,
			Unresolved: errset.Memory,
			Detail:     v.Detail + ": " + why,
		})
		out = append(out, subst{value: id, leaves: [][]Source{mergeSources(leaves)}, notes: notes})
	}
	return out
}

// embedded returns bases with every value replaced that is of a known
// type other than the struct type T of the load v but embeds a T: by the
// T inside it. A method of T promoted to the outer type is called on the
// outer value (the Resolver names the method itself as the Candidate, not
// the wrapper that loads the embedded struct first), so its loads are
// made on the embedded T. That T is found by the loads of the embedded
// fields on the way, each resolved like any other field load (fieldOf):
// from a tracked outer value, the T that its composite literal built.
func (s *Solver) embedded(v errset.Value, bases []subst) []subst {
	var out []subst
	for i, b := range bases {
		steps := s.embedSteps(v, s.tbl.Get(b.value))
		if steps == nil {
			if out != nil {
				out = append(out, b)
			}
			continue
		}
		if out == nil {
			out = append(out, bases[:i]...)
		}
		cur := []subst{b}
		for _, step := range steps {
			cur = s.fieldOf(step, cur)
		}
		out = append(out, cur...)
	}
	if out == nil {
		return bases
	}
	return out
}

// embedSteps returns the loads of embedded fields that lead from the
// value bv, of a known dynamic type that is not the struct type of the
// load v, to the one struct of that type it embeds (as Go's selectors
// find it: at the shallowest depth, and alone there), or nil.
func (s *Solver) embedSteps(v, bv errset.Value) []errset.Value {
	switch {
	case bv.Type == nil, bv.Kind != errset.Concrete && bv.Kind != errset.Sentinel && (bv.Kind != errset.Wrap || bv.WrapKind != errset.WrapUnwrapMethod):
		return nil
	case bv.Kind == errset.Concrete && bv.TypedNil, sameStruct(bv.Type, v.Type):
		return nil
	}
	type at struct {
		t     types.Type
		steps []errset.Value
	}
	level := []at{{t: bv.Type}}
	for depth := 0; depth < 8 && len(level) > 0; depth++ {
		var next []at
		var found [][]errset.Value
		for _, cur := range level {
			t := types.Unalias(cur.t)
			if p, ok := t.Underlying().(*types.Pointer); ok {
				t = types.Unalias(p.Elem())
			}
			st, ok := t.Underlying().(*types.Struct)
			if _, named := t.(*types.Named); !ok || !named {
				continue
			}
			for i := range st.NumFields() {
				f := st.Field(i)
				if !f.Embedded() {
					continue
				}
				steps := append(cur.steps[:len(cur.steps):len(cur.steps)], errset.Value{
					Kind: errset.FieldOf, Site: v.Site, Pos: v.Pos, Type: t, Index: i, Name: f.Name(), Fn: v.Fn,
					Detail: fmt.Sprintf("load of field %s of %s", f.Name(), types.TypeString(cur.t, nil)),
				})
				if sameStruct(f.Type(), v.Type) {
					found = append(found, steps)
				} else {
					next = append(next, at{t: f.Type(), steps: steps})
				}
			}
		}
		switch len(found) {
		case 0:
			level = next
		case 1:
			return found[0]
		default:
			return nil
		}
	}
	return nil
}

// holdsRepeat reports whether the value tree id has one of leaves under a
// Repeat marker.
func (s *Solver) holdsRepeat(id errset.ValueID, leaves map[errset.ValueID]bool) bool {
	v := s.tbl.Get(id)
	switch v.Kind {
	case errset.Truncated:
		return v.Repeat && leaves[v.Inner[0]]
	case errset.Wrap:
		for _, in := range v.Inner {
			if s.holdsRepeat(in, leaves) {
				return true
			}
		}
	}
	return false
}

// mergeSources returns one source list with the sources of every list of
// leaves.
func mergeSources(leaves [][]Source) []Source {
	var out []Source
	for _, srcs := range leaves {
		for _, src := range srcs {
			out = addSource(out, src)
		}
	}
	return out
}

// fieldBase says what the value bv is to the field load v (see fieldOf).
func (s *Solver) fieldBase(v, bv errset.Value) fieldBase {
	switch bv.Kind {
	case errset.Guarded, errset.Param, errset.FreeVar, errset.CallRef, errset.ErrorfLike, errset.StoreRef, errset.FieldRef, errset.FieldOf:
		if !s.atTop {
			return basePending
		}
		// Nothing more is substituted: the static type of a parameter
		// or captured variable still says what it is.
		for bv.Kind == errset.Guarded {
			bv = s.tbl.Get(bv.Inner[0])
		}
		if (bv.Kind == errset.Param || bv.Kind == errset.FreeVar) && sameStruct(bv.Type, v.Type) {
			return baseStatic
		}
		return baseUnknown
	case errset.Truncated:
		if !s.atTop && !s.tbl.Get(bv.Inner[0]).Kind.IsOrigin() && s.tbl.Get(bv.Inner[0]).Kind != errset.Unresolved {
			return basePending
		}
		return baseUnknown
	case errset.Unresolved:
		return baseUnknown
	case errset.Opaque:
		// errors.New and fmt.Errorf without an error to wrap give
		// values of their own types, whose fields hold no error.
		if bv.Constructor == "errors.New" || bv.Constructor == "fmt.Errorf" {
			return baseNot
		}
		return baseUnknown
	case errset.Sentinel:
		switch {
		case bv.Type == nil:
			return baseUnknown
		case sameStruct(bv.Type, v.Type):
			return baseSome
		}
		return baseNot
	case errset.Concrete:
		switch {
		case !sameStruct(bv.Type, v.Type), bv.TypedNil:
			// A load from a nil pointer does not return.
			return baseNot
		case s.emptyLiteral(v, bv):
			return baseInstance
		}
		return baseSome
	case errset.Wrap:
		switch bv.WrapKind {
		case errset.WrapUnwrapMethod:
			if !sameStruct(bv.Type, v.Type) {
				return baseNot
			}
			if path, ok := s.unwrapPath(bv.Type); ok && path[0] == v.Index && s.followsPath(v, path) {
				return baseInstance
			}
			return baseSome
		case errset.WrapErrorf:
			if !isNamed(v.Type, "fmt", "wrapError") {
				return baseNot
			}
			switch {
			case flow.SingleWrapFormat(bv.Format) && len(bv.Inner) == 1 && v.Name == "err":
				return baseInstance
			case bv.Format != "" && len(bv.Inner) > 1:
				// Several %w verbs with operands: a *fmt.wrapErrors.
				return baseNot
			}
			return baseUnknown
		case errset.WrapBraces:
			if !inBracesErrtrace(v.Type) {
				return baseNot
			}
			// errtrace.Wrap's wrapper: the type of that package whose
			// Unwrap returns this field.
			for _, t := range []types.Type{v.Type, types.NewPointer(v.Type)} {
				if path, ok := s.unwrapPath(t); ok && len(path) == 1 && path[0] == v.Index && len(bv.Inner) == 1 {
					return baseInstance
				}
			}
			return baseUnknown
		}
		// errors.Join's type holds a slice of errors, not a field
		// that is an error.
		return baseNot
	}
	return baseUnknown
}

// emptyLiteral reports whether the Concrete Type value bv is a composite
// literal (or new(T)) of the struct type of the load v that stores nothing
// to the field its Unwrap returns, which v loads or is on the way to: a
// tracked value whose Unwrap field is nil (flow makes a Wrap of a literal
// only when it stores an error to that field).
func (s *Solver) emptyLiteral(v, bv errset.Value) bool {
	alloc, ok := bv.Site.(*ssa.Alloc)
	if !ok || !bv.Allocated || !flow.TrackedAlloc(alloc) {
		return false
	}
	path, ok := s.unwrapPath(bv.Type)
	return ok && path[0] == v.Index
}

// followsPath reports whether the load v of the first field of path, the
// way to the field that the Unwrap of v's struct type returns, can be
// followed: path is that field alone, or the first field holds a struct
// (or a pointer to one) whose own Unwrap returns the rest of path (the
// Unwrap of v's struct type is promoted from it).
func (s *Solver) followsPath(v errset.Value, path []int) bool {
	if len(path) == 1 {
		return true
	}
	ft := fieldTypeOf(v.Type, v.Index)
	if ft == nil {
		return false
	}
	inner, ok := s.unwrapPath(ft)
	if !ok {
		inner, ok = s.unwrapPath(types.NewPointer(ft))
	}
	return ok && slices.Equal(inner, path[1:])
}

// instanceField returns the field that v loads from the tracked value bv
// (the value of b): the inner values of the Wrap, each with its leaves'
// sources; nothing for a literal that stores nothing to the field; and,
// where the Unwrap of bv's type is promoted from the struct that v loads
// (an embedded struct, of type ft), that struct as a tracked value of its
// own type with the same inner values, for the load of its field that
// follows.
func (s *Solver) instanceField(v, bv errset.Value, b subst, ft types.Type) []subst {
	if bv.Kind != errset.Wrap {
		return nil
	}
	if bv.WrapKind != errset.WrapUnwrapMethod {
		// fmt.Errorf with one %w, or errtrace.Wrap: one inner value.
		return []subst{{value: bv.Inner[0], leaves: b.leaves, notes: b.notes}}
	}
	if path, ok := s.unwrapPath(bv.Type); ok && len(path) > 1 {
		// The embedded struct as a tracked value: of the type that
		// has the Unwrap method (its own, or a pointer to it).
		w := bv
		w.Type = types.Unalias(ft)
		if p, isPtr := w.Type.(*types.Pointer); isPtr {
			w.Type = types.NewPointer(types.Unalias(p.Elem()))
		} else if _, ok := s.unwrapPath(w.Type); !ok {
			w.Type = types.NewPointer(w.Type)
		}
		return []subst{{value: s.tbl.Intern(w), leaves: b.leaves, notes: b.notes}}
	}
	var out []subst
	off := 0
	for _, in := range bv.Inner {
		n := s.tbl.Leaves(in)
		out = append(out, subst{value: in, leaves: b.leaves[off : off+n : off+n], notes: b.notes})
		off += n
	}
	return out
}

// unwrapPath is flow.UnwrapPath for the Solver's program, cached.
func (s *Solver) unwrapPath(t types.Type) ([]int, bool) {
	if t == nil || s.prog == nil {
		return nil, false
	}
	if s.unwraps == nil {
		s.unwraps = make(map[types.Type]unwrapPath)
	}
	u, ok := s.unwraps[t]
	if !ok {
		u.path, u.ok = flow.UnwrapPath(s.prog, t)
		s.unwraps[t] = u
	}
	return u.path, u.ok
}

type unwrapPath struct {
	path []int
	ok   bool
}

// fieldTypeOf returns the type of field i of the struct type t, or nil.
func fieldTypeOf(t types.Type, i int) types.Type {
	if t == nil {
		return nil
	}
	st, ok := types.Unalias(t).Underlying().(*types.Struct)
	if !ok || i >= st.NumFields() {
		return nil
	}
	return st.Field(i).Type()
}

// underlyingStruct returns the struct type that t is (not one it points
// to).
func underlyingStruct(t types.Type) (*types.Struct, bool) {
	if t == nil {
		return nil, false
	}
	st, ok := types.Unalias(t).Underlying().(*types.Struct)
	return st, ok
}

// isNamed reports whether t is the named type name of package pkg.
func isNamed(t types.Type, pkg, name string) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == pkg && n.Obj().Name() == name
}

// inBracesErrtrace reports whether t is a named type of the errtrace
// library.
func inBracesErrtrace(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "braces.dev/errtrace"
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
