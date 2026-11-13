package mark_test

import (
	"fmt"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/fixture"
	"github.com/KilledKenny/errtrace/internal/mark"
)

// subject resolves "is <Name>" (a Sentinel) or "as <type>" (a Concrete
// Type, maybe with a leading *) in the fixture's package.
func subject(t *testing.T, pkg *ssa.Package, s string) mark.Subject {
	t.Helper()
	kind, name, _ := strings.Cut(s, " ")
	switch kind {
	case "is":
		g, ok := pkg.Members[name].(*ssa.Global)
		if !ok {
			t.Fatalf("no variable %s", name)
		}
		return mark.Subject{Sentinel: g}
	case "as":
		ptr := strings.HasPrefix(name, "*")
		obj, ok := pkg.Pkg.Scope().Lookup(strings.TrimPrefix(name, "*")).(*types.TypeName)
		if !ok {
			t.Fatalf("no type %s", name)
		}
		typ := obj.Type()
		if ptr {
			typ = types.NewPointer(typ)
		}
		return mark.Subject{Type: typ}
	}
	t.Fatalf("bad subject %q", s)
	return mark.Subject{}
}

// results returns the indices of the marked results of fn, as "0,1", or
// "-" if none is marked.
func results(res *mark.Result, fn *ssa.Function) string {
	var out []string
	for i := range fn.Signature.Results().Len() {
		if res.ResultMarked(fn, i) {
			out = append(out, fmt.Sprint(i))
		}
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ",")
}

type markTest struct {
	subject string
	// want maps a function to its marked results.
	want map[string]string
}

func runMarkTests(t *testing.T, file string, callees func(*fixture.Loaded) mark.Callees, tests []markTest) {
	t.Helper()
	l := fixture.LoadProgram(t, filepath.Join("testdata", file))
	pkg := l.SSAPackage(l.Initial[0])
	res := callees(l)
	for _, tt := range tests {
		r, err := mark.Run(t.Context(), l.SSA, res, subject(t, pkg, tt.subject))
		if err != nil {
			t.Fatal(err)
		}
		for name, want := range tt.want {
			fn := pkg.Func(name)
			if fn == nil {
				t.Errorf("%s: no function %s", tt.subject, name)
				continue
			}
			if got := results(r, fn); got != want {
				t.Errorf("%s: marked results of %s = %s, want %s", tt.subject, name, got, want)
			}
		}
	}
}

// noCalls is the Callees of a program whose calls are not followed.
type noCalls struct{}

func (noCalls) Callees(ssa.CallInstruction) []*ssa.Function { return nil }
func (noCalls) Callers(*ssa.Function) []ssa.CallInstruction { return nil }

func TestIntra(t *testing.T) {
	t.Parallel()
	runMarkTests(t, "intra.txtar", func(*fixture.Loaded) mark.Callees { return noCalls{} }, []markTest{
		{"is ErrX", map[string]string{
			"Direct": "0", "Other": "-", "Alias": "0", "New": "-", "Compare": "-",
			"Phi": "0", "Two": "1",
			"WrapW": "0", "WrapV": "-", "WrapSecond": "-", "WrapIndexed": "0", "WrapDyn": "0",
			"Join": "0", "JoinOther": "-", "Unwrap": "0",
			"FieldLoad": "0", "FieldOther": "-", "FieldValue": "0", "FieldRenamed": "0", "FieldTagged": "0",
			"GlobalLoad": "0", "UnsetLoad": "-",
			"SliceLoad": "0", "ArrayLoad": "0", "RangeLoad": "0",
			"MapLoad": "0", "MapOther": "-", "MapRange": "0",
			"ChanRecv": "0", "ChanSelect": "0",
			"PtrLoad": "0", "Local": "-", "LocalEscaped": "0",
			// A wrapper type is marked as a whole: every *wrapErr.
			"MakeWrap": "0", "MakeWrapOther": "0", "WrapTyped": "0",
			"MakeIs": "0", "MakeMulti": "0",
			"MakePlain": "-", "PlainTyped": "-",
			"As": "0", "AsOther": "-", "AsPlain": "-",
			"Assert": "0", "AssertPlain": "-",
			"Recover": "0", "Closure": "0", "ClosureOther": "-",
			"MakeVal": "-",
		}},
		{"is ErrY", map[string]string{
			"Direct": "-", "Other": "0", "Alias": "-", "Phi": "0", "Two": "0",
			"WrapSecond": "0", "WrapIndexed": "-", "Join": "0", "JoinOther": "0",
			"FieldLoad": "-", "GlobalLoad": "-", "MapLoad": "-", "ChanRecv": "-",
			"Local": "0", "MakeWrap": "0", "MakeIs": "-",
			"Recover": "-", "Closure": "-", "ClosureOther": "0", "AsOther": "0",
		}},
		{"is ErrAlias", map[string]string{"Direct": "0", "Alias": "0", "Other": "-"}},
		// A value of a comparable non-pointer type may equal the Sentinel.
		{"is ErrVal", map[string]string{"MakeVal": "0", "Direct": "-", "MakePlain": "-"}},
		{"as *plain", map[string]string{
			"MakePlain": "0", "PlainTyped": "0", "AsPlain": "0", "AssertPlain": "0",
			"Direct": "-", "MakeWrap": "-", "WrapW": "-", "MakeVal": "-",
		}},
		{"as *wrapErr", map[string]string{"MakeWrap": "0", "MakeWrapOther": "0", "WrapTyped": "0", "MakePlain": "-", "Direct": "-"}},
		{"as valErr", map[string]string{"MakeVal": "0", "MakePlain": "-"}},
	})
}
