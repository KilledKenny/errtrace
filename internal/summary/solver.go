package summary

import (
	"context"
	"fmt"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
	"github.com/killedKenny/errtrace/internal/flow"
	"github.com/killedKenny/errtrace/internal/resolve"
)

const (
	// maxRounds caps the fixpoint iterations inside one SCC.
	maxRounds = 64
	// maxValues caps the number of values one error result can carry.
	maxValues = 1024
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
	sites map[*ssa.Function][]ssa.CallInstruction
	args  map[argKey][]flow.Fact
	sums  map[*ssa.Function]*Summary
	// order lists the summarised functions in the order they were solved
	// (callees first).
	order []*ssa.Function
	stats Stats
	// paths are the best Propagation Paths, computed when first asked for
	// after a Solve.
	paths map[node]best

	// transfer computes the result summaries of a function from the
	// current summaries of its callees. It is evaluate, except in tests.
	transfer func(*ssa.Function) []ResultSummary
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
	s := &Solver{
		tbl:   tbl,
		res:   res,
		opts:  opts,
		local: make(map[*ssa.Function]*flow.LocalFacts),
		sites: make(map[*ssa.Function][]ssa.CallInstruction),
		args:  make(map[argKey][]flow.Fact),
		sums:  make(map[*ssa.Function]*Summary),
	}
	s.transfer = s.evaluate
	return s
}

// Stats returns how much work the Solver has done so far.
func (s *Solver) Stats() Stats { return s.stats }

// Summary returns the summary of fn, or nil if fn has not been solved.
func (s *Solver) Summary(fn *ssa.Function) *Summary { return s.sums[fn] }

// Solve computes the summaries of fns and of every function their error
// results depend on, bottom-up over the SCCs of the demand graph.
// Functions solved by an earlier call are reused.
func (s *Solver) Solve(ctx context.Context, fns []*ssa.Function) error {
	var roots []*ssa.Function
	for _, fn := range fns {
		if s.sums[fn] == nil {
			roots = append(roots, fn)
		}
	}
	if len(roots) == 0 {
		return nil
	}
	s.paths = nil
	return tarjan(roots, s.succ, func(scc []*ssa.Function) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.solveSCC(scc)
		return nil
	})
}

// SolveSite computes the summaries that the error results of the call site
// in fn depend on: those of its callees and of the calls in its arguments.
func (s *Solver) SolveSite(ctx context.Context, fn *ssa.Function, site ssa.CallInstruction) error {
	return s.Solve(ctx, s.callees(s.sitesFrom(fn, []ssa.CallInstruction{site})))
}

// SiteFacts returns the values that error result index of the call site in
// fn can carry, in fn's terms, with Via sources through the site. The
// site's dependencies must have been solved with SolveSite.
func (s *Solver) SiteFacts(fn *ssa.Function, site ssa.CallInstruction, index int) []Fact {
	var fs factSet
	s.passes(func(st *evalState) {
		fs = factSet{}
		s.expandCall(fn, site, index, &fs, st)
	})
	return fs.facts
}

// succ returns the unsolved functions of the demand graph that fn's error
// results depend on.
func (s *Solver) succ(fn *ssa.Function) []*ssa.Function {
	var out []*ssa.Function
	for _, g := range s.callees(s.demandSites(fn)) {
		if s.sums[g] == nil {
			out = append(out, g)
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

// demandSites returns the call sites whose results can reach fn's error
// results: the calls its results come from, and the calls in the error
// arguments of those calls (which a callee may return as a parameter).
func (s *Solver) demandSites(fn *ssa.Function) []ssa.CallInstruction {
	sites, ok := s.sites[fn]
	if ok {
		return sites
	}
	var initial []ssa.CallInstruction
	for _, r := range s.localFacts(fn).Results {
		for _, f := range r.Facts {
			initial = s.appendCallRefSites(initial, f.Value)
		}
	}
	sites = s.sitesFrom(fn, initial)
	s.sites[fn] = sites
	return sites
}

// sitesFrom returns initial and, transitively, the calls found in the
// error-typed arguments (and closure bindings) of those calls, without
// duplicates.
func (s *Solver) sitesFrom(fn *ssa.Function, initial []ssa.CallInstruction) []ssa.CallInstruction {
	seen := make(map[ssa.CallInstruction]bool)
	var out []ssa.CallInstruction
	work := append([]ssa.CallInstruction(nil), initial...)
	for len(work) > 0 {
		site := work[0]
		work = work[1:]
		if seen[site] {
			continue
		}
		seen[site] = true
		out = append(out, site)
		for _, v := range errorOperands(site) {
			for _, f := range s.argFacts(fn, v, site) {
				work = s.appendCallRefSites(work, f.Value)
			}
		}
	}
	return out
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

// appendCallRefSites appends the sites of the CallRef leaves of the value
// tree id.
func (s *Solver) appendCallRefSites(sites []ssa.CallInstruction, id errset.ValueID) []ssa.CallInstruction {
	v := s.tbl.Get(id)
	switch v.Kind {
	case errset.CallRef:
		if site, ok := v.Site.(ssa.CallInstruction); ok {
			sites = append(sites, site)
		}
	case errset.Wrap, errset.Truncated:
		for _, in := range v.Inner {
			sites = s.appendCallRefSites(sites, in)
		}
	}
	return sites
}

// solveSCC computes the summaries of the functions of one SCC, whose
// callees outside the SCC are solved already.
func (s *Solver) solveSCC(scc []*ssa.Function) {
	s.stats.SCCs++
	for _, fn := range scc {
		sum := &Summary{Fn: fn}
		for _, r := range s.localFacts(fn).Results {
			sum.Results = append(sum.Results, ResultSummary{Index: r.Index})
		}
		s.sums[fn] = sum
		s.order = append(s.order, fn)
		s.stats.Functions++
	}
	if len(scc) == 1 && !s.selfEdge(scc[0]) {
		s.sums[scc[0]].Results = s.capSize(scc[0], s.transfer(scc[0]))
		return
	}
	var changed map[*ssa.Function][]bool
	for range maxRounds {
		changed = nil
		for _, fn := range scc {
			next := s.capSize(fn, s.transfer(fn))
			if diff := changedResults(s.sums[fn].Results, next); diff != nil {
				if changed == nil {
					changed = make(map[*ssa.Function][]bool)
				}
				changed[fn] = diff
			}
			s.sums[fn].Results = next
		}
		if changed == nil {
			return
		}
	}
	// The SCC did not converge: mark the results still changing.
	for _, fn := range scc {
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
}

func (s *Solver) selfEdge(fn *ssa.Function) bool {
	for _, g := range s.callees(s.demandSites(fn)) {
		if g == fn {
			return true
		}
	}
	return false
}

// capSize cuts every result with more than maxValues values down to
// maxValues and adds an Unresolved fixpoint-limit value to it.
func (s *Solver) capSize(fn *ssa.Function, results []ResultSummary) []ResultSummary {
	for i := range results {
		r := &results[i]
		if len(r.Facts) <= maxValues {
			continue
		}
		r.Facts = r.Facts[:maxValues:maxValues]
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
