package flow_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilledKenny/errtrace/internal/errset"
	"github.com/KilledKenny/errtrace/internal/fixture"
	"github.com/KilledKenny/errtrace/internal/flow"
)

func TestBindingFacts(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "freevar.txtar"))
	pkg := l.SSAPackage(l.Initial[0])
	tests := []struct {
		creator string
		want    string
	}{
		{"ByRef", `sentinel io.EOF; opaque errors.New "boom"`},
		{"ByValue", "param example.com/fx.ByValue#0 err"},
	}
	for _, tt := range tests {
		creator := pkg.Func(tt.creator)
		closure := creator.AnonFuncs[0]
		tbl := errset.NewTable()
		lf := flow.Analyze(tbl, closure, flow.Options{})
		fv := tbl.Get(lf.Results[0].Facts[0].Value)
		if fv.Kind != errset.FreeVar {
			t.Fatalf("%s: closure result is %s, want a FreeVar", tt.creator, tbl.String(lf.Results[0].Facts[0].Value))
		}
		got, facts, ok := flow.BindingFacts(tbl, closure, fv.Index, fv.Type, flow.Options{})
		if !ok || got != creator {
			t.Fatalf("%s: BindingFacts = %v, %v; want the creator", tt.creator, got, ok)
		}
		var vals []string
		for _, f := range facts {
			vals = append(vals, tbl.String(f.Value))
		}
		if s := strings.Join(vals, "; "); s != tt.want {
			t.Errorf("%s: binding facts %q, want %q", tt.creator, s, tt.want)
		}
	}
	// A function that is not a closure has no bindings.
	if _, _, ok := flow.BindingFacts(errset.NewTable(), pkg.Func("ByRef"), 0, pkg.Func("ByRef").Signature, flow.Options{}); ok {
		t.Error("BindingFacts of a top-level function reported ok")
	}
}
