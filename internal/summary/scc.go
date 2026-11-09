package summary

// tarjan finds the strongly connected components of the graph reachable
// from roots, where succ gives the successors of a node, and calls emit
// with each one as soon as it is complete. Components are emitted in
// reverse topological order: every component a component's nodes lead to
// is emitted before it (callees before callers). The nodes of one
// component are in the order they were discovered.
//
// The search is iterative, so deep call chains (such as the stdlib's)
// cannot overflow the stack. succ is called once per node. If emit returns
// an error, the search stops and returns it.
func tarjan[N comparable](roots []N, succ func(N) []N, emit func(scc []N) error) error {
	type frame struct {
		n     N
		succs []N
		next  int
	}
	index := make(map[N]int)
	low := make(map[N]int)
	onStack := make(map[N]bool)
	var stack []N
	counter := 0
	push := func(n N) frame {
		index[n] = counter
		low[n] = counter
		counter++
		stack = append(stack, n)
		onStack[n] = true
		return frame{n: n, succs: succ(n)}
	}
	for _, root := range roots {
		if _, seen := index[root]; seen {
			continue
		}
		calls := []frame{push(root)}
		for len(calls) > 0 {
			top := &calls[len(calls)-1]
			if top.next < len(top.succs) {
				w := top.succs[top.next]
				top.next++
				if _, seen := index[w]; !seen {
					calls = append(calls, push(w))
				} else if onStack[w] {
					low[top.n] = min(low[top.n], index[w])
				}
				continue
			}
			n := top.n
			calls = calls[:len(calls)-1]
			if len(calls) > 0 {
				parent := calls[len(calls)-1].n
				low[parent] = min(low[parent], low[n])
			}
			if low[n] != index[n] {
				continue
			}
			i := len(stack) - 1
			for stack[i] != n {
				i--
			}
			scc := append([]N(nil), stack[i:]...)
			stack = stack[:i]
			for _, m := range scc {
				onStack[m] = false
			}
			if err := emit(scc); err != nil {
				return err
			}
		}
	}
	return nil
}
