package load_test

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/fixture"
	"github.com/killedKenny/errtrace/internal/load"
)

func TestLoadTypeError(t *testing.T) {
	t.Parallel()
	dir, _ := fixture.Extract(t, filepath.Join("testdata", "type_error.txtar"))
	_, err := load.Load(t.Context(), load.Config{Dir: dir, Env: fixture.Env()})
	if err == nil {
		t.Fatal("Load succeeded, want a type error")
	}
	want := filepath.Join(dir, "a.go") + ":4:9: undefined: undefinedThing"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestFileForPrefersNonTestVariant(t *testing.T) {
	t.Parallel()
	dir, _ := fixture.Extract(t, filepath.Join("testdata", "spans.txtar"))
	prog, err := load.Load(t.Context(), load.Config{Dir: dir, Env: fixture.Env(), Tests: true})
	if err != nil {
		t.Fatal(err)
	}
	pkg, _, ok := prog.FileFor(filepath.Join(dir, "a.go"))
	if !ok {
		t.Fatal("a.go not found")
	}
	if pkg.ID != "example.com/fx" {
		t.Errorf("FileFor(a.go) package = %s, want example.com/fx", pkg.ID)
	}
	pkg, _, ok = prog.FileFor(filepath.Join(dir, "a_test.go"))
	if !ok || pkg.ID != "example.com/fx [example.com/fx.test]" {
		t.Errorf("FileFor(a_test.go) = %v, %v; want the test variant", pkg, ok)
	}
	if _, _, ok := prog.FileFor(filepath.Join(dir, "missing.go")); ok {
		t.Error("FileFor(missing.go) found a package")
	}
}

func TestSpans(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "spans.txtar"))
	rel := func(s string) string { return strings.TrimPrefix(s, l.Dir+string(filepath.Separator)) }
	pkg, _, ok := l.FileFor(filepath.Join(l.Dir, "a.go"))
	if !ok {
		t.Fatal("a.go not found")
	}
	fn := l.SSAPackage(pkg).Func("F")
	var got []string
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			ret, ok := instr.(*ssa.Return)
			if !ok {
				continue
			}
			v := ret.Results[0]
			s, ok := l.SpanOfValue(v)
			if !ok {
				t.Errorf("no span for %s", v)
				continue
			}
			got = append(got, rel(s.String()))
		}
	}
	// &MyErr{} reaches the return through an implicit MakeInterface (no
	// position): its operand's span is used. g() is a call: its call
	// expression is used.
	want := "a.go:13:10-18 a.go:15:9-12"
	if strings.Join(got, " ") != want {
		t.Errorf("return operand spans = %s, want %s", strings.Join(got, " "), want)
	}

	// A Global's span is its identifier.
	glob := l.SSAPackage(pkg).Var("ErrBad")
	if s, ok := l.SpanOfValue(glob); !ok || rel(s.String()) != "a.go:5:5-11" {
		t.Errorf("SpanOfValue(ErrBad) = %s, %v; want a.go:5:5-11", rel(s.String()), ok)
	}

	// In the stdlib (no debug info), the value errors.New returns is found
	// through the innermost expression enclosing its operand's position.
	errPkg := l.SSA.ImportedPackage("errors")
	if errPkg == nil {
		t.Fatal("errors package not built")
	}
	var ret *ssa.Return
	for _, b := range errPkg.Func("New").Blocks {
		if r, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return); ok {
			ret = r
		}
	}
	s, ok := l.SpanOfValue(ret.Results[0])
	if !ok || filepath.Base(s.File) != "errors.go" || s.IsPoint() {
		t.Errorf("SpanOfValue(errors.New result) = %s, %v; want an expression in errors.go", s, ok)
	}
}
