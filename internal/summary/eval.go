package summary

import (
	"fmt"
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
	"github.com/killedKenny/errtrace/internal/flow"
)

// evaluate computes the result summaries of fn from its local facts and the
// current summaries of its callees.
func (s *Solver) evaluate(fn *ssa.Function) []ResultSummary {
	lf := s.localFacts(fn)
	out := make([]ResultSummary, len(lf.Results))
	for i, r := range lf.Results {
		var fs factSet
		active := make(map[callKey]*cycle)
		for _, f := range r.Facts {
			s.expand(fn, f, &fs, active)
		}
		out[i] = ResultSummary{Index: r.Index, Facts: fs.facts}
	}
	return out
}

// expand adds to fs the values of the local fact f of fn, with the calls
// it refers to (CallRef values) replaced by their callees' summaries. Each
// leaf keeps its own provenance: a local leaf enters fn where f says it
// does, a leaf from a call has the call's sources.
//
// active holds the call sites being expanded (see expandCall).
func (s *Solver) expand(fn *ssa.Function, f flow.Fact, fs *factSet, active map[callKey]*cycle) {
	for _, sub := range s.mapTree(f.Value, func(id errset.ValueID, k int) []subst {
		v := s.tbl.Get(id)
		site, ok := v.Site.(ssa.CallInstruction)
		if v.Kind != errset.CallRef || !ok {
			return []subst{{value: id, leaves: local(1, Source{Kind: Local, Site: f.LeafSource(k)})}}
		}
		var calls factSet
		s.expandCall(fn, site, v.Index, &calls, active)
		out := make([]subst, len(calls.facts))
		for i, c := range calls.facts {
			out[i] = subst{value: c.Value, leaves: c.Leaves}
		}
		return out
	}) {
		fs.add(sub.value, sub.leaves)
	}
}

// callKey is one error result of a call site.
type callKey struct {
	site  ssa.CallInstruction
	index int
}

// cycle is the state of a call site result being expanded: approx is the
// current approximation of its values, used where the site's argument
// depends on its own result, and hit says whether that happened.
type cycle struct {
	approx []Fact
	hit    bool
}

// expandCall adds to fs the values that error result index of the call
// site in fn can carry: the facts of every Candidate's summary, with the
// site's arguments substituted for the Candidate's parameters.
//
// A call whose argument depends on its own result (err = f(err) in a
// loop) is solved to a fixpoint: inside itself the site stands for its
// values so far, starting with none, and it is expanded again until they
// stop changing. Each round can add a Wrap if the callee wraps, so the
// chain depth cap (applied by mapTree) is what makes the values finite;
// the overflow becomes Truncated, as for a loop that wraps inline.
func (s *Solver) expandCall(fn *ssa.Function, site ssa.CallInstruction, index int, fs *factSet, active map[callKey]*cycle) {
	key := callKey{site, index}
	if c := active[key]; c != nil {
		c.hit = true
		for _, f := range c.approx {
			fs.add(f.Value, f.Leaves)
		}
		return
	}
	c := &cycle{}
	active[key] = c
	defer delete(active, key)

	// The values only grow from round to round and are bounded by the
	// cap; the round limit is a safeguard against a non-monotone round.
	limit := 4 * (s.chainDepth() + 2)
	for round := 0; ; round++ {
		var cur factSet
		c.hit = false
		s.expandCallOnce(fn, site, index, &cur, active)
		if !c.hit || round == limit || sameValues(cur.facts, c.approx) {
			for _, f := range cur.facts {
				fs.add(f.Value, f.Leaves)
			}
			return
		}
		c.approx = cur.facts
	}
}

// expandCallOnce is one round of expandCall.
func (s *Solver) expandCallOnce(fn *ssa.Function, site ssa.CallInstruction, index int, fs *factSet, active map[callKey]*cycle) {
	here := local(1, Source{Kind: Local, Site: site})
	common := site.Common()
	dynamic := s.res.IsDynamic(site)
	callees := s.res.Callees(site)
	if len(callees) == 0 {
		fs.add(s.unresolved(site, errset.NoCandidates, "dynamic call to "+flow.CalleeName(common)), here)
		return
	}
	for _, g := range callees {
		if name, ok := flow.Intrinsic(g); ok {
			if call, isCall := site.(*ssa.Call); isCall && index == 0 {
				// Model the intrinsic at the site, as a returned call
				// to it is modelled.
				for _, af := range s.argFacts(fn, call, call) {
					s.expand(fn, af, fs, active)
				}
				continue
			}
			fs.add(s.tbl.Intern(errset.Value{Kind: errset.Opaque, Site: site, Pos: common.Pos(), Constructor: name}), here)
			continue
		}
		if g.Blocks == nil {
			fs.add(s.unresolved(site, errset.NoBody, g.String()+" has no body"), here)
			continue
		}
		sum := s.sums[g]
		var res *ResultSummary
		if sum != nil {
			res = sum.Result(index)
		}
		if res == nil {
			fs.add(s.unresolved(site, errset.Memory, fmt.Sprintf("result %d of %s is not tracked", index, g)), here)
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
			for _, sub := range s.substitute(fn, site, g, gf.Value, via, active) {
				fs.add(sub.value, sub.leaves)
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
// leaves.
type subst struct {
	value  errset.ValueID
	leaves [][]Source
}

// substitute maps the value id of callee g's summary into fn at the call
// site: Param(i) becomes the values of the site's argument i in fn, a
// FreeVar of an immediately applied closure captured by value becomes the
// values of its binding, an ErrorfLike value is evaluated against the
// site's format and operands, and Error Origins and Unresolved values stay
// as they are. Each leaf gets the source via, naming the callee leaf it
// comes from, with the leaf's sources in fn if it came from an argument.
func (s *Solver) substitute(fn *ssa.Function, site ssa.CallInstruction, g *ssa.Function, id errset.ValueID, via Source, active map[callKey]*cycle) []subst {
	return s.mapTree(id, func(id errset.ValueID, k int) []subst {
		via := via
		via.CalleeLeaf = k
		v := s.tbl.Get(id)
		if v.Kind == errset.ErrorfLike && v.Fn == g.String() {
			return s.forwardedErrorf(fn, site, v, via, active)
		}
		var operand ssa.Value
		switch {
		case v.Kind == errset.Param && v.Fn == g.String():
			operand = argument(site.Common(), v.Index)
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
			s.expand(fn, af, &fs, active)
		}
		out := make([]subst, len(fs.facts))
		for i, f := range fs.facts {
			leaves := make([][]Source, len(f.Leaves))
			for j, srcs := range f.Leaves {
				src := via
				src.ArgSources = srcs
				leaves[j] = []Source{src}
			}
			out[i] = subst{value: f.Value, leaves: leaves}
		}
		return out
	})
}

// forwardedErrorf evaluates the ErrorfLike value v of the callee at site
// in fn: the site's format argument and variadic operands take the place of
// the callee's parameters, as if fn called fmt.Errorf itself at the site.
// The Wraps made are placed at the site, where the format is. Each leaf
// gets the source via, with its sources in fn as argument sources.
func (s *Solver) forwardedErrorf(fn *ssa.Function, site ssa.CallInstruction, v errset.Value, via Source, active map[callKey]*cycle) []subst {
	c := site.Common()
	format, args := argument(c, v.Index), argument(c, v.ArgIndex)
	if format == nil || args == nil {
		return []subst{{value: s.unresolved(site, errset.DynamicFormat, "format of fmt.Errorf is not a constant"), leaves: local(1, via)}}
	}
	var fs factSet
	for _, af := range flow.ErrorfFacts(s.tbl, fn, site, site, c.Pos(), format, args, s.opts) {
		s.expand(fn, af, &fs, active)
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
		out[i] = subst{value: f.Value, leaves: leaves}
	}
	return out
}

// argument returns the operand of a call that the callee's parameter i
// receives, or nil.
func argument(c *ssa.CallCommon, i int) ssa.Value {
	if c.IsInvoke() {
		if i == 0 {
			return c.Value
		}
		i--
	}
	if i < len(c.Args) {
		return c.Args[i]
	}
	return nil
}

// mapTree maps every leaf of the value tree id with leaf, which gets the
// leaf and its index in leaf order, and rebuilds the Wraps around the
// results: a Wrap whose inner values map to several values each becomes
// one Wrap per combination (at most maxValues). Leaf sources follow their
// leaves. The rebuilt trees are capped at the chain depth limit.
func (s *Solver) mapTree(id errset.ValueID, leaf func(errset.ValueID, int) []subst) []subst {
	v := s.tbl.Get(id)
	if v.Kind != errset.Wrap && v.Kind != errset.Truncated {
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
			out = append(out, subst{value: c.Value, leaves: leaves})
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
	if v.Kind != errset.Wrap && v.Kind != errset.Truncated {
		return leaf(id, base)
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
				nextCombos = append(nextCombos, subst{leaves: append(c.leaves[:len(c.leaves):len(c.leaves)], sub.leaves...)})
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
