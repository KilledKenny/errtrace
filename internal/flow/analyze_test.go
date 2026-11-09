package flow_test

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
	"github.com/killedKenny/errtrace/internal/fixture"
	"github.com/killedKenny/errtrace/internal/flow"
)

func TestAnalyze(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "analyze.txtar"))
	pkg := l.SSAPackage(l.Initial[0])
	tests := map[string]string{
		"Symbolic": "result 1: callref example.com/fx.g #1; param example.com/fx.Symbolic#1 err; callref os.Open #1",
		"Two":      `result 0: opaque errors.New "one" | result 1: concrete *io/fs.PathError`,
		"Loop":     "result 0: sentinel io.EOF",
		"NoError":  "",
	}
	for name, want := range tests {
		tbl := errset.NewTable()
		lf := flow.Analyze(tbl, pkg.Func(name), flow.Options{})
		var results []string
		for _, r := range lf.Results {
			var facts []string
			for _, f := range r.Facts {
				facts = append(facts, tbl.String(f.Value))
			}
			results = append(results, "result "+string(rune('0'+r.Index))+": "+strings.Join(facts, "; "))
		}
		if got := strings.Join(results, " | "); got != want {
			t.Errorf("Analyze(%s) = %q, want %q", name, got, want)
		}
	}
}

func TestValueFacts(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "analyze.txtar"))
	fn := l.SSAPackage(l.Initial[0]).Func("Args")
	var call *ssa.Call
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if c, ok := instr.(*ssa.Call); ok {
				call = c
			}
		}
	}
	if call == nil {
		t.Fatal("no call in Args")
	}
	tbl := errset.NewTable()
	var got []string
	for _, f := range flow.ValueFacts(tbl, fn, call.Call.Args[0], call, flow.Options{}) {
		got = append(got, tbl.String(f.Value))
	}
	if want := "param example.com/fx.Args#1 err; sentinel io.EOF"; strings.Join(got, "; ") != want {
		t.Errorf("ValueFacts(wrap's argument) = %q, want %q", strings.Join(got, "; "), want)
	}
	if name, ok := flow.Intrinsic(call.Call.StaticCallee()); ok {
		t.Errorf("wrap is reported as intrinsic %q", name)
	}
}

func TestIntrinsic(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "analyze.txtar"))
	errorsPkg := l.SSA.ImportedPackage("errors")
	if errorsPkg == nil {
		t.Fatal("errors is not loaded")
	}
	if name, ok := flow.Intrinsic(errorsPkg.Func("New")); !ok || name != "errors.New" {
		t.Errorf("Intrinsic(errors.New) = %q, %v", name, ok)
	}
	if _, ok := flow.Intrinsic(errorsPkg.Func("Unwrap")); ok {
		t.Error("errors.Unwrap is reported as intrinsic")
	}
	if _, ok := flow.Intrinsic(nil); ok {
		t.Error("Intrinsic(nil) = true")
	}
}
