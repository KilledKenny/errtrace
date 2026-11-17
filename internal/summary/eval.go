package summary

import (
	"fmt"
	"go/types"
	"slices"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/errset"
	"github.com/KilledKenny/errtrace/internal/flow"
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
	converged := s.passes(func(st *evalState) {
		out = make([]ResultSummary, len(lf.Results))
		for i, r := range lf.Results {
			var fs factSet
			for _, f := range r.Facts {
				s.expand(fn, f, &fs, st)
			}
			out[i] = ResultSummary{Index: r.Index, Facts: fs.facts}
		}
	})
	if !converged {
		for i := range out {
			out[i].Facts = appendFact(out[i].Facts, s.notConverged(fn), Source{Kind: Local, Site: fn})
		}
	}
	return out
}

// notConverged is the Unresolved value of an evaluation, in fn, whose
// passes did not reach a fixpoint: values may be missing.
func (s *Solver) notConverged(fn *ssa.Function) errset.ValueID {
	return s.tbl.Intern(errset.Value{
		Kind: errset.Unresolved, Site: fn, Pos: fn.Pos(),
		Unresolved: errset.FixpointLimit,
		Detail:     fmt.Sprintf("values in %s that depend on themselves did not converge", fn),
	})
}

// passes runs pass, which expands values of one function, until the
// values of the call sites solved together stop changing (see evaluate).
// It reports false if they still changed at the pass limit.
func (s *Solver) passes(pass func(*evalState)) bool {
	newState := func() *evalState {
		return &evalState{
			final:   make(map[callKey][]Fact),
			prev:    make(map[callKey][]Fact),
			onStack: make(map[callKey]bool),
			vars:    make(map[ssa.Value]int),
			cur:     make(map[callKey][]Fact),
		}
	}
	outer, outerSaw := s.unsettledPass, s.sawNothing
	defer func() { s.unsettledPass, s.sawNothing = outer, outerSaw }()
	// Most evaluations have no value that depends on itself: one pass,
	// in which an inner value of a Wrap that has no value is nil.
	st := newState()
	s.unsettledPass, s.sawNothing = false, false
	pass(st)
	if !st.cyclic {
		return true
	}
	// Some value depends on itself. The values only grow from pass to
	// pass and are bounded by the cap; the pass limit is a safeguard
	// against a non-monotone pass. A Wrap whose inner values have no
	// value yet is first left out, since the values may come in a later
	// pass; once the passes have converged that way, the inner values
	// that still have none are nil, and the passes go on with the Wraps
	// of nothing until they converge again.
	st = newState()
	limit := 4 * (s.chainDepth() + 2)
	s.unsettledPass = true
	for n := 0; ; n++ {
		st.cur = make(map[callKey][]Fact)
		st.changed = false
		pass(st)
		if !st.changed {
			if s.unsettledPass && s.sawNothing {
				s.unsettledPass = false
				st.final = make(map[callKey][]Fact)
				st.prev = st.cur
				continue
			}
			return true
		}
		if n == limit {
			return false
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
			subs := s.captured(creator, facts, f.LeafSource(k), st)
			if v.Index < len(fn.FreeVars) && st.vars[s.rootVar(fn.FreeVars[v.Index])] > 0 {
				// The captured variable is read inside a value stored to
				// it (err = wrap(err) in a closure): what it holds went
				// through this store any number of times before. Its
				// values stand for themselves with their chains cut, as
				// for the stores of other closures (see stored).
				subs = s.flatSubs(subs)
			}
			return subs
		}
		if store, ok := v.Site.(*ssa.Store); ok && v.Kind == errset.StoreRef {
			return s.stored(fn, store, v, f.LeafSource(k), st)
		}
		if v.Kind == errset.FieldRef {
			return s.fieldValues(fn, v, f.LeafSource(k))
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
			leaves[j] = []Source{{Kind: Captured, Site: site, ArgFn: creator, ArgSources: srcs, via: viaOf(srcs)}}
		}
		out[i] = subst{value: f.Value, leaves: leaves, notes: f.Notes}
	}
	return out
}

// stored expands the StoreRef v: the value that the closure w stores with
// the instruction store to a captured variable that is loaded at site in
// fn. The stored value's facts are expanded in w, once per evaluation (a
// store reached inside its own expansion, as for err = wrap(err), stands
// for its values of the previous pass, like a call site: see expandCall).
// Each leaf gets a Stored source: loaded at site, from its sources in w.
// The symbolic values of fn itself stay (a closure that stores what it
// captured of fn's parameters runs for the same call of fn); those of
// other functions are made Unresolved, since the load cannot know them:
// the closure's own parameters, whose arguments depend on who calls it,
// and captured variables whose bindings are not known.
func (s *Solver) stored(fn *ssa.Function, store *ssa.Store, v errset.Value, site any, st *evalState) []subst {
	w := store.Parent()
	key := callKey{store, 0}
	root := s.rootVar(store.Addr)
	var facts []Fact
	if st.vars[root] > 0 {
		// A load of the variable inside a value stored to it (err =
		// wrap(err), through any of its stores): the value read went
		// through a store before, perhaps many times. It stands for
		// the leaves of the store's values so far, each under a
		// Repeat marker, so that a store adds its Wraps once and the
		// values stay linear in the stores.
		st.taint()
		known, ok := st.final[key]
		if !ok {
			if known, ok = st.cur[key]; !ok {
				known = st.prev[key]
			}
		}
		facts = s.flat(known)
	} else {
		st.vars[root]++
		st.inStore++
		facts = s.memoized(key, st, func(fs *factSet) {
			for _, af := range s.argFacts(w, store.Val, store) {
				s.expand(w, af, fs, st)
			}
		})
		st.inStore--
		st.vars[root]--
	}
	var out []subst
	for _, f := range facts {
		for _, sub := range s.mapTree(f.Value, func(id errset.ValueID, k int) []subst {
			l := s.tbl.Get(id)
			if !s.belongsTo(l, fn) {
				what := ""
				switch l.Kind {
				case errset.Param:
					what = "parameter " + l.Name
				case errset.FreeVar:
					what = "captured variable " + l.Name
				case errset.ErrorfLike:
					what = "a format forwarded from parameter " + l.Name
				}
				if what != "" {
					id = s.tbl.Intern(errset.Value{
						Kind: errset.Unresolved, Site: l.Site, Pos: l.Pos,
						Unresolved: errset.Memory,
						Detail:     fmt.Sprintf("variable %s written from %s of %s", v.Name, what, l.Fn),
					})
				}
			}
			return []subst{{value: id, leaves: [][]Source{{{Kind: Stored, Site: site, ArgFn: w, Store: store, ArgSources: f.Leaves[k], via: viaOf(f.Leaves[k])}}}}}
		}) {
			out = append(out, subst{value: sub.value, leaves: sub.leaves, notes: errset.AddNotes(slices.Clone(f.Notes), sub.notes...)})
		}
	}
	return out
}

// belongsTo reports whether the symbolic value l (a Param, FreeVar or
// ErrorfLike) is one of fn's own.
func (s *Solver) belongsTo(l errset.Value, fn *ssa.Function) bool {
	switch site := l.Site.(type) {
	case *ssa.Parameter:
		return site.Parent() == fn
	case ssa.Value:
		if site.Parent() != nil {
			return site.Parent() == fn
		}
	case ssa.Instruction:
		return site.Parent() == fn
	}
	return l.Fn == fn.String()
}

// rootVar returns the local variable (an Alloc) that the address addr of
// a closure's store stands for: addr itself, or what the closure's
// captured variable was bound to, through nested closures.
func (s *Solver) rootVar(addr ssa.Value) ssa.Value {
	for range 64 {
		fv, ok := addr.(*ssa.FreeVar)
		if !ok {
			return addr
		}
		fn := fv.Parent()
		creator := fn.Parent()
		index := slices.Index(fn.FreeVars, fv)
		if creator == nil || index < 0 {
			return addr
		}
		var bound ssa.Value
		for _, b := range creator.Blocks {
			for _, instr := range b.Instrs {
				if mc, ok := instr.(*ssa.MakeClosure); ok && mc.Fn == fn && index < len(mc.Bindings) {
					bound = mc.Bindings[index]
				}
			}
		}
		if bound == nil {
			return addr
		}
		addr = bound
	}
	return addr
}

// flat returns facts as they are read back inside a cycle of stores: each
// leaf of each value under a Repeat marker (errset.Value.Repeat), without
// its Wrap Chain, with the leaf's sources. The values went around the
// cycle an unknown number of times: the Origins are those of facts, and
// the marker says that any number of Wraps may lie between the leaf and
// what the reader puts around it.
func (s *Solver) flat(facts []Fact) []Fact {
	var fs factSet
	for _, f := range facts {
		for _, c := range s.tbl.RepeatCut(f.Value) {
			leaves := make([][]Source, len(c.From))
			for i, from := range c.From {
				for _, k := range from {
					for _, src := range f.Leaves[k] {
						leaves[i] = addSource(leaves[i], src)
					}
				}
			}
			fs.add(c.Value, leaves, nil)
		}
	}
	return fs.facts
}

// flatSubs is flat for substitutions.
func (s *Solver) flatSubs(subs []subst) []subst {
	facts := make([]Fact, len(subs))
	for i, sub := range subs {
		facts[i] = Fact{Value: sub.value, Leaves: sub.leaves}
	}
	facts = s.flat(facts)
	out := make([]subst, len(facts))
	for i, f := range facts {
		out[i] = subst{value: f.Value, leaves: f.Leaves}
	}
	return out
}

// holey reports whether the value tree id has a leaf that is not an Error
// Origin: an Unresolved value, or a symbolic value that may become one.
// Such a value must survive every cut (see keepHoles).
func (s *Solver) holey(id errset.ValueID) bool {
	if h, ok := s.holes[id]; ok {
		return h
	}
	v := s.tbl.Get(id)
	h := false
	if v.Kind.IsLeaf() {
		h = !v.Kind.IsOrigin()
	} else {
		for _, in := range v.Inner {
			h = h || s.holey(in)
		}
	}
	s.holes[id] = h
	return h
}

// holeLeaves returns the leaves of the value tree id that are not Error
// Origins, each as a value of its own under a Truncated marker (its Wrap
// Chain is dropped), with its sources from leaves.
func (s *Solver) holeLeaves(id errset.ValueID, leaves [][]Source) []subst {
	if !s.holey(id) {
		return nil
	}
	var out []subst
	for k, e := range errset.Flatten(s.tbl, id) {
		l := s.tbl.Get(e.Leaf)
		if l.Kind.IsOrigin() {
			continue
		}
		v := e.Leaf
		if len(e.Chain) > 0 || e.Truncated {
			v = s.tbl.Intern(errset.Value{Kind: errset.Truncated, Site: l.Site, Pos: l.Pos, Inner: []errset.ValueID{e.Leaf}})
		}
		var srcs []Source
		if k < len(leaves) {
			srcs = leaves[k]
		}
		out = append(out, subst{value: v, leaves: [][]Source{srcs}})
	}
	return out
}

// cutFacts cuts facts down to its first n. No Unresolved value is lost
// with what is dropped: the leaves of the dropped values that are not
// Error Origins (Unresolved values, and symbolic values that may become
// one) are kept as values of their own, under Truncated markers. There
// are at most as many of those as the program has such places, so the
// result stays bounded.
func (s *Solver) cutFacts(facts []Fact, n int) []Fact {
	if len(facts) <= n {
		return facts
	}
	fs := factSet{facts: facts[:n:n]}
	fs.index = make(map[errset.ValueID]int, n)
	for i, f := range fs.facts {
		fs.index[f.Value] = i
	}
	for _, f := range facts[n:] {
		for _, h := range s.holeLeaves(f.Value, f.Leaves) {
			fs.add(h.value, h.leaves, nil)
		}
	}
	return fs.facts
}

// callKey is one error result of a call site, or, with index 0, the
// value of a Store instruction (see stored).
type callKey struct {
	site  any
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
	// cyclic reports that some value was read that is not final: the
	// evaluation has a value that depends on itself.
	cyclic bool
	// vars counts, per local variable, the stores to it that are being
	// expanded (see stored), and inStore all of them.
	vars    map[ssa.Value]int
	inStore int
}

// taint marks the call site result being expanded as not final.
func (st *evalState) taint() {
	st.cyclic = true
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
	for _, f := range s.memoized(callKey{site, index}, st, func(cur *factSet) {
		s.expandCallOnce(fn, site, index, cur, st)
	}) {
		fs.add(f.Value, f.Leaves, f.Notes)
	}
}

// memoized returns the values of key in the evaluation st, which compute
// finds: computed once if they depend on no value still being solved, and
// otherwise once per pass, with key standing for its values of the
// previous pass where it is reached inside its own computation (see
// expandCall).
func (s *Solver) memoized(key callKey, st *evalState, compute func(*factSet)) []Fact {
	if facts, ok := st.final[key]; ok {
		return facts
	}
	if facts, ok := st.cur[key]; ok {
		st.taint()
		return facts
	}
	if st.onStack[key] {
		st.taint()
		return st.prev[key]
	}
	st.onStack[key] = true
	st.tainted = append(st.tainted, false)
	var cur factSet
	compute(&cur)
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
	return cur.facts
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
		facts := res.Facts
		if st.inStore > 0 && s.solving[g] {
			// Inside a value stored to a variable, the result of a
			// function that is being solved with it (a closure that
			// returns the variable) is the variable read back around
			// the cycle: see stored.
			facts = s.flat(facts)
		}
		for _, gf := range facts {
			via := Source{
				Kind:         Via,
				Site:         site,
				Callee:       g,
				CalleeResult: index,
				CalleeValue:  gf.Value,
				Dynamic:      dynamic,
			}
			for _, sub := range s.substitute(fn, site, g, gf, via, st) {
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
func (s *Solver) substitute(fn *ssa.Function, site ssa.CallInstruction, g *ssa.Function, gf Fact, via Source, st *evalState) []subst {
	return s.mapTree(gf.Value, func(id errset.ValueID, k int) []subst {
		via := via
		via.CalleeLeaf = k
		if k < len(gf.Leaves) {
			via.via = viaOf(gf.Leaves[k])
		}
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
				src.via = later(via.via, viaOf(srcs))
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
				src.via = later(via.via, viaOf(args))
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
	if v.Kind == errset.FieldOf {
		return s.fieldOf(v, s.mapTreeRaw(v.Inner[0], base, leaf))
	}
	var operands [][]subst
	// limits are the fixpoint-limit values among the inner values' values.
	// They say that a set was cut, which is true of the Wrap's values as
	// well: they pass through as they are instead of being wrapped, which
	// would make one of them per Wrap Chain.
	var limits []subst
	off := base
	several := 0
	vanished := false
	for _, in := range v.Inner {
		var subs []subst
		for _, sub := range s.mapTreeRaw(in, off, leaf) {
			if s.isLimit(sub.value) {
				limits = append(limits, sub)
			} else {
				subs = append(subs, sub)
			}
		}
		off += s.tbl.Leaves(in)
		if len(subs) == 0 {
			// The inner value has no value here: it is nil. A Wrap
			// holds nothing for it (fmt.Errorf drops a nil %w operand,
			// errors.Join a nil error, a nil field is a nil Unwrap).
			// Not so an inner value that stands for what a field is
			// given later, and is given nothing: it is no value at
			// all, and neither is a Wrap made of it.
			vanished = vanished || s.laterTree(in)
			continue
		}
		operands = append(operands, subs)
		if len(subs) > 1 {
			several++
		}
	}
	if vanished {
		return limits
	}
	if len(operands) == 0 {
		// Every inner value is nil, unless a fixpoint is still running
		// and may give one of them a value.
		var out []subst
		if alone, ok := s.wrapOfNothing(v); ok {
			if s.unsettledPass {
				s.sawNothing = true
			} else {
				out = append(out, subst{value: alone, leaves: local(1, Source{Kind: Local, Site: v.Site})})
			}
		}
		return append(out, limits...)
	}
	limit := maxValues
	combos := []subst{{}}
	var inner [][]errset.ValueID
	inner = append(inner, nil)
	cut := false
	for _, subs := range operands {
		var nextCombos []subst
		var nextInner [][]errset.ValueID
		for ci, c := range combos {
			for _, sub := range subs {
				if len(nextCombos) == limit {
					cut = true
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
		// A Wrap made for what the field is given later is, with a
		// value for it, a Wrap like any other.
		w.Later = false
		combos[i].value = s.tbl.Intern(w)
	}
	if cut && len(combos) > 0 {
		marker := s.tbl.Intern(errset.Value{
			Kind: errset.Unresolved, Site: v.Site, Pos: v.Pos,
			Unresolved: errset.FixpointLimit,
			Detail:     fmt.Sprintf("more than %d combinations of wrapped values", limit),
		})
		var srcs []Source
		if len(combos[0].leaves) > 0 {
			srcs = combos[0].leaves[0]
		}
		limits = append(limits, subst{value: marker, leaves: [][]Source{srcs}})
		// No Unresolved value may be lost with the combinations that
		// were dropped: those of the operands are kept as values of
		// their own, without the Wrap.
		seen := make(map[errset.ValueID]bool)
		for _, subs := range operands {
			for _, sub := range subs {
				for _, h := range s.holeLeaves(sub.value, sub.leaves) {
					if !seen[h.value] {
						seen[h.value] = true
						combos = append(combos, h)
					}
				}
			}
		}
	}
	seenLimit := make(map[errset.ValueID]bool)
	for _, l := range limits {
		if !seenLimit[l.value] {
			seenLimit[l.value] = true
			combos = append(combos, l)
		}
	}
	return combos
}

// wrapOfNothing returns what the Wrap (or marker) v is when every one of
// its inner values is nil. fmt.Errorf with %w still makes an error, which
// wraps nothing: an Opaque Error. A composite literal of a type with a
// custom Unwrap is a value of that type with a nil field: a Concrete
// Type. errors.Join of nil errors and errtrace.Wrap(nil) are nil, and a
// marker over nothing is nothing: there is no value.
func (s *Solver) wrapOfNothing(v errset.Value) (errset.ValueID, bool) {
	if v.Kind != errset.Wrap || v.Later {
		// A Wrap made only for what a field is given after its value
		// was built: with no later writer, there is no such value.
		return 0, false
	}
	switch v.WrapKind {
	case errset.WrapErrorf:
		return s.tbl.Intern(errset.Value{
			Kind: errset.Opaque, Site: v.Site, Pos: v.Pos,
			Constructor: "fmt.Errorf", Message: v.Format, HasMessage: v.Format != "",
		}), true
	case errset.WrapUnwrapMethod:
		return s.tbl.Intern(errset.Value{Kind: errset.Concrete, Site: v.Site, Pos: v.Pos, Type: v.Type, Allocated: true}), true
	}
	return 0, false
}

// laterTree reports whether the value tree id holds what a field is
// given after its value was built (errset.Value.Later): a Later FieldRef,
// under any Wraps, markers and guards. Such a tree has no value at all
// where the FieldRef has none: the Wrap that flow made for the FieldRef
// is not there, and neither is a Wrap around that one.
func (s *Solver) laterTree(id errset.ValueID) bool {
	v := s.tbl.Get(id)
	switch v.Kind {
	case errset.FieldRef:
		return v.Later
	case errset.Wrap, errset.Truncated, errset.Guarded:
		if v.Later {
			return true
		}
		for _, in := range v.Inner {
			if s.laterTree(in) {
				return true
			}
		}
	}
	return false
}

// isLimit reports whether id is an Unresolved fixpoint-limit value (or
// one under a Truncated marker): the mark of a set that was cut.
func (s *Solver) isLimit(id errset.ValueID) bool {
	v := s.tbl.Get(id)
	if v.Kind == errset.Truncated {
		v = s.tbl.Get(v.Inner[0])
	}
	return v.Kind == errset.Unresolved && v.Unresolved == errset.FixpointLimit
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
		case flow.Keep:
			if !g.Holds || (g.Kind != errset.GuardHelper && g.Kind != errset.GuardAs && g.Kind != errset.GuardAssert) {
				continue
			}
			// The value is kept on the path where a check holds that
			// errtrace cannot decide for it: it may not be there, or
			// not be what it seems (see errset.GuardNote.Assumed).
			if verdict, _ := flow.Decide(s.tbl, sub.value, g); verdict == flow.Maybe {
				sub.notes = errset.AddNotes(slices.Clone(sub.notes), errset.GuardNote{Site: g.Site, Pos: g.Pos, Reason: g.String(), Assumed: true})
			}
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
