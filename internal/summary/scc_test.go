package summary

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestTarjanOrder(t *testing.T) {
	// 1 → 2 → 3 → 2 (an SCC {2,3}), 3 → 4, 1 → 5 → 5 (a self-loop),
	// 5 → 4, 6 is unreachable.
	graph := map[int][]int{
		1: {2, 5},
		2: {3},
		3: {2, 4},
		4: nil,
		5: {5, 4},
		6: {1},
	}
	calls := make(map[int]int)
	succ := func(n int) []int {
		calls[n]++
		return graph[n]
	}
	var got [][]int
	err := tarjan([]int{1}, succ, func(scc []int) error {
		got = append(got, slices.Clone(scc))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]int{{4}, {2, 3}, {5}, {1}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("SCCs = %v, want %v (callees first)", got, want)
	}
	for n, c := range calls {
		if c != 1 {
			t.Errorf("succ(%d) called %d times", n, c)
		}
	}
	if _, ok := calls[6]; ok {
		t.Error("unreachable node 6 was visited")
	}
}

func TestTarjanDeepChain(t *testing.T) {
	// A chain far deeper than a recursive search could handle comfortably.
	const n = 200000
	succ := func(i int) []int {
		if i == n {
			return nil
		}
		return []int{i + 1}
	}
	count, first := 0, -1
	err := tarjan([]int{0}, succ, func(scc []int) error {
		if count == 0 {
			first = scc[0]
		}
		count++
		return nil
	})
	if err != nil || count != n+1 || first != n {
		t.Errorf("deep chain: err %v, %d SCCs, first %d; want %d SCCs, first %d", err, count, first, n+1, n)
	}
}

func TestTarjanStops(t *testing.T) {
	stop := errors.New("stop")
	calls := 0
	err := tarjan([]int{1, 2}, func(int) []int { return nil }, func([]int) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("tarjan = %v after %d emits, want stop after 1", err, calls)
	}
}
