package render

import (
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/KilledKenny/errtrace/internal/errset"
	"github.com/KilledKenny/errtrace/internal/span"
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
			{Leaf: opaque, PossiblyHandled: []errset.GuardNote{{Site: "3:2", Reason: "guarded by call to example.com/fx.isRetryable"}}},
			{Leaf: unknown},
			{Leaf: unres, Path: errset.Path{{Site: "4:9", Func: "example.com/fx.Load"}}},
		},
		Locator: fakeLocator{},
	}
	var b strings.Builder
	if err := Text(&b, in); err != nil {
		t.Fatal(err)
	}
	want := `/m/a.go:9:6-10: Error Set of example.com/fx.Load: 5 entries, 1 unresolved, 1 possibly handled
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
	possibly handled:
		/m/a.go:3:2-2: guarded by call to example.com/fx.isRetryable
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

func TestTextFanout(t *testing.T) {
	pkg := types.NewPackage("example.com/fx", "fx")
	errType := types.Universe.Lookup("error").Type()
	tbl := errset.NewTable()
	var entries []errset.Entry
	dyn := func(site string, n int, callee string) errset.Hop {
		return errset.Hop{Site: site, Func: "example.com/fx.use", Callee: callee, Dynamic: true,
			Method: "(example.com/fx.Getter).Get", Candidates: n, CandidatesBefore: n}
	}
	for i, name := range []string{"ErrA", "ErrB", "ErrC"} {
		id := tbl.Intern(errset.Value{Kind: errset.Sentinel, Site: "1:" + string(rune('1'+i)), Var: types.NewVar(token.NoPos, pkg, name, errType)})
		callee := "(example.com/fx." + name[3:] + ").Get"
		entries = append(entries, errset.Entry{Leaf: id, Path: errset.Path{
			{Site: "2:1", Func: callee},
			dyn("7:9", 3, callee),
		}})
	}
	// A dynamic site with fewer Candidates than the fan-out is not grouped.
	other := tbl.Intern(errset.Value{Kind: errset.Sentinel, Site: "1:9", Var: types.NewVar(token.NoPos, pkg, "ErrD", errType)})
	entries = append(entries, errset.Entry{Leaf: other, Path: errset.Path{{Site: "3:1", Func: "example.com/fx.D"}, dyn("8:9", 1, "example.com/fx.D")}})
	in := Input{
		TargetSpan: span.Span{File: "/m/a.go", Start: span.Point{Line: 9, Col: 6, Offset: -1}, End: span.Point{Line: 9, Col: 10, Offset: -1}},
		TargetName: "example.com/fx.use",
		Table:      tbl,
		Entries:    entries,
		Locator:    fakeLocator{},
		Fanout:     3,
	}
	var b strings.Builder
	if err := Text(&b, in); err != nil {
		t.Fatal(err)
	}
	want := `/m/a.go:9:6-10: Error Set of example.com/fx.use: 4 entries
/m/a.go:1:9-9: sentinel example.com/fx.ErrD
	path (origin → target):
		/m/a.go:3:1-1: in example.com/fx.D
		/m/a.go:8:9-9: in example.com/fx.use, call to example.com/fx.D (dynamic (example.com/fx.Getter).Get: 1 of 1 candidates)
/m/a.go:7:9-9: group of 3 entries via dynamic call to (example.com/fx.Getter).Get (3 candidates) in example.com/fx.use
	/m/a.go:1:1-1: sentinel example.com/fx.ErrA (candidate (example.com/fx.A).Get)
	/m/a.go:1:2-2: sentinel example.com/fx.ErrB (candidate (example.com/fx.B).Get)
	/m/a.go:1:3-3: sentinel example.com/fx.ErrC (candidate (example.com/fx.C).Get)
`
	if got := b.String(); got != want {
		t.Errorf("Text with fan-out 3 =\n%s\nwant\n%s", got, want)
	}
	in.Fanout = 4
	b.Reset()
	if err := Text(&b, in); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "group of") {
		t.Errorf("Text with fan-out 4 grouped entries:\n%s", b.String())
	}
}
