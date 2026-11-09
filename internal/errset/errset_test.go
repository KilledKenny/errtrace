package errset

import (
	"go/token"
	"go/types"
	"reflect"
	"strings"
	"testing"
)

// testVars returns two package-level error variables of package
// example.com/fx.
func testVars() (pkg *types.Package, a, b *types.Var) {
	pkg = types.NewPackage("example.com/fx", "fx")
	errType := types.Universe.Lookup("error").Type()
	a = types.NewVar(token.NoPos, pkg, "ErrA", errType)
	b = types.NewVar(token.NoPos, pkg, "ErrB", errType)
	return pkg, a, b
}

func TestInternEqualValues(t *testing.T) {
	pkg, a, b := testVars()
	tbl := NewTable()
	named := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "MyErr", nil), types.NewStruct(nil, nil), nil)

	sa1 := tbl.Intern(Value{Kind: Sentinel, Var: a, Site: "first"})
	sa2 := tbl.Intern(Value{Kind: Sentinel, Var: a, Site: "second"})
	sb := tbl.Intern(Value{Kind: Sentinel, Var: b})
	if sa1 != sa2 {
		t.Errorf("equal Sentinels got IDs %d and %d", sa1, sa2)
	}
	if sa1 == sb {
		t.Errorf("different Sentinels share ID %d", sa1)
	}
	if got := tbl.Get(sa2).Site; got != "first" {
		t.Errorf("Site = %v, want the first interned one", got)
	}

	// Distinct but identical pointer types intern to one value.
	c1 := tbl.Intern(Value{Kind: Concrete, Type: types.NewPointer(named), Pos: 10})
	c2 := tbl.Intern(Value{Kind: Concrete, Type: types.NewPointer(named), Pos: 10})
	c3 := tbl.Intern(Value{Kind: Concrete, Type: types.NewPointer(named), Pos: 20})
	c4 := tbl.Intern(Value{Kind: Concrete, Type: named, Pos: 10})
	if c1 != c2 {
		t.Errorf("equal Concrete Types got IDs %d and %d", c1, c2)
	}
	if c1 == c3 || c1 == c4 {
		t.Errorf("Concrete Types at another position or of another type share an ID")
	}

	w1 := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapJoin, Inner: []ValueID{sa1, sb}})
	w2 := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapJoin, Inner: []ValueID{sa1, sb}})
	w3 := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapJoin, Inner: []ValueID{sb, sa1}})
	if w1 != w2 || w1 == w3 {
		t.Errorf("Wrap interning: w1=%d w2=%d w3=%d", w1, w2, w3)
	}
	if tbl.Len() != 7 {
		t.Errorf("Len = %d, want 7", tbl.Len())
	}
}

func TestInternCopiesInner(t *testing.T) {
	_, a, _ := testVars()
	tbl := NewTable()
	s := tbl.Intern(Value{Kind: Sentinel, Var: a})
	inner := []ValueID{s}
	w := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapJoin, Inner: inner})
	inner[0] = 99
	if got := tbl.Get(w).Inner[0]; got != s {
		t.Errorf("Inner changed through the caller's slice: %d", got)
	}
}

func TestFlatten(t *testing.T) {
	_, a, b := testVars()
	tbl := NewTable()
	sa := tbl.Intern(Value{Kind: Sentinel, Var: a})
	sb := tbl.Intern(Value{Kind: Sentinel, Var: b})
	un := tbl.Intern(Value{Kind: Unresolved, Unresolved: Memory, Detail: "field"})
	inner := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapErrorf, Format: "a: %w", Inner: []ValueID{sa}})
	trunc := tbl.Intern(Value{Kind: Truncated, Inner: []ValueID{un}})
	join := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapJoin, Inner: []ValueID{inner, sb, trunc}})
	got := Flatten(tbl, join)
	want := []Entry{
		{Leaf: sa, Chain: []ValueID{inner, join}},
		{Leaf: sb, Chain: []ValueID{join}},
		{Leaf: un, Chain: []ValueID{join}, Truncated: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Flatten =\n%+v\nwant\n%+v", got, want)
	}
	if n := tbl.Leaves(join); n != len(got) {
		t.Errorf("Leaves = %d, want %d (one per Flatten entry)", n, len(got))
	}

	if got := Flatten(tbl, sa); len(got) != 1 || got[0].Leaf != sa || len(got[0].Chain) != 0 {
		t.Errorf("Flatten of a leaf = %+v", got)
	}
}

func TestDedup(t *testing.T) {
	_, a, b := testVars()
	tbl := NewTable()
	sa := tbl.Intern(Value{Kind: Sentinel, Var: a})
	sb := tbl.Intern(Value{Kind: Sentinel, Var: b})
	p1 := Path{{Site: "one", Func: "F"}}
	p2 := Path{{Site: "two", Func: "F"}}
	got := Dedup(tbl, []Entry{
		{Leaf: sa, Path: p1},
		{Leaf: sb, Path: p1},
		{Leaf: sa, Path: p2},
		{Leaf: sa, Path: p1}, // the same path again: not another path
	})
	want := []Entry{
		{Leaf: sa, Path: p1, MorePaths: 1},
		{Leaf: sb, Path: p1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Dedup =\n%+v\nwant\n%+v", got, want)
	}
}

func TestDedupComparesWrapsWithoutSiblings(t *testing.T) {
	_, a, b := testVars()
	tbl := NewTable()
	sa := tbl.Intern(Value{Kind: Sentinel, Var: a})
	sb := tbl.Intern(Value{Kind: Sentinel, Var: b})
	// errors.Join(x, ErrA) at one place, with x being ErrA or ErrB: two
	// Wraps that differ only in their other operand.
	w1 := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapJoin, Pos: 7, Inner: []ValueID{sa, sa}})
	w2 := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapJoin, Pos: 7, Inner: []ValueID{sb, sa}})
	other := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapJoin, Pos: 8, Inner: []ValueID{sa}})
	p := Path{{Site: "one", Func: "F"}}
	var entries []Entry
	for _, w := range []ValueID{w1, w2, other} {
		for _, e := range Flatten(tbl, w) {
			e.Path = p
			entries = append(entries, e)
		}
	}
	got := Dedup(tbl, entries)
	want := []Entry{
		{Leaf: sa, Chain: []ValueID{w1}, Path: p},
		{Leaf: sb, Chain: []ValueID{w2}, Path: p},
		{Leaf: sa, Chain: []ValueID{other}, Path: p},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Dedup =\n%+v\nwant\n%+v", got, want)
	}
}

func TestSortIsStableAndRanked(t *testing.T) {
	pkg, a, b := testVars()
	named := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "MyErr", nil), types.NewStruct(nil, nil), nil)
	tbl := NewTable()
	ids := map[string]ValueID{
		"un":  tbl.Intern(Value{Kind: Unresolved, Unresolved: Memory, Detail: "x", Pos: 1}),
		"op":  tbl.Intern(Value{Kind: Opaque, Constructor: "errors.New", Message: "m", HasMessage: true, Pos: 2}),
		"c9":  tbl.Intern(Value{Kind: Concrete, Type: named, Pos: 9}),
		"c10": tbl.Intern(Value{Kind: Concrete, Type: types.NewPointer(named), Pos: 10}),
		"sb":  tbl.Intern(Value{Kind: Sentinel, Var: b, Pos: 3}),
		"sa":  tbl.Intern(Value{Kind: Sentinel, Var: a, Pos: 4}),
	}
	pos := func(id ValueID) (string, int) {
		v := tbl.Get(id)
		return "a.go", int(v.Pos)
	}
	// Two entries with the same leaf differ by chain.
	w := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapErrorf, Format: "x: %w", Inner: []ValueID{ids["sa"]}, Pos: 5})
	order := func(entries []Entry) string {
		var parts []string
		for _, e := range entries {
			s := tbl.String(e.Leaf)
			if len(e.Chain) > 0 {
				s += " via " + tbl.WrapDescription(e.Chain[0])
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, " | ")
	}
	want := `sentinel example.com/fx.ErrB | sentinel example.com/fx.ErrA | sentinel example.com/fx.ErrA via fmt.Errorf "x: %w" | ` +
		`concrete example.com/fx.MyErr | concrete *example.com/fx.MyErr | opaque errors.New "m" | unresolved(memory): x`
	inputs := [][]Entry{
		{{Leaf: ids["un"]}, {Leaf: ids["op"]}, {Leaf: ids["c10"]}, {Leaf: ids["c9"]}, {Leaf: ids["sa"], Chain: []ValueID{w}}, {Leaf: ids["sa"]}, {Leaf: ids["sb"]}},
		{{Leaf: ids["sb"]}, {Leaf: ids["sa"]}, {Leaf: ids["sa"], Chain: []ValueID{w}}, {Leaf: ids["c9"]}, {Leaf: ids["c10"]}, {Leaf: ids["op"]}, {Leaf: ids["un"]}},
	}
	for i, in := range inputs {
		for run := range 2 {
			entries := append([]Entry(nil), in...)
			Sort(tbl, entries, pos)
			if got := order(entries); got != want {
				t.Errorf("input %d, run %d: order =\n%s\nwant\n%s", i, run, got, want)
			}
		}
	}
	if n := CountUnresolved(tbl, inputs[0]); n != 1 {
		t.Errorf("CountUnresolved = %d, want 1", n)
	}
}

func TestCap(t *testing.T) {
	_, a, b := testVars()
	tbl := NewTable()
	sa := tbl.Intern(Value{Kind: Sentinel, Var: a})
	sb := tbl.Intern(Value{Kind: Sentinel, Var: b})
	wrap := func(format string, inner ...ValueID) ValueID {
		return tbl.Intern(Value{Kind: Wrap, WrapKind: WrapErrorf, Format: format, Inner: inner})
	}
	strs := func(ids []ValueID) string {
		var out []string
		for _, id := range ids {
			out = append(out, tbl.String(id))
		}
		return strings.Join(out, " | ")
	}
	w1 := wrap("1: %w", sa)
	w2 := wrap("2: %w %w", w1, sb)
	w3 := wrap("3: %w", w2)

	tests := []struct {
		name  string
		id    ValueID
		limit int
		want  string
	}{
		{"leaf", sa, 1, "sentinel example.com/fx.ErrA"},
		{"within limit", w3, 3, tbl.String(w3)},
		{"cut innermost", w3, 2, `wrap(errorf fmt.Errorf "3: %w")[wrap(errorf fmt.Errorf "2: %w %w")[truncated[sentinel example.com/fx.ErrA]; sentinel example.com/fx.ErrB]]`},
		{"cut below the top", w3, 1, `wrap(errorf fmt.Errorf "3: %w")[truncated[sentinel example.com/fx.ErrA]; truncated[sentinel example.com/fx.ErrB]]`},
		{"cut the top", w2, 0, "truncated[sentinel example.com/fx.ErrA] | truncated[sentinel example.com/fx.ErrB]"},
	}
	for _, tc := range tests {
		if got := strs(tbl.Cap(tc.id, tc.limit)); got != tc.want {
			t.Errorf("%s: Cap = %s, want %s", tc.name, got, tc.want)
		}
	}

	// A Truncated marker over a Wrap is pushed down to the leaves, and
	// capping is idempotent, which keeps fixpoints over capped trees finite.
	tw := tbl.Intern(Value{Kind: Truncated, Inner: []ValueID{w1}})
	if got, want := strs(tbl.Cap(tw, 5)), "truncated[sentinel example.com/fx.ErrA]"; got != want {
		t.Errorf("Cap(truncated wrap) = %s, want %s", got, want)
	}
	once := tbl.Cap(w3, 1)
	if twice := tbl.Cap(once[0], 1); !reflect.DeepEqual(once, twice) {
		t.Errorf("Cap is not idempotent: %v then %v", once, twice)
	}
}

// TestCapLeavesFollowsLeaves checks that CapLeaves says which leaf of the
// original tree each leaf of a capped value is, also when capping merges
// equal leaves.
func TestCapLeavesFollowsLeaves(t *testing.T) {
	_, a, b := testVars()
	tbl := NewTable()
	sa := tbl.Intern(Value{Kind: Sentinel, Var: a})
	sb := tbl.Intern(Value{Kind: Sentinel, Var: b})
	inner := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapErrorf, Format: "i %w %w", Inner: []ValueID{sa, sa}})
	// Leaves of top in order: sb (0), sa (1), sa (2).
	top := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapJoin, Inner: []ValueID{sb, inner}})
	if n := tbl.Leaves(top); n != 3 {
		t.Fatalf("Leaves = %d, want 3", n)
	}
	got := tbl.CapLeaves(top, 1)
	if len(got) != 1 || !reflect.DeepEqual(got[0].From, [][]int{{0}, {1, 2}}) || tbl.Leaves(got[0].Value) != 2 {
		t.Errorf("CapLeaves(top, 1) = %+v, want one value whose leaves are sb (0) and a single truncated sa (1 and 2)", got)
	}
	got = tbl.CapLeaves(top, 0)
	if len(got) != 2 || !reflect.DeepEqual(got[0].From, [][]int{{0}}) || !reflect.DeepEqual(got[1].From, [][]int{{1, 2}}) {
		t.Errorf("CapLeaves(top, 0) = %+v, want truncated sb from 0 and truncated sa from 1 and 2", got)
	}
	got = tbl.CapLeaves(top, 5)
	if len(got) != 1 || got[0].Value != top || !reflect.DeepEqual(got[0].From, [][]int{{0}, {1}, {2}}) {
		t.Errorf("CapLeaves(top, 5) = %+v, want top unchanged", got)
	}
}
