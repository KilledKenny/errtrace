package render

import (
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/KilledKenny/errtrace/internal/errset"
	"github.com/KilledKenny/errtrace/internal/span"
)

// TestMembershipGroups pins what a group of Carriers prints: one group
// for a pivot that says the same at several places (the header counts
// them), only the steps that every trace of the group starts with, the
// entry lines with the annotations Text prints, the Wrap that makes an
// entry a Carrier, and entries whose lines are the same once, with their
// number.
func TestMembershipGroups(t *testing.T) {
	pkg := types.NewPackage("example.com/fx", "fx")
	errType := types.Universe.Lookup("error").Type()
	named := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "MyErr", nil), types.NewStruct(nil, nil), nil)
	tbl := errset.NewTable()
	sent := tbl.Intern(errset.Value{Kind: errset.Sentinel, Site: "1:5", Var: types.NewVar(token.NoPos, pkg, "ErrY", errType)})
	typedNil := tbl.Intern(errset.Value{Kind: errset.Concrete, Site: "2:9", Type: types.NewPointer(named), TypedNil: true})
	unres := tbl.Intern(errset.Value{Kind: errset.Unresolved, Site: "4:9", Unresolved: errset.Memory, Detail: "value of TypeAssert not modelled"})
	wrap := tbl.Intern(errset.Value{Kind: errset.Wrap, Site: "5:9", WrapKind: errset.WrapUnwrapMethod, Type: types.NewPointer(named), Inner: []errset.ValueID{sent}})
	origin := TraceStep{Site: "1:1", Desc: "Sentinel example.com/fx.ErrX"}
	pivot := func(site string) TraceStep {
		return TraceStep{Site: site, Desc: "value of type example.com/fx.W, which may wrap example.com/fx.ErrX", Wide: true}
	}
	in := MembershipInput{
		Input: Input{
			TargetSpan: span.Span{File: "/m/a.go", Start: span.Point{Line: 9, Col: 6, Offset: -1}, End: span.Point{Line: 9, Col: 10, Offset: -1}},
			TargetName: "example.com/fx.Load",
			Table:      tbl,
			Locator:    fakeLocator{},
		},
		Subject: "example.com/fx.ErrX",
		Verdict: "maybe",
		Carriers: []CarrierEntry{
			// The same pivot at three places, one of them without a
			// span: one group.
			{Entry: errset.Entry{Leaf: sent, Chain: []errset.ValueID{wrap}, MorePaths: 1}, Trace: []TraceStep{origin, pivot("6:1")}, Wrap: wrap, HasWrap: true},
			{Entry: errset.Entry{Leaf: typedNil}, Trace: []TraceStep{origin, pivot("7:1")}},
			{Entry: errset.Entry{Leaf: unres, Path: errset.Path{{Site: "4:9", Func: "example.com/fx.a"}}}, Unresolved: true, Trace: []TraceStep{origin, pivot("")}},
			{Entry: errset.Entry{Leaf: unres, Path: errset.Path{{Site: "4:9", Func: "example.com/fx.b"}}}, Unresolved: true, Trace: []TraceStep{origin, pivot("6:1")}},
		},
		NotFollowed: []ReflectStore{{Site: "8:2", Function: "example.com/fx.set"}},
	}
	var b strings.Builder
	if err := Membership(&b, in); err != nil {
		t.Fatal(err)
	}
	want := `/m/a.go:9:6-10: example.com/fx.ErrX in Error Set of example.com/fx.Load: maybe: 4 entries could carry it (2 unresolved)
/m/a.go:6:1-1: group of 4 entries via: value of type example.com/fx.W, which may wrap example.com/fx.ErrX (at 3 places)
	marked via (origin of example.com/fx.ErrX → here):
		/m/a.go:1:1-1: Sentinel example.com/fx.ErrX
	/m/a.go:1:5-5: sentinel example.com/fx.ErrY [+1 more paths] under *example.com/fx.MyErr.Unwrap
	/m/a.go:2:9-9: concrete *example.com/fx.MyErr [typed nil]
	/m/a.go:4:9-9: unresolved(memory): value of TypeAssert not modelled (2 entries)
	not followed: example.com/fx.ErrX reaches 1 reflective store whose destination is unknown or only partly followed:
		/m/a.go:8:2-2: in example.com/fx.set
`
	if got := b.String(); got != want {
		t.Errorf("Membership =\n%s\nwant\n%s", got, want)
	}
}

// TestMembershipOneCarrier pins the header of a single Carrier.
func TestMembershipOneCarrier(t *testing.T) {
	tbl := errset.NewTable()
	unres := tbl.Intern(errset.Value{Kind: errset.Unresolved, Site: "4:9", Unresolved: errset.Memory, Detail: "load of field err of *example.com/fx.S"})
	in := MembershipInput{
		Input: Input{
			TargetSpan: span.Span{File: "/m/a.go", Start: span.Point{Line: 9, Col: 6, Offset: -1}, End: span.Point{Line: 9, Col: 10, Offset: -1}},
			TargetName: "example.com/fx.Load",
			Table:      tbl,
			Locator:    fakeLocator{},
		},
		Subject:  "example.com/fx.ErrX",
		Verdict:  "maybe",
		Carriers: []CarrierEntry{{Entry: errset.Entry{Leaf: unres}, Unresolved: true}},
	}
	var b strings.Builder
	if err := Membership(&b, in); err != nil {
		t.Fatal(err)
	}
	want := `/m/a.go:9:6-10: example.com/fx.ErrX in Error Set of example.com/fx.Load: maybe: 1 entry could carry it (1 unresolved)
/m/a.go:4:9-9: unresolved(memory): load of field err of *example.com/fx.S
`
	if got := b.String(); got != want {
		t.Errorf("Membership =\n%s\nwant\n%s", got, want)
	}
}

// TestMembershipGroupSteps pins the steps a group prints: those that the
// traces of all its entries start with, the pivot among them only if it
// is at one place for all, and none if the traces start differently.
func TestMembershipGroupSteps(t *testing.T) {
	tbl := errset.NewTable()
	a := tbl.Intern(errset.Value{Kind: errset.Unresolved, Site: "4:9", Unresolved: errset.Memory, Detail: "load a"})
	c := tbl.Intern(errset.Value{Kind: errset.Unresolved, Site: "5:9", Unresolved: errset.Memory, Detail: "load b"})
	origin := TraceStep{Site: "1:1", Desc: "Sentinel example.com/fx.ErrX"}
	other := TraceStep{Site: "2:1", Desc: "stored to variable example.com/fx.v"}
	pivot := TraceStep{Site: "6:1", Desc: "loaded from field f of example.com/fx.T", Wide: true}
	after := TraceStep{Site: "7:1", Desc: "returned by example.com/fx.g"}
	input := func(first, second []TraceStep) MembershipInput {
		return MembershipInput{
			Input: Input{
				TargetSpan: span.Span{File: "/m/a.go", Start: span.Point{Line: 9, Col: 6, Offset: -1}, End: span.Point{Line: 9, Col: 10, Offset: -1}},
				TargetName: "example.com/fx.Load",
				Table:      tbl,
				Locator:    fakeLocator{},
			},
			Subject: "example.com/fx.ErrX",
			Verdict: "maybe",
			Carriers: []CarrierEntry{
				{Entry: errset.Entry{Leaf: a}, Unresolved: true, Trace: first},
				{Entry: errset.Entry{Leaf: c}, Unresolved: true, Trace: second},
			},
		}
	}
	const head = "/m/a.go:9:6-10: example.com/fx.ErrX in Error Set of example.com/fx.Load: maybe: 2 entries could carry it (2 unresolved)\n" +
		"/m/a.go:6:1-1: group of 2 entries via: loaded from field f of example.com/fx.T\n"
	const entries = "\t/m/a.go:4:9-9: unresolved(memory): load a\n" +
		"\t/m/a.go:5:9-9: unresolved(memory): load b\n"
	for _, tc := range []struct {
		name          string
		first, second []TraceStep
		want          string
	}{
		{
			name:   "same up to the pivot",
			first:  []TraceStep{origin, pivot, after},
			second: []TraceStep{origin, pivot},
			want: head +
				"\tmarked via (origin of example.com/fx.ErrX → here):\n" +
				"\t\t/m/a.go:1:1-1: Sentinel example.com/fx.ErrX\n" +
				"\t\t/m/a.go:6:1-1: loaded from field f of example.com/fx.T\n" +
				entries,
		},
		{
			name:   "one has a step more before the pivot",
			first:  []TraceStep{origin, pivot},
			second: []TraceStep{origin, other, pivot},
			want: head +
				"\tmarked via (origin of example.com/fx.ErrX → here):\n" +
				"\t\t/m/a.go:1:1-1: Sentinel example.com/fx.ErrX\n" +
				entries,
		},
		{
			name:   "nothing in common but the pivot",
			first:  []TraceStep{origin, pivot},
			second: []TraceStep{other, pivot},
			want:   head + entries,
		},
	} {
		var b strings.Builder
		if err := Membership(&b, input(tc.first, tc.second)); err != nil {
			t.Fatal(err)
		}
		if got := b.String(); got != tc.want {
			t.Errorf("%s: Membership =\n%s\nwant\n%s", tc.name, got, tc.want)
		}
	}
}

// TestMembershipHeaders pins the first line of a no, of a no that names
// stores through reflection that were not followed (with the two parts of
// that section, and how it is cut short), and of a maybe without
// Carriers.
func TestMembershipHeaders(t *testing.T) {
	base := MembershipInput{
		Input: Input{
			TargetSpan: span.Span{File: "/m/a.go", Start: span.Point{Line: 9, Col: 6, Offset: -1}, End: span.Point{Line: 9, Col: 10, Offset: -1}},
			TargetName: "example.com/fx.Load",
			Table:      errset.NewTable(),
			Locator:    fakeLocator{},
		},
		Subject: "example.com/fx.ErrX",
	}
	const head = "/m/a.go:9:6-10: example.com/fx.ErrX in Error Set of example.com/fx.Load: "
	no := base
	no.Verdict = "no"
	apart := no
	apart.NotFollowed = []ReflectStore{{Site: "8:2", Function: "example.com/fx.set"}}
	// Two stores that the Subject reaches, and five that only the
	// flooding pass records: five are printed in all, unless Full.
	both := no
	both.NotFollowed = []ReflectStore{{Site: "8:2", Function: "example.com/fx.set"}, {Site: "8:4", Function: "example.com/fx.set"}}
	for range 5 {
		both.NotFollowed = append(both.NotFollowed, ReflectStore{Site: "7:2", Function: "example.com/fx.copy", FloodOnly: true})
	}
	bothFull := both
	bothFull.Full = true
	// More stores that the Subject reaches than are printed.
	many := no
	for range 6 {
		many.NotFollowed = append(many.NotFollowed, ReflectStore{Site: "8:2", Function: "example.com/fx.set"})
	}
	many.NotFollowed = append(many.NotFollowed, ReflectStore{Site: "7:2", Function: "example.com/fx.copy", FloodOnly: true})
	const (
		notFollowed2 = "\tnot followed: example.com/fx.ErrX reaches 2 reflective stores whose destination is unknown or only partly followed:\n" +
			"\t\t/m/a.go:8:2-2: in example.com/fx.set\n" +
			"\t\t/m/a.go:8:4-4: in example.com/fx.set\n" +
			"\talso, once those are taken to write anywhere, 5 more:\n"
		copyLine = "\t\t/m/a.go:7:2-2: in example.com/fx.copy\n"
		setLine  = "\t\t/m/a.go:8:2-2: in example.com/fx.set\n"
	)
	maybe := base
	maybe.Verdict = "maybe"
	maybe.TargetTrace = []TraceStep{{Site: "1:1", Desc: "Sentinel example.com/fx.ErrX"}}
	for _, tc := range []struct {
		name string
		in   MembershipInput
		want string
	}{
		{"no", no, head + "no\n"},
		{"no with stores not followed", apart, head + "no, apart from reflection\n" +
			"\tnot followed: example.com/fx.ErrX reaches 1 reflective store whose destination is unknown or only partly followed:\n" +
			"\t\t/m/a.go:8:2-2: in example.com/fx.set\n"},
		{"no with stores of the flooding pass", both, head + "no, apart from reflection\n" +
			notFollowed2 + strings.Repeat(copyLine, 3) + "\t\t… and 2 more\n"},
		{"no with stores of the flooding pass, full", bothFull, head + "no, apart from reflection\n" +
			notFollowed2 + strings.Repeat(copyLine, 5)},
		{"no with more stores than are printed", many, head + "no, apart from reflection\n" +
			"\tnot followed: example.com/fx.ErrX reaches 6 reflective stores whose destination is unknown or only partly followed:\n" +
			strings.Repeat(setLine, 5) + "\t\t… and 1 more\n" +
			"\talso, once those are taken to write anywhere, 1 more:\n" +
			"\t\t… and 1 more\n"},
		{"maybe without Carriers", maybe, head + "maybe: no entry of the Error Set shows how\n" +
			"\tmarked via (origin of example.com/fx.ErrX → target):\n" +
			"\t\t/m/a.go:1:1-1: Sentinel example.com/fx.ErrX\n"},
	} {
		var b strings.Builder
		if err := Membership(&b, tc.in); err != nil {
			t.Fatal(err)
		}
		if got := b.String(); got != tc.want {
			t.Errorf("%s: Membership =\n%s\nwant\n%s", tc.name, got, tc.want)
		}
	}
}
