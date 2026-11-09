package summary

import (
	"fmt"
	"go/types"
	"slices"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
	"github.com/killedKenny/errtrace/internal/flow"
)

// evaluate computes the result summaries of fn from its local facts and the
// current summaries of its callees.
//
// The call sites of fn whose arguments depend on their own results (err =
// f(err) in a loop, or several such calls around one loop) are solved
// together, to one fixpoint (see expandCall): every pass expands all of
// fn's results once, each such site standing for its values of the
// previous pass where it is reached inside itself, until no site's values
// change. Each pass can add a Wrap around the loop if a callee wraps, so
// the chain depth cap (applied by mapTree) is what makes the values
// finite; the overflow becomes Truncated, as for a loop that wraps inline.
func (s *Solver) evaluate(fn *ssa.Function) []ResultSummary {
	lf := s.localFacts(fn)
	var out []ResultSummary
	s.passes(func(st *evalState) {
		out = make([]ResultSummary, len(lf.Results))
		for i, r := range lf.Results {
			var fs factSet
			for _, f := range r.Facts {
				s.expand(fn, f, &fs, st)
			}
			out[i] = ResultSummary{Index: r.Index, Facts: fs.facts}
		}
	})
	return out
}

// passes runs pass, which expands values of one function, until the
// values of the call sites solved together stop changing (see evaluate).
func (s *Solver) passes(pass func(*evalState)) {
	st := &evalState{
		final:   make(map[callKey][]Fact),
		prev:    make(map[callKey][]Fact),
		onStack: make(map[callKey]bool),
	}
	// The values only grow from pass to pass and are bounded by the cap;
	// the pass limit is a safeguard against a non-monotone pass.
	limit := 4 * (s.chainDepth() + 2)
	for n := 0; ; n++ {
		st.cur = make(map[callKey][]Fact)
		st.changed = false
		pass(st)
		if !st.changed || n == limit {
			return
		}
		st.prev = st.cur
	}
}

// expand adds to fs the values of the local fact f of fn, with the calls
// it refers to (CallRef values) replaced by their callees' summaries, and
// the variables a closure fn captured (FreeVar values) replaced by the
// values its creator bound to them. Each leaf keeps its own provenance: a
// local leaf enters fn where f says it does, a leaf from a call has the
// call's sources, and a captured leaf has its sources in the creator.
func (s *Solver) expand(fn *ssa.Function, f flow.Fact, fs *factSet, st *evalState) {
	for _, sub := range s.mapTree(f.Value, func(id errset.ValueID, k int) []subst {
		v := s.tbl.Get(id)
		if creator, facts, ok := s.binding(fn, v); ok {
			return s.captured(creator, facts, f.LeafSource(k), st)
		}
		site, ok := v.Site.(ssa.CallInstruction)
		if v.Kind != errset.CallRef || !ok {
			return []subst{{value: id, leaves: local(1, Source{Kind: Local, Site: f.LeafSource(k)})}}
		}
		var calls factSet
		s.expandCall(fn, site, v.Index, &calls, st)
		out := make([]subst, len(calls.facts))
		for i, c := range calls.facts {
			out[i] = subst{value: c.Value, leaves: c.Leaves, notes: c.Notes}
		}
		return out
	}) {
		fs.add(sub.value, sub.leaves, sub.notes)
	}
}

// captured expands the facts bound to a captured variable in creator, the
// function that created the closure that reads the variable at site. Each
// leaf gets a Captured source: read at site, from its sources in creator.
func (s *Solver) captured(creator *ssa.Function, facts []flow.Fact, site any, st *evalState) []subst {
	var fs factSet
	for _, bf := range facts {
		s.expand(creator, bf, &fs, st)
	}
	out := make([]subst, len(fs.facts))
	for i, f := range fs.facts {
		leaves := make([][]Source, len(f.Leaves))
		for j, srcs := range f.Leaves {
			leaves[j] = []Source{{Kind: Captured, Site: site, ArgFn: creator, ArgSources: srcs}}
		}
		out[i] = subst{value: f.Value, leaves: leaves, notes: f.Notes}
	}
	return out
}

// callKey is one error result of a call site.
type callKey struct {
	site  ssa.CallInstruction
	index int
}

// evalState is the state of one evaluation of a function's results.
type evalState struct {
	// final holds the values of the call site results that depend on no
	// value still being solved; they are expanded once per evaluation.
	final map[callKey][]Fact
	// prev and cur hold the values of the other call site results, as of
	// the previous pass and as computed so far in this pass.
	prev, cur map[callKey][]Fact
	// onStack holds the call site results being expanded, and tainted
	// says for each of them whether it read a value that is not final.
	onStack map[callKey]bool
	tainted []bool
	// changed says whether a value of this pass differs from prev.
	changed bool
}

// taint marks the call site result being expanded as not final.
func (st *evalState) taint() {
	if n := len(st.tainted); n > 0 {
		st.tainted[n-1] = true
	}
}

// expandCall adds to fs the values that error result index of the call
// site in fn can carry: the facts of every Candidate's summary, with the
// site's arguments substituted for the Candidate's parameters.
//
// A site reached inside its own expansion stands for its values of the
// previous pass (none in the first), and it and every site that reads it
// are not final: they are expanded once per pass until the values of the
// pass stop changing (see evaluate). Every other site is expanded once.
func (s *Solver) expandCall(fn *ssa.Function, site ssa.CallInstruction, index int, fs *factSet, st *evalState) {
	key := callKey{site, index}
	addAll := func(facts []Fact) {
		for _, f := range facts {
			fs.add(f.Value, f.Leaves, f.Notes)
		}
	}
	if facts, ok := st.final[key]; ok {
		addAll(facts)
		return
	}
	if facts, ok := st.cur[key]; ok {
		st.taint()
		addAll(facts)
		return
	}
	if st.onStack[key] {
		st.taint()
		addAll(st.prev[key])
		return
	}
	st.onStack[key] = true
	st.tainted = append(st.tainted, false)
	var cur factSet
	s.expandCallOnce(fn, site, index, &cur, st)
	n := len(st.tainted) - 1
	tainted := st.tainted[n]
	st.tainted = st.tainted[:n]
	delete(st.onStack, key)
	if tainted {
		st.cur[key] = cur.facts
		if !sameValues(cur.facts, st.prev[key]) {
			st.changed = true
		}
		st.taint()
	} else {
		st.final[key] = cur.facts
	}
	addAll(cur.facts)
}

// expandCallOnce is one round of expandCall.
func (s *Solver) expandCallOnce(fn *ssa.Function, site ssa.CallInstruction, index int, fs *factSet, st *evalState) {
	here := local(1, Source{Kind: Local, Site: site})
	common := site.Common()
	dynamic := s.res.IsDynamic(site)
	callees := s.res.Callees(site)
	if len(callees) == 0 {
		fs.add(s.unresolved(site, errset.NoCandidates, "dynamic call to "+flow.CalleeName(common)), here, nil)
		return
	}
	for _, g := range callees {
		if name, ok := flow.Intrinsic(g); ok {
			if call, isCall := site.(*ssa.Call); isCall && index == 0 {
				// Model the intrinsic at the site, as a returned call
				// to it is modelled.
				for _, af := range s.argFacts(fn, call, call) {
					s.expand(fn, af, fs, st)
				}
				continue
			}
			fs.add(s.tbl.Intern(errset.Value{Kind: errset.Opaque, Site: site, Pos: common.Pos(), Constructor: name}), here, nil)
			continue
		}
		if g.Blocks == nil {
			fs.add(s.unresolved(site, errset.NoBody, g.String()+" has no body"), here, nil)
			continue
		}
		sum := s.sums[g]
		var res *ResultSummary
		if sum != nil {
			res = sum.Result(index)
		}
		if res == nil {
			fs.add(s.unresolved(site, errset.Memory, fmt.Sprintf("result %d of %s is not tracked", index, g)), here, nil)
			continue
		}
		for _, gf := range res.Facts {
			via := Source{
				Kind:         Via,
				Site:         site,
				Callee:       g,
				CalleeResult: index,
				CalleeValue:  gf.Value,
				Dynamic:      dynamic,
			}
			for _, sub := range s.substitute(fn, site, g, gf.Value, via, st) {
				fs.add(sub.value, sub.leaves, errset.AddNotes(slices.Clone(gf.Notes), sub.notes...))
			}
		}
	}
}

func (s *Solver) unresolved(site ssa.CallInstruction, kind errset.UnresolvedKind, detail string) errset.ValueID {
	return s.tbl.Intern(errset.Value{
		Kind:       errset.Unresolved,
		Site:       site,
		Pos:        site.Common().Pos(),
		Unresolved: kind,
		Detail:     detail,
	})
}

// subst is one value a value tree maps to, with the sources of each of its
// leaves and the guards that possibly handled it.
type subst struct {
	value  errset.ValueID
	leaves [][]Source
	notes  []errset.GuardNote
}

// substitute maps the value id of callee g's summary into fn at the call
// site: Param(i) becomes the values of the site's argument i in fn, a
// FreeVar still in the summary (that of a synthetic closure, such as a
// bound method wrapper) of an immediately applied closure captured by
// value becomes the values of its binding, an ErrorfLike value is evaluated against the
// site's format and operands, and Error Origins and Unresolved values stay
// as they are. Each leaf gets the source via, naming the callee leaf it
// comes from, with the leaf's sources in fn if it came from an argument.
func (s *Solver) substitute(fn *ssa.Function, site ssa.CallInstruction, g *ssa.Function, id errset.ValueID, via Source, st *evalState) []subst {
	return s.mapTree(id, func(id errset.ValueID, k int) []subst {
		via := via
		via.CalleeLeaf = k
		v := s.tbl.Get(id)
		if v.Kind == errset.ErrorfLike && v.Fn == g.String() {
			return s.forwardedErrorf(fn, site, g, v, via, st)
		}
		var operand ssa.Value
		switch {
		case v.Kind == errset.Param && v.Fn == g.String():
			operand = argument(site.Common(), g, v.Index)
		case v.Kind == errset.FreeVar && v.Fn == g.String():
			if mc, ok := site.Common().Value.(*ssa.MakeClosure); ok && mc.Fn == g && v.Index < len(mc.Bindings) {
				// A variable captured by reference is a pointer; only a
				// binding of the value's own type (captured by value, as a
				// bound method's receiver) is the value itself.
				if b := mc.Bindings[v.Index]; v.Type != nil && types.Identical(b.Type(), v.Type) {
					operand = b
				}
			}
		}
		if operand == nil {
			return []subst{{value: id, leaves: local(1, via)}}
		}
		var fs factSet
		for _, af := range s.argFacts(fn, operand, site) {
			s.expand(fn, af, &fs, st)
		}
		out := make([]subst, len(fs.facts))
		for i, f := range fs.facts {
			leaves := make([][]Source, len(f.Leaves))
			for j, srcs := range f.Leaves {
				src := via
				src.ArgSources = srcs
				leaves[j] = []Source{src}
			}
			out[i] = subst{value: f.Value, leaves: leaves, notes: f.Notes}
		}
		return out
	})
}

// forwardedErrorf evaluates the ErrorfLike value v of the callee at site
// in fn: the site's format argument and variadic operands take the place of
// the callee's parameters, as if fn called fmt.Errorf itself at the site.
// The Wraps made are placed at the site, where the format is. Each leaf
// gets the source via, with its sources in fn as argument sources.
func (s *Solver) forwardedErrorf(fn *ssa.Function, site ssa.CallInstruction, g *ssa.Function, v errset.Value, via Source, st *evalState) []subst {
	c := site.Common()
	format, args := argument(c, g, v.Index), argument(c, g, v.ArgIndex)
	if format == nil || args == nil {
		return []subst{{value: s.unresolved(site, errset.DynamicFormat, "format of fmt.Errorf is not a constant"), leaves: local(1, via)}}
	}
	var fs factSet
	for _, af := range flow.ErrorfFacts(s.tbl, fn, site, site, c.Pos(), format, args, s.opts) {
		s.expand(fn, af, &fs, st)
	}
	out := make([]subst, len(fs.facts))
	for i, f := range fs.facts {
		leaves := make([][]Source, len(f.Leaves))
		for j, srcs := range f.Leaves {
			// A leaf made at the site itself needs no argument path:
			// the call hop already names the site.
			var args []Source
			atSite := false
			for _, src := range srcs {
				if src.Kind == Local && src.Site == site {
					atSite = true
				} else {
					args = append(args, src)
				}
			}
			if atSite || len(args) == 0 {
				leaves[j] = append(leaves[j], via)
			}
			if len(args) > 0 {
				src := via
				src.ArgSources = args
				leaves[j] = addSource(leaves[j], src)
			}
		}
		out[i] = subst{value: f.Value, leaves: leaves, notes: f.Notes}
	}
	return out
}

// argument returns the operand of a call that parameter i of the callee g
// receives, or nil. The receiver of an interface call is parameter 0. A
// Candidate of a function value may have more parameters than the call has
// arguments: a method reached through its method value, whose bound
// receiver comes first and is not an operand of the call.
func argument(c *ssa.CallCommon, g *ssa.Function, i int) ssa.Value {
	if c.IsInvoke() {
		if i == 0 {
			return c.Value
		}
		i--
	} else if shift := len(g.Params) - len(c.Args); shift > 0 {
		i -= shift
	}
	if i >= 0 && i < len(c.Args) {
		return c.Args[i]
	}
	return nil
}

// mapTree maps every leaf of the value tree id with leaf, which gets the
// leaf and its index in leaf order, and rebuilds the Wraps around the
// results: a Wrap whose inner values map to several values each becomes
// one Wrap per combination (at most maxValues). The guards of a Guarded
// value are applied to the values its inner value maps to (see
// applyGuards). Leaf sources follow their leaves. The rebuilt trees are
// capped at the chain depth limit.
func (s *Solver) mapTree(id errset.ValueID, leaf func(errset.ValueID, int) []subst) []subst {
	if s.tbl.Get(id).Kind.IsLeaf() {
		return leaf(id, 0)
	}
	var out []subst
	for _, sub := range s.mapTreeRaw(id, 0, leaf) {
		for _, c := range s.tbl.CapLeaves(sub.value, s.chainDepth()) {
			leaves := make([][]Source, len(c.From))
			for i, from := range c.From {
				for _, k := range from {
					for _, src := range sub.leaves[k] {
						leaves[i] = addSource(leaves[i], src)
					}
				}
			}
			out = append(out, subst{value: c.Value, leaves: leaves, notes: sub.notes})
		}
	}
	return out
}

func (s *Solver) chainDepth() int {
	if s.opts.ChainDepth <= 0 {
		return flow.DefaultChainDepth
	}
	return s.opts.ChainDepth
}

// mapTreeRaw is mapTree without the cap, for the tree id whose first leaf
// has index base.
func (s *Solver) mapTreeRaw(id errset.ValueID, base int, leaf func(errset.ValueID, int) []subst) []subst {
	v := s.tbl.Get(id)
	if v.Kind.IsLeaf() {
		return leaf(id, base)
	}
	if v.Kind == errset.Guarded {
		var out []subst
		for _, sub := range s.mapTreeRaw(v.Inner[0], base, leaf) {
			if sub, ok := s.applyGuards(sub, v.Guards); ok {
				out = append(out, sub)
			}
		}
		return out
	}
	combos := []subst{{}}
	var inner [][]errset.ValueID
	inner = append(inner, nil)
	off := base
	for _, in := range v.Inner {
		subs := s.mapTreeRaw(in, off, leaf)
		off += s.tbl.Leaves(in)
		var nextCombos []subst
		var nextInner [][]errset.ValueID
		for ci, c := range combos {
			for _, sub := range subs {
				if len(nextCombos) == maxValues {
					break
				}
				nextCombos = append(nextCombos, subst{
					leaves: append(c.leaves[:len(c.leaves):len(c.leaves)], sub.leaves...),
					notes:  errset.AddNotes(slices.Clone(c.notes), sub.notes...),
				})
				nextInner = append(nextInner, append(inner[ci][:len(inner[ci]):len(inner[ci])], sub.value))
			}
		}
		combos, inner = nextCombos, nextInner
	}
	for i := range combos {
		w := v
		w.Inner = inner[i]
		combos[i].value = s.tbl.Intern(w)
	}
	return combos
}

// applyGuards applies guards to the value of sub (see flow.ApplyGuard). It
// reports false if a guard removes the value. A guard that cannot be
// decided yet, because the value holds symbolic values such as a Param, is
// kept pending in a Guarded value, to be applied where a caller
// substitutes them.
func (s *Solver) applyGuards(sub subst, guards []errset.Guard) (subst, bool) {
	var pending []errset.Guard
	for _, g := range guards {
		switch outcome, note := flow.ApplyGuard(s.tbl, sub.value, g); outcome {
		case flow.Remove:
			return subst{}, false
		case flow.Note:
			sub.notes = errset.AddNotes(slices.Clone(sub.notes), note)
		case flow.Pending:
			pending = append(pending, g)
		}
	}
	if len(pending) > 0 {
		v := s.tbl.Get(sub.value)
		inner := sub.value
		if v.Kind == errset.Guarded {
			pending = append(slices.Clone(v.Guards), pending...)
			inner = v.Inner[0]
		}
		sub.value = s.tbl.Intern(errset.Value{Kind: errset.Guarded, Site: v.Site, Pos: v.Pos, Inner: []errset.ValueID{inner}, Guards: pending})
	}
	return sub, true
}
