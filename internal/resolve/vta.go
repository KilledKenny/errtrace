package resolve

import (
	"context"
	"sort"
	"time"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/ssa"
)

// VTATiming is how long each phase of building a VTA Resolver took.
type VTATiming struct {
	// CHA is the time to build the initial CHA call graph.
	CHA time.Duration
	// VTA1 is the time of the first forward slice and VTA pass.
	VTA1 time.Duration
	// VTA2 is the time of the second forward slice and VTA pass, and of
	// building the Candidates of every call site.
	VTA2 time.Duration
}

// VTA is the Resolver that finds the Candidates of dynamic calls with
// variable type analysis over the functions reachable from a set of roots,
// as govulncheck does: a CHA call graph, its forward slice from the roots,
// VTA over that slice, the forward slice of the VTA graph, and VTA again,
// with the synthetic wrappers spliced out so that Candidates are the real
// methods and functions. Static calls keep their static callee.
type VTA struct {
	prog      *ssa.Program
	cha       *callgraph.Graph
	roots     []*ssa.Function
	reachable map[*ssa.Function]bool
	callees   map[ssa.CallInstruction][]*ssa.Function
	// Timing is how long building the Resolver took, by phase.
	Timing VTATiming
}

var _ Resolver = (*VTA)(nil)

// NewVTA builds the VTA Resolver of prog from roots.
func NewVTA(ctx context.Context, prog *ssa.Program, roots []*ssa.Function) (*VTA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := time.Now()
	v := &VTA{prog: prog, cha: cha.CallGraph(prog)}
	v.Timing.CHA = time.Since(start)
	if err := v.build(ctx, roots); err != nil {
		return nil, err
	}
	return v, nil
}

// WithRoots returns the VTA Resolver of the same program from v's roots
// plus extra, reusing v's CHA call graph.
func (v *VTA) WithRoots(ctx context.Context, extra []*ssa.Function) (*VTA, error) {
	w := &VTA{prog: v.prog, cha: v.cha, Timing: VTATiming{CHA: v.Timing.CHA}}
	roots := append(v.roots[:len(v.roots):len(v.roots)], extra...)
	if err := w.build(ctx, roots); err != nil {
		return nil, err
	}
	return w, nil
}

func (v *VTA) build(ctx context.Context, roots []*ssa.Function) error {
	v.roots = roots
	start := time.Now()
	slice1 := forwardSlice(roots, v.cha)
	if err := ctx.Err(); err != nil {
		return err
	}
	vta1 := vta.CallGraph(slice1, v.cha)
	v.Timing.VTA1 = time.Since(start)
	if err := ctx.Err(); err != nil {
		return err
	}

	start = time.Now()
	v.reachable = forwardSlice(roots, vta1)
	if err := ctx.Err(); err != nil {
		return err
	}
	cg := vta.CallGraph(v.reachable, vta1)
	v.callees = make(map[ssa.CallInstruction][]*ssa.Function)
	// DeleteSyntheticNodes drops the nodes of synthetic wrappers and
	// with them the edges of the calls inside them, which a summary of a
	// wrapper called statically still needs: record those first.
	for fn, n := range cg.Nodes {
		if fn != nil && fn.Syntax() == nil {
			v.addEdges(n)
		}
	}
	cg.DeleteSyntheticNodes()
	for _, n := range cg.Nodes {
		v.addEdges(n)
	}
	for site, fns := range v.callees {
		sort.Slice(fns, func(i, j int) bool { return fns[i].String() < fns[j].String() })
		v.callees[site] = fns
	}
	v.Timing.VTA2 = time.Since(start)
	return ctx.Err()
}

// addEdges records the Candidates of the dynamic call sites of n.
func (v *VTA) addEdges(n *callgraph.Node) {
	for _, e := range n.Out {
		if e.Site == nil || e.Callee == nil || e.Callee.Func == nil || e.Site.Common().StaticCallee() != nil {
			continue
		}
		fns := v.callees[e.Site]
		dup := false
		for _, f := range fns {
			dup = dup || f == e.Callee.Func
		}
		if !dup {
			v.callees[e.Site] = append(fns, e.Callee.Func)
		}
	}
}

// Callees returns the static callee of a static call, and otherwise the
// Candidates VTA found for site, sorted by name. A dynamic call in a
// function that is not reachable from the roots has no Candidates.
func (v *VTA) Callees(site ssa.CallInstruction) []*ssa.Function {
	if callee := site.Common().StaticCallee(); callee != nil {
		return []*ssa.Function{callee}
	}
	return v.callees[site]
}

// IsDynamic reports whether site has no static callee.
func (v *VTA) IsDynamic(site ssa.CallInstruction) bool {
	return site.Common().StaticCallee() == nil
}

// Reachable reports whether fn is in the forward slice of the roots in
// the first VTA call graph: the functions the second pass analysed.
func (v *VTA) Reachable(fn *ssa.Function) bool { return v.reachable[fn] }
