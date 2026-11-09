package resolve_test

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/fixture"
	"github.com/killedKenny/errtrace/internal/resolve"
)

func TestStatic(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "static.txtar"))
	fn := l.SSAPackage(l.Initial[0]).Func("F")
	var calls []ssa.CallInstruction
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if c, ok := instr.(ssa.CallInstruction); ok {
				calls = append(calls, c)
			}
		}
	}
	// g(), func(){}(), r.Read(nil), h()
	want := []struct {
		callee  string
		dynamic bool
	}{
		{"example.com/fx.g", false},
		{"example.com/fx.F$1", false},
		{"", true},
		{"", true},
	}
	if len(calls) != len(want) {
		t.Fatalf("found %d calls in F, want %d", len(calls), len(want))
	}
	var r resolve.Resolver = resolve.Static{}
	for i, c := range calls {
		got := ""
		if callees := r.Callees(c); len(callees) == 1 {
			got = callees[0].String()
		} else if len(callees) > 1 {
			t.Errorf("call %d: %d callees", i, len(callees))
		}
		if got != want[i].callee || r.IsDynamic(c) != want[i].dynamic {
			t.Errorf("call %d (%v): callee %q dynamic %v, want %q %v", i, c, got, r.IsDynamic(c), want[i].callee, want[i].dynamic)
		}
	}
	if !r.Reachable(fn) {
		t.Error("Static.Reachable = false")
	}
}
