package flow_test

import (
	"path/filepath"
	"strings"
	"testing"

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
		lf := flow.Analyze(tbl, pkg.Func(name))
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
