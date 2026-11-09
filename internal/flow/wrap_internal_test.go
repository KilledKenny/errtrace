package flow

import (
	"testing"

	"github.com/killedKenny/errtrace/internal/errset"
)

// TestWrapsCombinationCap checks that a Wrap whose operand combinations
// pass maxCombos keeps maxCombos Wraps and adds unresolved(fixpoint-limit).
func TestWrapsCombinationCap(t *testing.T) {
	tbl := errset.NewTable()
	a := &analyzer{tbl: tbl}
	leaves := func(prefix string, n int) []errset.ValueID {
		var out []errset.ValueID
		for i := range n {
			out = append(out, tbl.Intern(errset.Value{Kind: errset.Opaque, Constructor: "errors.New", Message: prefix + string(rune('a'+i%26)) + string(rune('a'+i/26)), HasMessage: true}))
		}
		return out
	}
	base := errset.Value{WrapKind: errset.WrapJoin}

	ids := a.wraps(base, [][]errset.ValueID{leaves("x", 32), leaves("y", 32)})
	if len(ids) != maxCombos {
		t.Fatalf("32x32 operands: got %d values, want %d with no unresolved", len(ids), maxCombos)
	}
	for _, id := range ids {
		if tbl.Get(id).Kind != errset.Wrap {
			t.Fatalf("32x32 operands: unexpected %v", tbl.Get(id).Kind)
		}
	}

	ids = a.wraps(base, [][]errset.ValueID{leaves("x", 33), leaves("y", 32)})
	if len(ids) != maxCombos+1 {
		t.Fatalf("33x32 operands: got %d values, want %d", len(ids), maxCombos+1)
	}
	last := tbl.Get(ids[len(ids)-1])
	if last.Kind != errset.Unresolved || last.Unresolved != errset.FixpointLimit {
		t.Fatalf("33x32 operands: last value is %v, want unresolved(fixpoint-limit)", last.Kind)
	}
}
