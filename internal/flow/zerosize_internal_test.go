package flow

import (
	"go/types"
	"testing"
)

// TestMayBeZeroSize checks which types may have size zero, so that
// pointers to them are not taken as distinct.
func TestMayBeZeroSize(t *testing.T) {
	intT := types.Typ[types.Int]
	empty := types.NewStruct(nil, nil)
	field := func(name string, typ types.Type) *types.Var { return types.NewField(0, nil, name, typ, false) }
	tp := types.NewTypeParam(types.NewTypeName(0, nil, "T", nil), types.NewInterfaceType(nil, nil))
	for _, tc := range []struct {
		name string
		typ  types.Type
		want bool
	}{
		{"empty struct", empty, true},
		{"struct of empty", types.NewStruct([]*types.Var{field("a", empty), field("b", types.NewArray(intT, 0))}, nil), true},
		{"struct with int", types.NewStruct([]*types.Var{field("a", empty), field("b", intT)}, nil), false},
		{"zero-length array", types.NewArray(intT, 0), true},
		{"array of empty", types.NewArray(empty, 3), true},
		{"array of int", types.NewArray(intT, 1), false},
		{"named empty", types.NewNamed(types.NewTypeName(0, nil, "Z", nil), empty, nil), true},
		{"type parameter", tp, true},
		{"int", intT, false},
		{"string", types.Typ[types.String], false},
	} {
		if got := mayBeZeroSize(tc.typ); got != tc.want {
			t.Errorf("%s: mayBeZeroSize(%s) = %v, want %v", tc.name, tc.typ, got, tc.want)
		}
	}
}
