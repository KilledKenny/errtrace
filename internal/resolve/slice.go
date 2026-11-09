package resolve

import (
	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/ssa"
)

// forwardSlice returns the functions reachable from roots in cg: through
// call edges, and through every function that an instruction refers to as
// an operand (a function value, or the body of a closure), as govulncheck's
// forward slice does.
func forwardSlice(roots []*ssa.Function, cg *callgraph.Graph) map[*ssa.Function]bool {
	seen := make(map[*ssa.Function]bool)
	var work []*ssa.Function
	visit := func(fn *ssa.Function) {
		if fn != nil && !seen[fn] {
			seen[fn] = true
			work = append(work, fn)
		}
	}
	for _, r := range roots {
		visit(r)
	}
	var buf [10]*ssa.Value
	for len(work) > 0 {
		fn := work[len(work)-1]
		work = work[:len(work)-1]
		if n := cg.Nodes[fn]; n != nil {
			for _, e := range n.Out {
				if e.Site != nil {
					visit(e.Callee.Func)
				}
			}
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				for _, op := range instr.Operands(buf[:0]) {
					if f, ok := (*op).(*ssa.Function); ok {
						visit(f)
					}
				}
			}
		}
	}
	return seen
}
