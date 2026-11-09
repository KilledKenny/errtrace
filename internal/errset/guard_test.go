package errset

import (
	"reflect"
	"testing"
)

func TestGuardedIdentityAndLeaves(t *testing.T) {
	_, a, _ := testVars()
	tbl := NewTable()
	sa := tbl.Intern(Value{Kind: Sentinel, Var: a})
	w := tbl.Intern(Value{Kind: Wrap, WrapKind: WrapJoin, Inner: []ValueID{sa, sa}})
	g := Guard{Kind: GuardEqual, Var: a, Pos: 7}
	g1 := tbl.Intern(Value{Kind: Guarded, Inner: []ValueID{w}, Guards: []Guard{g}})
	held := g
	held.Holds = true
	g2 := tbl.Intern(Value{Kind: Guarded, Inner: []ValueID{w}, Guards: []Guard{held}})
	if g1 == g2 {
		t.Error("guards with different outcomes intern to one value")
	}
	if again := tbl.Intern(Value{Kind: Guarded, Site: "other", Inner: []ValueID{w}, Guards: []Guard{{Kind: GuardEqual, Var: a, Pos: 7, Site: "x"}}}); again != g1 {
		t.Error("equal guards with other sites intern to different values")
	}
	if n := tbl.Leaves(g1); n != 2 {
		t.Errorf("Leaves(guarded join) = %d, want 2", n)
	}
	entries := Flatten(tbl, g1)
	if len(entries) != 2 || entries[0].Leaf != sa || !reflect.DeepEqual(entries[0].Chain, []ValueID{w}) {
		t.Errorf("Flatten(guarded join) = %+v, want two entries of ErrA under the join", entries)
	}
	capped := tbl.Cap(g1, 0)
	if len(capped) != 1 || tbl.Get(capped[0]).Kind != Guarded || tbl.Get(tbl.Get(capped[0]).Inner[0]).Kind != Truncated {
		t.Errorf("Cap(guarded join, 0) = %v, want the guards over the Truncated leaf", capped)
	}
}

func TestDedupIntersectsNotes(t *testing.T) {
	_, a, _ := testVars()
	tbl := NewTable()
	sa := tbl.Intern(Value{Kind: Sentinel, Var: a})
	n1 := GuardNote{Pos: 1, Reason: "one"}
	n2 := GuardNote{Pos: 2, Reason: "two"}
	got := Dedup(tbl, []Entry{
		{Leaf: sa, PossiblyHandled: []GuardNote{n1, n2}},
		{Leaf: sa, PossiblyHandled: []GuardNote{n2}},
	})
	if len(got) != 1 || !reflect.DeepEqual(got[0].PossiblyHandled, []GuardNote{n2}) {
		t.Errorf("Dedup notes = %+v, want only the shared note", got)
	}
	got = Dedup(tbl, []Entry{
		{Leaf: sa, PossiblyHandled: []GuardNote{n1}},
		{Leaf: sa},
	})
	if len(got[0].PossiblyHandled) != 0 || CountPossiblyHandled(got) != 0 {
		t.Errorf("an entry that also arrives unhandled is possibly handled: %+v", got)
	}
}
