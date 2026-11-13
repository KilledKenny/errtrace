package summary

import (
	"context"
	"fmt"

	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/errset"
	"github.com/KilledKenny/errtrace/internal/flow"
	"github.com/KilledKenny/errtrace/internal/resolve"
)

const (
	// maxRounds caps the fixpoint iterations inside one SCC.
	maxRounds = 64
	// maxValues caps the number of values one error result, or one field
	// location, can carry. It was 1024 while a store that wraps what it
	// loads was unrolled to the chain depth; such stores no longer
	// multiply values (see Solver.flat), and the Error Set of a function
	// that reaches much of the standard library has more than 1024
	// values.
	maxValues = 2048
	// maxCombos caps the number of Wraps that substitution makes of one
	// Wrap with several inner values from the combinations of their
	// values. A Wrap of one inner value has as many as that value has,
	// which maxValues bounds.
	maxCombos = 1024
)

// Stats counts the work a Solver did.
type Stats struct {
	// Functions is the number of functions summarised.
	Functions int
	// SCCs is the number of strongly connected components solved.
	SCCs int
}

// Solver computes Summaries on demand. It is not safe for concurrent use.
type Solver struct {
	tbl  *errset.Table
	res  resolve.Resolver
	opts flow.Options

	local map[*ssa.Function]*flow.LocalFacts
	// demands caches what each function's error results depend on.
	demands map[*ssa.Function]*demand
	args    map[argKey][]flow.Fact
	// bindings caches the binding facts of captured variables.
	bindings map[bindingKey]bindingFacts
	sums     map[*ssa.Function]*Summary
	// order lists the summarised functions in the order they were solved
	// (callees first).
	order []*ssa.Function
	stats Stats
	// paths are the best Propagation Paths, computed when first asked for
	// after a Solve.
	paths map[node]best
	// memo has the best paths of the source lists, as of the paths (see
	// computePaths).
	memo map[srcsKey]memoBest

	// fields indexes the field locations of the program and their
	// writers; it is built when a field load is first met (see fields.go).
	fields *fieldIndex
	// roots are the functions Solve and SolveSite were asked about.
	roots []*ssa.Function
	// holes caches holey.
	holes map[errset.ValueID]bool

	// transfer computes the result summaries of a function from the
	// current summaries of its callees. It is evaluate, except in tests.
	transfer func(*ssa.Function) []ResultSummary
}

// dnode is a node of the demand graph: a function, or a field location.
type dnode struct {
	fn  *ssa.Function
	loc *location
}

// bindingKey is one captured variable of a closure, read as one type.
type bindingKey struct {
	fn    *ssa.Function
	index int
	typ   types.Type
}

// bindingFacts are the values bound to a captured variable, as local facts
// of the closure's creator.
type bindingFacts struct {
	creator *ssa.Function
	facts   []flow.Fact
	ok      bool
}

type argKey struct {
	fn *ssa.Function
	v  ssa.Value
	at ssa.Instruction
}

// NewSolver returns a Solver that interns values in tbl, finds the
// Candidates of call sites with res and analyses functions with opts (whose
// ChainDepth also caps the Wrap Chains that substitution builds).
func NewSolver(tbl *errset.Table, res resolve.Resolver, opts flow.Options) *Solver {
	if opts.Sentinels == nil {
		opts.Sentinels = flow.NewSentinels()
	}
	s := &Solver{
		tbl:      tbl,
		res:      res,
		opts:     opts,
		local:    make(map[*ssa.Function]*flow.LocalFacts),
		demands:  make(map[*ssa.Function]*demand),
		holes:    make(map[errset.ValueID]bool),
		args:     make(map[argKey][]flow.Fact),
		bindings: make(map[bindingKey]bindingFacts),
		sums:     make(map[*ssa.Function]*Summary),
	}
	s.transfer = s.evaluate
	return s
}

// Stats returns how much work the Solver has done so far.
func (s *Solver) Stats() Stats { return s.stats }

// Summary returns the summary of fn, or nil if fn has not been solved.
func (s *Solver) Summary(fn *ssa.Function) *Summary { return s.sums[fn] }

// Solve computes the summaries of fns and of every function and field
// location their error results depend on, bottom-up over the SCCs of the
// demand graph. Functions solved by an earlier call are reused.
func (s *Solver) Solve(ctx context.Context, fns []*ssa.Function) error {
	s.roots = append(s.roots, fns...)
	var roots []dnode
	for _, fn := range fns {
		if s.sums[fn] == nil {
			roots = append(roots, dnode{fn: fn})
		}
	}
	return s.solve(ctx, roots)
}

func (s *Solver) solve(ctx context.Context, roots []dnode) error {
	if len(roots) == 0 {
		return nil
	}
	s.paths = nil
	return tarjan(roots, s.succ, func(scc []dnode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.solveSCC(scc)
		return nil
	})
}

// SolveSite computes the summaries that the error results of the call site
// in fn depend on: those of its callees and of the calls and field loads
// in its arguments.
func (s *Solver) SolveSite(ctx context.Context, fn *ssa.Function, site ssa.CallInstruction) error {
	s.roots = append(s.roots, fn)
	c := s.newCollector()
	c.site(site)
	c.drain()
	return s.solve(ctx, s.unsolved(c.nodes()))
}

// SiteFacts returns the values that error result index of the call site in
// fn can carry, in fn's terms, with Via sources through the site. The
// site's dependencies must have been solved with SolveSite.
func (s *Solver) SiteFacts(fn *ssa.Function, site ssa.CallInstruction, index int) []Fact {
	var fs factSet
	converged := s.passes(func(st *evalState) {
		fs = factSet{}
		s.expandCall(fn, site, index, &fs, st)
	})
	if !converged {
		return appendFact(fs.facts, s.notConverged(fn), Source{Kind: Local, Site: site})
	}
	return fs.facts
}

// succ returns the unsolved nodes of the demand graph that n depends on.
func (s *Solver) succ(n dnode) []dnode {
	return s.unsolved(s.deps(n))
}

// deps returns the nodes of the demand graph that n depends on: for a
// function, its callees with summaries and the field locations it loads
// from; for a field location, what its writers' stored values depend on.
func (s *Solver) deps(n dnode) []dnode {
	if n.loc != nil {
		return s.locationDemand(n.loc).nodes(s)
	}
	return s.demandOf(n.fn).nodes(s)
}

// unsolved returns the nodes of nodes that have no summary yet.
func (s *Solver) unsolved(nodes []dnode) []dnode {
	var out []dnode
	for _, n := range nodes {
		if (n.fn != nil && s.sums[n.fn] == nil) || (n.loc != nil && n.loc.sum == nil) {
			out = append(out, n)
		}
	}
	return out
}

// callees returns the Candidates of sites that have summaries: those with
// a body that are not intrinsics, without duplicates, in order.
func (s *Solver) callees(sites []ssa.CallInstruction) []*ssa.Function {
	var out []*ssa.Function
	seen := make(map[*ssa.Function]bool)
	for _, site := range sites {
		for _, g := range s.res.Callees(site) {
			if _, ok := flow.Intrinsic(g); ok || g.Blocks == nil || seen[g] {
				continue
			}
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}

func (s *Solver) localFacts(fn *ssa.Function) *flow.LocalFacts {
	lf, ok := s.local[fn]
	if !ok {
		lf = flow.Analyze(s.tbl, fn, s.opts)
		s.local[fn] = lf
	}
	return lf
}

// argFacts returns the local facts of v, used at the instruction at in fn.
func (s *Solver) argFacts(fn *ssa.Function, v ssa.Value, at ssa.Instruction) []flow.Fact {
	k := argKey{fn, v, at}
	facts, ok := s.args[k]
	if !ok {
		facts = flow.ValueFacts(s.tbl, fn, v, at, s.opts)
		s.args[k] = facts
	}
	return facts
}

// binding returns the values that the FreeVar v of the closure fn can
// hold, as local facts of the function that created fn (see
// flow.BindingFacts). It reports false if v is not one of fn's captured
// variables or its bindings are not known.
func (s *Solver) binding(fn *ssa.Function, v errset.Value) (*ssa.Function, []flow.Fact, bool) {
	if v.Kind != errset.FreeVar || v.Fn != fn.String() {
		return nil, nil, false
	}
	k := bindingKey{fn, v.Index, v.Type}
	b, ok := s.bindings[k]
	if !ok {
		b.creator, b.facts, b.ok = flow.BindingFacts(s.tbl, fn, v.Index, v.Type, s.opts)
		s.bindings[k] = b
	}
	return b.creator, b.facts, b.ok
}

// demand is what a node of the demand graph depends on.
type demand struct {
	// sites are the call sites whose results can reach the node. They may
	// be in other functions than the node's.
	sites []ssa.CallInstruction
	// locs are the field locations whose values can reach the node.
	locs []*location
}

// nodes returns the demand as nodes of the demand graph: the callees of
// the sites that have summaries, then the locations.
func (d *demand) nodes(s *Solver) []dnode {
	var out []dnode
	for _, g := range s.callees(d.sites) {
		out = append(out, dnode{fn: g})
	}
	for _, l := range d.locs {
		out = append(out, dnode{loc: l})
	}
	return out
}

// demandOf returns what fn's error results depend on: the calls its
// results come from, the calls in the error arguments of those calls
// (which a callee may return as a parameter), the calls in the creating
// function whose results a closure's captured variables are bound to, the
// calls in the values that closures store to its variables, and the field
// locations that any of those values load from.
func (s *Solver) demandOf(fn *ssa.Function) *demand {
	d, ok := s.demands[fn]
	if ok {
		return d
	}
	c := s.newCollector()
	for _, r := range s.localFacts(fn).Results {
		for _, f := range r.Facts {
			c.value(fn, f.Value)
		}
	}
	c.drain()
	d = &demand{sites: c.sites, locs: c.locs}
	s.demands[fn] = d
	return d
}

// collector gathers what a set of local facts depends on.
type collector struct {
	s *Solver
	// sites are the call sites found, without duplicates, in the order
	// found; those before next have had their arguments followed.
	sites    []ssa.CallInstruction
	next     int
	seenSite map[ssa.CallInstruction]bool
	// locs are the field locations found.
	locs    []*location
	seenLoc map[*location]bool
	// params are the parameters found, each with the number of calls
	// between it and the facts the collector started from (see
	// locationDemand).
	params     []*ssa.Parameter
	paramDepth map[*ssa.Parameter]int
	depth      int
	// errorfs are the ErrorfLike values found (formats forwarded from
	// parameters), with their depths like params.
	errorfs     []errset.ValueID
	errorfDepth map[errset.ValueID]int
	// stores are the closure stores already followed, which keeps a
	// variable that is stored to itself (err = wrap(err) in a closure)
	// finite.
	stores map[*ssa.Store]bool
}

func (s *Solver) newCollector() *collector {
	return &collector{
		s:           s,
		seenSite:    make(map[ssa.CallInstruction]bool),
		seenLoc:     make(map[*location]bool),
		paramDepth:  make(map[*ssa.Parameter]int),
		errorfDepth: make(map[errset.ValueID]int),
		stores:      make(map[*ssa.Store]bool),
	}
}

// nodes returns what was collected as nodes of the demand graph.
func (c *collector) nodes() []dnode {
	return (&demand{sites: c.sites, locs: c.locs}).nodes(c.s)
}

// site adds a call site.
func (c *collector) site(site ssa.CallInstruction) {
	if !c.seenSite[site] {
		c.seenSite[site] = true
		c.sites = append(c.sites, site)
	}
}

// drain follows the error-typed arguments (and closure bindings) of the
// sites found, transitively, each in the function that contains it.
func (c *collector) drain() {
	for ; c.next < len(c.sites); c.next++ {
		site := c.sites[c.next]
		fn := site.Parent()
		for _, v := range errorOperands(site) {
			for _, f := range c.s.argFacts(fn, v, site) {
				c.value(fn, f.Value)
			}
		}
	}
}

// value adds what the value tree id of fn depends on: the sites of its
// CallRef leaves, the locations of its FieldRef leaves, its Param leaves,
// what the values bound to its FreeVar leaves depend on in the function
// that created the closure fn, and what the values its StoreRef leaves'
// stores write depend on in their closures.
func (c *collector) value(fn *ssa.Function, id errset.ValueID) {
	v := c.s.tbl.Get(id)
	switch v.Kind {
	case errset.CallRef:
		if site, ok := v.Site.(ssa.CallInstruction); ok {
			c.site(site)
		}
	case errset.FreeVar:
		if creator, facts, ok := c.s.binding(fn, v); ok {
			for _, f := range facts {
				c.value(creator, f.Value)
			}
		}
	case errset.StoreRef:
		if st, ok := v.Site.(*ssa.Store); ok && !c.stores[st] {
			c.stores[st] = true
			for _, f := range c.s.argFacts(st.Parent(), st.Val, st) {
				c.value(st.Parent(), f.Value)
			}
		}
	case errset.FieldRef:
		if l := c.s.location(fn, v); l != nil && !c.seenLoc[l] {
			c.seenLoc[l] = true
			c.locs = append(c.locs, l)
		}
	case errset.Param:
		if p, ok := v.Site.(*ssa.Parameter); ok {
			if _, seen := c.paramDepth[p]; !seen {
				c.paramDepth[p] = c.depth
				c.params = append(c.params, p)
			}
		}
	case errset.ErrorfLike:
		if _, seen := c.errorfDepth[id]; !seen {
			c.errorfDepth[id] = c.depth
			c.errorfs = append(c.errorfs, id)
		}
	case errset.Wrap, errset.Truncated, errset.Guarded:
		for _, in := range v.Inner {
			c.value(fn, in)
		}
	}
}

// errorOperands returns the operands of a call that a callee's Param or
// FreeVar (or a Wrap the call makes) can stand for and that may carry an
// error: the receiver and arguments, the operands packed into a variadic
// argument (those of fmt.Errorf, errors.Join or a forwarding wrapf), and
// the bindings of an immediately applied closure.
func errorOperands(site ssa.CallInstruction) []ssa.Value {
	c := site.Common()
	var ops []ssa.Value
	if c.IsInvoke() {
		ops = append(ops, c.Value)
	}
	ops = append(ops, c.Args...)
	if mc, ok := c.Value.(*ssa.MakeClosure); ok {
		ops = append(ops, mc.Bindings...)
	}
	var out []ssa.Value
	for _, v := range ops {
		if flow.IsErrorType(v.Type()) {
			out = append(out, v)
			continue
		}
		packed, _ := flow.VarargsOperands(v)
		for _, p := range packed {
			if p != nil && flow.MayCarryError(p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// solveSCC computes the summaries of the functions and field locations of
// one SCC, whose dependencies outside the SCC are solved already.
func (s *Solver) solveSCC(scc []dnode) {
	counted := false
	var locs []*location
	for _, n := range scc {
		if n.loc != nil {
			n.loc.sum = &locSummary{}
			locs = append(locs, n.loc)
			continue
		}
		fn := n.fn
		sum := &Summary{Fn: fn}
		for _, r := range s.localFacts(fn).Results {
			sum.Results = append(sum.Results, ResultSummary{Index: r.Index})
		}
		s.sums[fn] = sum
		s.order = append(s.order, fn)
		s.stats.Functions++
		if !counted {
			counted = true
			s.stats.SCCs++
		}
	}
	if len(scc) == 1 && !s.selfEdge(scc[0]) {
		if n := scc[0]; n.loc != nil {
			n.loc.sum = s.evalLocation(n.loc)
		} else {
			s.sums[n.fn].Results = s.capSize(n.fn, s.transfer(n.fn))
		}
		return
	}
	// While the SCC is solved, a load of one of its locations stands for
	// the leaves of the location's values so far (see fieldValues): what
	// is loaded went around the cycle an unknown number of times. Once
	// the locations are known, the functions are evaluated once more
	// with the locations' whole values.
	for _, l := range locs {
		l.open = true
	}
	defer func() {
		for _, l := range locs {
			l.open = false
		}
		if len(locs) == 0 {
			return
		}
		for _, n := range scc {
			if n.fn != nil {
				s.sums[n.fn].Results = s.finalResults(n.fn, s.capSize(n.fn, s.transfer(n.fn)))
			}
		}
	}()
	var changed map[*ssa.Function][]bool
	var changedLocs []*location
	for range maxRounds {
		changed, changedLocs = nil, nil
		for _, n := range scc {
			if n.loc != nil {
				next := s.evalLocation(n.loc)
				if !sameValues(n.loc.sum.facts, next.facts) {
					changedLocs = append(changedLocs, n.loc)
				}
				n.loc.sum = next
				continue
			}
			fn := n.fn
			next := s.capSize(fn, s.transfer(fn))
			if diff := changedResults(s.sums[fn].Results, next); diff != nil {
				if changed == nil {
					changed = make(map[*ssa.Function][]bool)
				}
				changed[fn] = diff
			}
			s.sums[fn].Results = next
		}
		if changed == nil && changedLocs == nil {
			return
		}
	}
	// The SCC did not converge: mark the results and locations still
	// changing.
	for _, n := range scc {
		fn := n.fn
		if fn == nil {
			continue
		}
		for i, ch := range changed[fn] {
			if !ch {
				continue
			}
			r := &s.sums[fn].Results[i]
			id := s.tbl.Intern(errset.Value{
				Kind: errset.Unresolved, Site: fn, Pos: fn.Pos(),
				Unresolved: errset.FixpointLimit,
				Detail:     fmt.Sprintf("summary of %s did not converge within %d rounds", fn, maxRounds),
			})
			r.Facts = appendFact(r.Facts, id, Source{Kind: Local, Site: fn})
		}
	}
	for _, l := range changedLocs {
		l.sum.holes = append(l.sum.holes, locHole{errset.FixpointLimit, fmt.Sprintf("did not converge within %d rounds", maxRounds)})
	}
}

func (s *Solver) selfEdge(n dnode) bool {
	for _, d := range s.deps(n) {
		if d == n {
			return true
		}
	}
	return false
}

// finalResults returns next, the results of fn evaluated once more after
// its SCC was solved, with the fixpoint-limit values of the results that
// did not converge carried over from fn's summary.
func (s *Solver) finalResults(fn *ssa.Function, next []ResultSummary) []ResultSummary {
	for i, r := range s.sums[fn].Results {
		if i >= len(next) {
			break
		}
		for _, f := range r.Facts {
			if v := s.tbl.Get(f.Value); v.Kind == errset.Unresolved && v.Unresolved == errset.FixpointLimit && v.Site == any(fn) {
				next[i].Facts = appendFact(next[i].Facts, f.Value, Source{Kind: Local, Site: fn})
			}
		}
	}
	return next
}

// capSize cuts every result with more than maxValues values down to
// maxValues and adds an Unresolved fixpoint-limit value to it.
func (s *Solver) capSize(fn *ssa.Function, results []ResultSummary) []ResultSummary {
	for i := range results {
		r := &results[i]
		if len(r.Facts) <= maxValues {
			continue
		}
		r.Facts = s.cutFacts(r.Facts, maxValues)
		id := s.tbl.Intern(errset.Value{
			Kind: errset.Unresolved, Site: fn, Pos: fn.Pos(),
			Unresolved: errset.FixpointLimit,
			Detail:     fmt.Sprintf("result %d of %s can carry more than %d values", r.Index, fn, maxValues),
		})
		r.Facts = appendFact(r.Facts, id, Source{Kind: Local, Site: fn})
	}
	return results
}

// appendFact adds the fact id with source src to facts, unless a fact of
// that value is there already.
func appendFact(facts []Fact, id errset.ValueID, src Source) []Fact {
	for _, f := range facts {
		if f.Value == id {
			return facts
		}
	}
	return append(facts, Fact{Value: id, Leaves: [][]Source{{src}}})
}

// changedResults compares the value sets of two summaries of one function
// result by result. It returns nil if they are equal, and otherwise which
// results differ.
func changedResults(old, next []ResultSummary) []bool {
	var diff []bool
	for i := range next {
		if i < len(old) && sameValues(old[i].Facts, next[i].Facts) {
			continue
		}
		if diff == nil {
			diff = make([]bool, len(next))
		}
		diff[i] = true
	}
	return diff
}

func sameValues(a, b []Fact) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[errset.ValueID]bool, len(a))
	for _, f := range a {
		set[f.Value] = true
	}
	for _, f := range b {
		if !set[f.Value] {
			return false
		}
	}
	return true
}
