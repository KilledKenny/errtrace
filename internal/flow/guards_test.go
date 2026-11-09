package flow_test

import (
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killedKenny/errtrace/internal/errset"
	"github.com/killedKenny/errtrace/internal/fixture"
	"github.com/killedKenny/errtrace/internal/flow"
)

// TestGuardCollection checks which guards Analyze finds for a returned
// value: those of the Ifs that dominate the return (or the Phi edge), with
// the outcome of the branch taken.
func TestGuardCollection(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "guards.txtar"))
	pkg := l.SSAPackage(l.Initial[0])
	tests := map[string]string{
		"And":           "guarded(comparison with io.EOF false)[callref example.com/fx.g #0]",
		"Or":            "guarded(comparison with io.EOF false; comparison with io.ErrUnexpectedEOF false)[callref example.com/fx.g #0]",
		"NotDominating": "callref example.com/fx.g #0",
		"Edge":          "guarded(comparison with io.EOF false)[callref example.com/fx.g #0]",
	}
	for name, want := range tests {
		tbl := errset.NewTable()
		lf := flow.Analyze(tbl, pkg.Func(name), flow.Options{})
		var facts []string
		for _, f := range lf.Results[0].Facts {
			facts = append(facts, tbl.String(f.Value))
		}
		if got := strings.Join(facts, "; "); got != want {
			t.Errorf("Analyze(%s) = %q, want %q", name, got, want)
		}
	}
}

// TestApplyGuard checks the outcome of guards on Error Origins.
func TestApplyGuard(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "guards.txtar"))
	io := l.SSA.ImportedPackage("io")
	if io == nil {
		t.Fatal("io is not loaded")
	}
	eof := io.Var("EOF").Object().(*types.Var)
	unexpected := io.Var("ErrUnexpectedEOF").Object().(*types.Var)
	tbl := errset.NewTable()
	sEOF := tbl.Intern(errset.Value{Kind: errset.Sentinel, Var: eof})
	sUnexp := tbl.Intern(errset.Value{Kind: errset.Sentinel, Var: unexpected})
	wrapped := tbl.Intern(errset.Value{Kind: errset.Wrap, WrapKind: errset.WrapErrorf, Format: "w: %w", Inner: []errset.ValueID{sEOF}})
	unres := tbl.Intern(errset.Value{Kind: errset.Unresolved, Unresolved: errset.Memory, Detail: "x"})
	param := tbl.Intern(errset.Value{Kind: errset.Param, Fn: "f", Name: "err"})
	tests := []struct {
		name string
		id   errset.ValueID
		g    errset.Guard
		want flow.Outcome
	}{
		{"eq false removes the sentinel", sEOF, errset.Guard{Kind: errset.GuardEqual, Var: eof}, flow.Remove},
		{"eq false keeps a wrap of it", wrapped, errset.Guard{Kind: errset.GuardEqual, Var: eof}, flow.Keep},
		{"eq true removes another sentinel", sUnexp, errset.Guard{Kind: errset.GuardEqual, Var: eof, Holds: true}, flow.Remove},
		{"eq false notes an unresolved value", unres, errset.Guard{Kind: errset.GuardEqual, Var: eof}, flow.Note},
		{"eq true keeps an unresolved value", unres, errset.Guard{Kind: errset.GuardEqual, Var: eof, Holds: true}, flow.Keep},
		{"is false removes a wrapped sentinel", wrapped, errset.Guard{Kind: errset.GuardIs, Var: eof}, flow.Remove},
		{"is true removes another sentinel", sUnexp, errset.Guard{Kind: errset.GuardIs, Var: eof, Holds: true}, flow.Remove},
		{"nil true removes everything", unres, errset.Guard{Kind: errset.GuardNil, Holds: true}, flow.Remove},
		{"a param is pending", param, errset.Guard{Kind: errset.GuardIs, Var: eof}, flow.Pending},
		{"a helper notes on its false outcome", sEOF, errset.Guard{Kind: errset.GuardHelper, Detail: "guarded by call to h"}, flow.Note},
		{"a helper keeps on its true outcome", sEOF, errset.Guard{Kind: errset.GuardHelper, Detail: "guarded by call to h", Holds: true}, flow.Keep},
	}
	for _, tt := range tests {
		if got, _ := flow.ApplyGuard(tbl, tt.id, tt.g); got != tt.want {
			t.Errorf("%s: ApplyGuard = %v, want %v", tt.name, got, tt.want)
		}
	}
}
