package summary

import (
	"slices"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
)

// node is a leaf of a fact of a summary: leaf number leaf of a value of
// one error result of a function.
type node struct {
	fn     *ssa.Function
	result int
	value  errset.ValueID
	leaf   int
}

// best is the best Propagation Path known to a node, and its cost.
type best struct {
	path    errset.Path
	dynamic int
}

// less reports whether a is a cheaper path than b: fewer dynamic hops,
// then fewer hops.
func (a best) less(b best) bool {
	if a.dynamic != b.dynamic {
		return a.dynamic < b.dynamic
	}
	return len(a.path) < len(b.path)
}

// Path returns the best Propagation Path of a leaf that reaches fn with
// the sources srcs (a leaf of a fact of fn's summary, or of a fact from
// SiteFacts): the
// one with the fewest dynamic hops, then the fewest hops, with ties going
// to the first-ranked source. It runs from the place the value enters the
// innermost function up to fn. It reports false if no source leads to a
// known path.
func (s *Solver) Path(fn *ssa.Function, srcs []Source) (errset.Path, bool) {
	if s.paths == nil {
		s.computePaths()
	}
	b, ok := s.bestOf(fn, srcs)
	return b.path, ok
}

// computePaths finds the best path of every node of every summary. It is a
// shortest-path search over the nodes, whose edges are the sources:
// it relaxes the nodes in solving order (callees first) until no path gets
// cheaper. Costs are positive, so it terminates, and every best path
// visits each node at most once.
func (s *Solver) computePaths() {
	s.paths = make(map[node]best)
	for changed := true; changed; {
		changed = false
		for _, fn := range s.order {
			for _, r := range s.sums[fn].Results {
				for _, f := range r.Facts {
					for k, srcs := range f.Leaves {
						n := node{fn, r.Index, f.Value, k}
						b, ok := s.bestOf(fn, srcs)
						if !ok {
							continue
						}
						if cur, known := s.paths[n]; !known || b.less(cur) {
							s.paths[n] = b
							changed = true
						}
					}
				}
			}
		}
	}
}

// bestOf returns the cheapest path among srcs in fn, given the current
// best paths of the nodes they refer to.
func (s *Solver) bestOf(fn *ssa.Function, srcs []Source) (best, bool) {
	var b best
	found := false
	for _, src := range srcs {
		c, ok := s.sourcePath(fn, src)
		if ok && (!found || c.less(b)) {
			b, found = c, true
		}
	}
	return b, found
}

// sourcePath returns the path through one source of a value in fn: hop 0
// for a local source; for a source through a call, the path of the
// argument substituted into the callee (if any), then the callee's path,
// then the call.
func (s *Solver) sourcePath(fn *ssa.Function, src Source) (best, bool) {
	if src.Kind == Local {
		return best{path: errset.Path{{Site: src.Site, Func: fn.String()}}}, true
	}
	callee, ok := s.paths[node{src.Callee, src.CalleeResult, src.CalleeValue, src.CalleeLeaf}]
	if !ok {
		return best{}, false
	}
	var arg best
	if len(src.ArgSources) > 0 {
		if arg, ok = s.bestOf(fn, src.ArgSources); !ok {
			return best{}, false
		}
	}
	path := slices.Concat(arg.path, callee.path, errset.Path{{
		Site:    src.Site,
		Func:    fn.String(),
		Callee:  src.Callee.String(),
		Dynamic: src.Dynamic,
	}})
	dynamic := arg.dynamic + callee.dynamic
	if src.Dynamic {
		dynamic++
	}
	return best{path: path, dynamic: dynamic}, true
}
