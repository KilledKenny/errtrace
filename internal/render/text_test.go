package render

import (
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/killedKenny/errtrace/internal/errset"
	"github.com/killedKenny/errtrace/internal/span"
)

// fakeLocator gives every string site the span "/m/a.go:<site>".
type fakeLocator struct{}

func (fakeLocator) Locate(site any) (span.Span, bool) {
	s, ok := site.(string)
	if !ok {
		return span.Span{}, false
	}
	var line, col int
	for i, part := range strings.SplitN(s, ":", 2) {
		n := 0
		for _, r := range part {
			n = n*10 + int(r-'0')
		}
		if i == 0 {
			line = n
		} else {
			col = n
		}
	}
	p := span.Point{Line: line, Col: col, Offset: -1}
	return span.Span{File: "/m/a.go", Start: p, End: p}, true
}

func TestText(t *testing.T) {
	pkg := types.NewPackage("example.com/fx", "fx")
	errType := types.Universe.Lookup("error").Type()
	named := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "MyErr", nil), types.NewStruct(nil, nil), nil)
	tbl := errset.NewTable()
	sent := tbl.Intern(errset.Value{Kind: errset.Sentinel, Site: "1:5", Var: types.NewVar(token.NoPos, pkg, "ErrX", errType)})
	typedNil := tbl.Intern(errset.Value{Kind: errset.Concrete, Site: "2:9", Type: types.NewPointer(named), TypedNil: true})
	opaque := tbl.Intern(errset.Value{Kind: errset.Opaque, Site: "3:9", Constructor: "errors.New", Message: "a \"b\"", HasMessage: true})
	unknown := tbl.Intern(errset.Value{Kind: errset.Opaque, Site: 42})
	unres := tbl.Intern(errset.Value{Kind: errset.Unresolved, Site: "4:9", Unresolved: errset.Memory, Detail: "load of field err of *example.com/fx.S"})
	wrap := tbl.Intern(errset.Value{Kind: errset.Wrap, Site: "5:9", WrapKind: errset.WrapErrorf, Format: "load: %w", Inner: []errset.ValueID{sent}})

	in := Input{
		TargetSpan: span.Span{File: "/m/a.go", Start: span.Point{Line: 9, Col: 6, Offset: -1}, End: span.Point{Line: 9, Col: 10, Offset: -1}},
		TargetName: "example.com/fx.Load",
		Table:      tbl,
		Entries: []errset.Entry{
			{Leaf: sent, Chain: []errset.ValueID{wrap}, Truncated: true, MorePaths: 2, Path: errset.Path{
				{Site: "1:20", Func: "example.com/fx.inner"},
				{Site: "6:3", Func: "example.com/fx.Load", Callee: "example.com/fx.inner"},
			}},
			{Leaf: typedNil, Path: errset.Path{{Site: "2:9", Func: "example.com/fx.Load"}}},
			{Leaf: opaque},
			{Leaf: unknown},
			{Leaf: unres, Path: errset.Path{{Site: "4:9", Func: "example.com/fx.Load"}}},
		},
		Locator: fakeLocator{},
	}
	var b strings.Builder
	if err := Text(&b, in); err != nil {
		t.Fatal(err)
	}
	want := `/m/a.go:9:6-10: Error Set of example.com/fx.Load: 5 entries, 1 unresolved
/m/a.go:1:5-5: sentinel example.com/fx.ErrX [chain truncated] [+2 more paths]
	wrap chain (origin → target):
		/m/a.go:5:9-9: fmt.Errorf "load: %w"
	path (origin → target):
		/m/a.go:1:20-20: in example.com/fx.inner
		/m/a.go:6:3-3: in example.com/fx.Load, call to example.com/fx.inner
/m/a.go:2:9-9: concrete *example.com/fx.MyErr [typed nil]
	path (origin → target):
		/m/a.go:2:9-9: in example.com/fx.Load
/m/a.go:3:9-9: opaque errors.New "a \"b\""
-: opaque
/m/a.go:4:9-9: unresolved(memory): load of field err of *example.com/fx.S
	path (origin → target):
		/m/a.go:4:9-9: in example.com/fx.Load
`
	if got := b.String(); got != want {
		t.Errorf("Text =\n%s\nwant\n%s", got, want)
	}
}

func TestTextEmpty(t *testing.T) {
	in := Input{
		TargetSpan: span.Span{File: "/m/a.go", Start: span.Point{Line: 1, Col: 6, Offset: -1}, End: span.Point{Line: 1, Col: 7, Offset: -1}},
		TargetName: "example.com/fx.F",
		Table:      errset.NewTable(),
	}
	var b strings.Builder
	if err := Text(&b, in); err != nil {
		t.Fatal(err)
	}
	if got, want := b.String(), "/m/a.go:1:6-7: Error Set of example.com/fx.F: 0 entries\n"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}
