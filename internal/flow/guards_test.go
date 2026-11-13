package flow_test

import (
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KilledKenny/errtrace/internal/errset"
	"github.com/KilledKenny/errtrace/internal/fixture"
	"github.com/KilledKenny/errtrace/internal/flow"
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
	sents := flow.NewSentinels()
	eofInfo := sents.Info(io.Var("EOF"))
	if !eofInfo.Fresh || eofInfo.Canon != io.Var("EOF") {
		t.Fatalf("Info(io.EOF) = %+v, want a Fresh value of its own", eofInfo)
	}
	unexpInfo := sents.Info(io.Var("ErrUnexpectedEOF"))
	tbl := errset.NewTable()
	sEOF := tbl.Intern(errset.Value{Kind: errset.Sentinel, Var: eof, Canon: eof, Fresh: true})
	sUnexp := tbl.Intern(errset.Value{Kind: errset.Sentinel, Var: unexpected, Canon: unexpected, Fresh: unexpInfo.Fresh, Type: unexpInfo.Type})
	// A Fresh Sentinel whose dynamic type errtrace does not know.
	sUntyped := tbl.Intern(errset.Value{Kind: errset.Sentinel, Var: io.Var("ErrClosedPipe").Object().(*types.Var), Fresh: true})
	// Values whose Canon and Fresh are set by hand (they follow from Var
	// and are not part of a value's identity, so each needs its own Var).
	unfresh := tbl.Intern(errset.Value{Kind: errset.Sentinel, Var: io.Var("ErrShortBuffer").Object().(*types.Var)})
	alias := tbl.Intern(errset.Value{Kind: errset.Sentinel, Var: io.Var("ErrNoProgress").Object().(*types.Var), Canon: eof})
	wrapped := tbl.Intern(errset.Value{Kind: errset.Wrap, WrapKind: errset.WrapErrorf, Format: "w: %w", Inner: []errset.ValueID{sEOF}})
	wrappedMayNil := tbl.Intern(errset.Value{Kind: errset.Wrap, WrapKind: errset.WrapErrorf, Format: "w: %w", Inner: []errset.ValueID{sEOF}, MayHoldNil: true})
	unres := tbl.Intern(errset.Value{Kind: errset.Unresolved, Unresolved: errset.Memory, Detail: "x"})
	param := tbl.Intern(errset.Value{Kind: errset.Param, Fn: "f", Name: "err"})
	tests := []struct {
		name string
		id   errset.ValueID
		g    errset.Guard
		want flow.Outcome
	}{
		{"eq false removes the sentinel", sEOF, errset.Guard{Kind: errset.GuardEqual, Var: eof}, flow.Remove},
		{"eq false keeps a wrap of it", wrapped, errset.Guard{Kind: errset.GuardEqual, Var: eof, Canon: eof, Fresh: true}, flow.Keep},
		{"eq false notes a wrap that a sentinel not Fresh may hold", wrapped, errset.Guard{Kind: errset.GuardEqual, Var: eof}, flow.Note},
		{"eq true keeps a wrap that a sentinel not Fresh may hold", wrapped, errset.Guard{Kind: errset.GuardEqual, Var: eof, Holds: true}, flow.Keep},
		{"eq true removes a wrap against a Fresh sentinel", wrapped, errset.Guard{Kind: errset.GuardEqual, Var: eof, Canon: eof, Fresh: true, Holds: true}, flow.Remove},
		{"eq false notes the sentinel of an unstable variable", sEOF, errset.Guard{Kind: errset.GuardEqual, Var: eof, Canon: eof, Unstable: true}, flow.Note},
		{"eq true keeps the sentinel of an unstable variable", sEOF, errset.Guard{Kind: errset.GuardEqual, Var: eof, Canon: eof, Unstable: true, Holds: true}, flow.Keep},
		{"eq true removes another sentinel", sUnexp, errset.Guard{Kind: errset.GuardEqual, Var: eof, Canon: eof, Fresh: true, Holds: true}, flow.Remove},
		{"eq true keeps a sentinel that may alias", unfresh, errset.Guard{Kind: errset.GuardEqual, Var: eof, Canon: eof, Fresh: true, Holds: true}, flow.Keep},
		{"eq false notes a sentinel that may alias", unfresh, errset.Guard{Kind: errset.GuardEqual, Var: eof, Canon: eof, Fresh: true}, flow.Note},
		{"eq false removes an alias", alias, errset.Guard{Kind: errset.GuardEqual, Var: eof, Canon: eof, Fresh: true}, flow.Remove},
		{"eq false notes an unresolved value", unres, errset.Guard{Kind: errset.GuardEqual, Var: eof}, flow.Note},
		{"eq true keeps an unresolved value", unres, errset.Guard{Kind: errset.GuardEqual, Var: eof, Holds: true}, flow.Keep},
		{"is false removes a wrapped sentinel", wrapped, errset.Guard{Kind: errset.GuardIs, Var: eof}, flow.Remove},
		{"is true removes another sentinel", sUnexp, errset.Guard{Kind: errset.GuardIs, Var: eof, Canon: eof, Fresh: true, Holds: true}, flow.Remove},
		{"is true keeps a sentinel of unknown type, which may have an Is method", sUntyped, errset.Guard{Kind: errset.GuardIs, Var: eof, Canon: eof, Fresh: true, Holds: true}, flow.Keep},
		{"is false keeps a wrap that may hold nothing", wrappedMayNil, errset.Guard{Kind: errset.GuardIs, Var: eof}, flow.Note},
		{"is true keeps a wrap that may hold nothing", wrappedMayNil, errset.Guard{Kind: errset.GuardIs, Var: eof, Holds: true}, flow.Keep},
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
