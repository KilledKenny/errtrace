package resolve_test

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/fixture"
	"github.com/killedKenny/errtrace/internal/resolve"
)

// dynamicCalls returns the dynamic call sites of fn, in order, without
// calls of builtins.
func dynamicCalls(fn *ssa.Function) []ssa.CallInstruction {
	var out []ssa.CallInstruction
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if c, ok := instr.(ssa.CallInstruction); ok && c.Common().StaticCallee() == nil {
				if _, builtin := c.Common().Value.(*ssa.Builtin); !builtin {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// TestVTAToy checks the VTA recipe against the callgraph-algorithms
// research: on its toy program the callback in apply has exactly one
// Candidate and r.Read in f at most five.
func TestVTAToy(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "toy.txtar"))
	pkg := l.SSAPackage(l.Initial[0])
	roots := resolve.Roots(l.SSA, []*ssa.Package{pkg})
	if len(roots) == 0 || roots[0].Pkg == nil {
		t.Fatalf("roots = %v", roots)
	}
	foundMain := false
	for _, r := range roots {
		foundMain = foundMain || r == pkg.Func("main")
	}
	if !foundMain {
		t.Fatalf("roots %v do not include main", roots)
	}
	v, err := resolve.NewVTA(t.Context(), l.SSA, roots)
	if err != nil {
		t.Fatal(err)
	}
	var r resolve.Resolver = v

	apply := pkg.Func("apply")
	calls := dynamicCalls(apply)
	if len(calls) != 1 {
		t.Fatalf("apply has %d dynamic calls, want 1", len(calls))
	}
	if got := r.Callees(calls[0]); len(got) != 1 || got[0].String() != "example.com/toy.main$2" {
		t.Errorf("cb() Candidates = %v, want [example.com/toy.main$2]", got)
	}
	if !r.IsDynamic(calls[0]) {
		t.Error("cb() is not dynamic")
	}

	f := pkg.Func("f")
	calls = dynamicCalls(f)
	if len(calls) != 1 || !calls[0].Common().IsInvoke() {
		t.Fatalf("f has dynamic calls %v, want one r.Read", calls)
	}
	if got := r.Callees(calls[0]); len(got) == 0 || len(got) > 5 {
		t.Errorf("r.Read Candidates = %v, want 1 to 5", got)
	}

	if !r.Reachable(f) || !r.Reachable(apply) {
		t.Error("f or apply not reachable from main")
	}
	if v.Timing.CHA <= 0 || v.Timing.VTA1 <= 0 || v.Timing.VTA2 <= 0 {
		t.Errorf("Timing = %+v, want every phase timed", v.Timing)
	}
}

// TestVTAExtraRoots checks that a function not reachable from main/init
// becomes reachable once it is added as a root, and that its static calls
// keep their static callee.
func TestVTAExtraRoots(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "static.txtar"))
	pkg := l.SSAPackage(l.Initial[0])
	v, err := resolve.NewVTA(t.Context(), l.SSA, resolve.Roots(l.SSA, []*ssa.Package{pkg}))
	if err != nil {
		t.Fatal(err)
	}
	fn := pkg.Func("F")
	if v.Reachable(fn) {
		t.Fatal("F reachable without a main")
	}
	w, err := v.WithRoots(t.Context(), []*ssa.Function{fn})
	if err != nil {
		t.Fatal(err)
	}
	if !w.Reachable(fn) || !w.Reachable(pkg.Func("g")) {
		t.Error("F or g not reachable from F")
	}
	for _, c := range dynamicCalls(fn) {
		if got := w.Callees(c); len(got) != 0 {
			t.Errorf("%v: Candidates %v, want none", c, got)
		}
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if c, ok := instr.(ssa.CallInstruction); ok && c.Common().StaticCallee() != nil {
				if got := w.Callees(c); len(got) != 1 || got[0] != c.Common().StaticCallee() || w.IsDynamic(c) {
					t.Errorf("%v: Candidates %v, want the static callee", c, got)
				}
			}
		}
	}
}
