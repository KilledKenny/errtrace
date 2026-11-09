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
		active := make(map[ssa.CallInstruction]bool)
		for _, f := range r.Facts {
			s.expand(fn, f, &fs, active)
		}
		out[i] = ResultSummary{Index: r.Index, Facts: fs.facts}
	}
	return out
}

// expand adds to fs the values of the local fact f of fn, with the calls
// it refers to (CallRef values) replaced by their callees' summaries.
//
// active holds the call sites being expanded. A call whose argument
// depends on its own result (err = f(err) in a loop) is not expanded
// again inside itself: the values that flow around such a loop are the
// ones that enter it, which the outer expansion already adds.
func (s *Solver) expand(fn *ssa.Function, f flow.Fact, fs *factSet, active map[ssa.CallInstruction]bool) {
	local := Source{Kind: Local, Site: f.Source}
	for _, sub := range s.mapTree(f.Value, func(id errset.ValueID) []subst {
		v := s.tbl.Get(id)
		if v.Kind != errset.CallRef {
			return []subst{{value: id}}
		}
		site, ok := v.Site.(ssa.CallInstruction)
		if !ok {
			return []subst{{value: id}}
		}
		var calls factSet
		s.expandCall(fn, site, v.Index, &calls, active)
		out := make([]subst, len(calls.facts))
		for i, c := range calls.facts {
			out[i] = subst{value: c.Value, sources: c.Sources}
		}
		return out
	}) {
		if len(sub.sources) == 0 {
			fs.add(sub.value, local)
			continue
		}
		for _, src := range sub.sources {
			fs.add(sub.value, src)
		}
	}
}

// expandCall adds to fs the values that error result index of the call
// site in fn can carry: the facts of every Candidate's summary, with the
// site's arguments substituted for the Candidate's parameters.
func (s *Solver) expandCall(fn *ssa.Function, site ssa.CallInstruction, index int, fs *factSet, active map[ssa.CallInstruction]bool) {
	if active[site] {
		return
	}
	active[site] = true
	defer delete(active, site)

	here := Source{Kind: Local, Site: site}
	common := site.Common()
	dynamic := s.res.IsDynamic(site)
	callees := s.res.Callees(site)
	if len(callees) == 0 {
		fs.add(s.unresolved(site, errset.NoCandidates, "dynamic call to "+flow.CalleeName(common)), here)
		return
	}
	for _, g := range callees {
		if name, ok := flow.Intrinsic(g); ok {
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
			for _, sub := range s.substitute(fn, site, g, gf.Value, active) {
				fs.add(sub.value, Source{
					Kind:         Via,
					Site:         site,
					Callee:       g,
					CalleeResult: index,
					CalleeValue:  gf.Value,
					ArgSources:   sub.sources,
					Dynamic:      dynamic,
				})
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

// subst is one value a value tree maps to, with the sources of the parts
// that were replaced (empty if nothing was).
type subst struct {
	value   errset.ValueID
	sources []Source
}

// substitute maps the value id of callee g's summary into fn at the call
// site: Param(i) becomes the values of the site's argument i in fn, a
// FreeVar of an immediately applied closure captured by value becomes the
// values of its binding, and Error Origins and Unresolved values stay as
// they are.
func (s *Solver) substitute(fn *ssa.Function, site ssa.CallInstruction, g *ssa.Function, id errset.ValueID, active map[ssa.CallInstruction]bool) []subst {
	return s.mapTree(id, func(id errset.ValueID) []subst {
		v := s.tbl.Get(id)
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
			return []subst{{value: id}}
		}
		var fs factSet
		for _, af := range s.argFacts(fn, operand, site) {
			s.expand(fn, af, &fs, active)
		}
		out := make([]subst, len(fs.facts))
		for i, f := range fs.facts {
			out[i] = subst{value: f.Value, sources: f.Sources}
		}
		return out
	})
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

// mapTree maps every leaf of the value tree id with leaf and rebuilds the
// Wraps around the results: a Wrap whose inner values map to several
// values each becomes one Wrap per combination (at most maxValues).
func (s *Solver) mapTree(id errset.ValueID, leaf func(errset.ValueID) []subst) []subst {
	v := s.tbl.Get(id)
	if v.Kind != errset.Wrap && v.Kind != errset.Truncated {
		return leaf(id)
	}
	combos := []subst{{}}
	var inner [][]errset.ValueID
	inner = append(inner, nil)
	for _, in := range v.Inner {
		subs := s.mapTree(in, leaf)
		var nextCombos []subst
		var nextInner [][]errset.ValueID
		for ci, c := range combos {
			for _, sub := range subs {
				if len(nextCombos) == maxValues {
					break
				}
				srcs := c.sources
				if len(srcs) == 0 {
					srcs = sub.sources
				}
				nextCombos = append(nextCombos, subst{sources: srcs})
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
