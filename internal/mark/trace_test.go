package mark_test

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/fixture"
	"github.com/KilledKenny/errtrace/internal/mark"
)

func descs(steps []mark.Step) string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Desc)
	}
	return strings.Join(out, " | ")
}

func TestTrace(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "calls.txtar"))
	pkg := l.SSAPackage(l.Initial[0])
	r, err := mark.Run(t.Context(), l.SSA, upper(t)(l), subject(t, pkg, "is ErrX"))
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"source":  "Sentinel example.com/fx.ErrX | loaded from variable example.com/fx.ErrX",
		"Free":    "Sentinel example.com/fx.ErrX | loaded from variable example.com/fx.ErrX | returned by example.com/fx.source",
		"CallerX": "Sentinel example.com/fx.ErrX | loaded from variable example.com/fx.ErrX | passed to example.com/fx.wrap, which returns it",
		"wrap":    "Sentinel example.com/fx.ErrX | loaded from variable example.com/fx.ErrX | passed as parameter err of example.com/fx.wrap (this caller passes it) | wrapped by fmt.Errorf",
		"Method":  "Sentinel example.com/fx.ErrX | loaded from variable example.com/fx.ErrX | stored to field inner of example.com/fx.me | loaded from field inner of example.com/fx.me | returned by (*example.com/fx.me).Get",
		"Go":      "Sentinel example.com/fx.ErrX | loaded from variable example.com/fx.ErrX | passed as parameter e of example.com/fx.Go$1 (this caller passes it) | stored to a channel of error | loaded from a channel of error",
		"CallerY": "",
	}
	for name, want := range tests {
		if got := descs(r.TraceResult(pkg.Func(name), 0)); got != want {
			t.Errorf("TraceResult(%s) =\n\t%s\nwant\n\t%s", name, got, want)
		}
	}
	// The trace of a value: the call of source in Free.
	for _, b := range pkg.Func("Free").Blocks {
		for _, instr := range b.Instrs {
			if call, ok := instr.(*ssa.Call); ok {
				if got := descs(r.Trace(call)); got != tests["Free"] {
					t.Errorf("Trace(call in Free) = %s", got)
				}
				if !r.Marked(call) {
					t.Error("the call in Free is not marked")
				}
			}
		}
	}
}

// TestDeterministic runs the pass twice and compares every trace: the
// first reason of a Mark must not depend on map iteration order.
func TestDeterministic(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "calls.txtar"))
	pkg := l.SSAPackage(l.Initial[0])
	var names []string
	for name, m := range pkg.Members {
		if _, ok := m.(*ssa.Function); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	all := func() string {
		r, err := mark.Run(t.Context(), l.SSA, upper(t, "Unreached")(l), subject(t, pkg, "is ErrX"))
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, name := range names {
			fn := pkg.Func(name)
			for i := range fn.Signature.Results().Len() {
				b.WriteString(name + ": " + descs(r.TraceResult(fn, i)) + "\n")
			}
		}
		return b.String()
	}
	first := all()
	for range 3 {
		if again := all(); again != first {
			t.Fatalf("traces differ between runs:\n%s\n---\n%s", first, again)
		}
	}
}
