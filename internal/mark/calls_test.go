package mark_test

import (
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/fixture"
	"github.com/KilledKenny/errtrace/internal/mark"
	"github.com/KilledKenny/errtrace/internal/resolve"
)

// upper returns the upper-bound call edges of the fixture, as the CLI
// builds them. With extra, the named functions are added as roots, as an
// unreachable Target is: the program is then not whole, and every dynamic
// call has its CHA callees.
func upper(t *testing.T, extra ...string) func(l *fixture.Loaded) mark.Callees {
	return func(l *fixture.Loaded) mark.Callees {
		var initial []*ssa.Package
		for _, p := range l.Initial {
			if sp := l.SSAPackage(p); sp != nil {
				initial = append(initial, sp)
			}
		}
		v, err := resolve.NewVTA(t.Context(), l.SSA, resolve.Roots(l.SSA, initial))
		if err != nil {
			t.Fatal(err)
		}
		if len(extra) > 0 {
			var fns []*ssa.Function
			for _, name := range extra {
				fns = append(fns, initial[0].Func(name))
			}
			if v, err = v.WithRoots(t.Context(), fns); err != nil {
				t.Fatal(err)
			}
		}
		return v.Upper()
	}
}

func TestIntraWithCalls(t *testing.T) {
	t.Parallel()
	runMarkTests(t, "intra.txtar", upper(t), []markTest{
		{"is ErrX", map[string]string{
			"Direct": "0", "Other": "-", "New": "-", "WrapV": "-", "JoinOther": "-",
			"FieldOther": "-", "UnsetLoad": "-", "MapOther": "-", "Local": "-",
			"MakePlain": "-", "AsOther": "-", "AsPlain": "-", "AssertPlain": "-", "ClosureOther": "-",
		}},
	})
}

func TestCalls(t *testing.T) {
	t.Parallel()
	runMarkTests(t, "calls.txtar", upper(t), []markTest{
		{"is ErrX", map[string]string{
			// The shared helper: only the caller that passes X is marked.
			"CallerX": "0", "CallerY": "-", "Chain": "0", "ChainY": "-",
			"RecX": "0", "RecY": "-", "MutualX": "0", "MutualY": "-",
			"Free": "0", "RecFree": "0",
			// In a whole program the Candidates of VTA are used.
			"Iface": "0", "IfaceY": "-", "Unreached": "0",
			"Callback": "0", "CallbackY": "-",
			"Method": "0", "Go": "0", "Defer": "0", "DeferClosure": "0",
			"DynErrorf": "0", "Multi": "1", "SecondY": "-", "SecondX": "0", "bound": "0",
			"wrap": "0", "id": "0", "second": "0",
		}},
		{"is ErrY", map[string]string{
			"CallerX": "-", "CallerY": "0", "Chain": "-", "ChainY": "0",
			"RecX": "-", "RecY": "0", "MutualX": "-", "MutualY": "0",
			"Free": "-", "RecFree": "-",
			"Iface": "-", "IfaceY": "0", "Unreached": "0",
			"Callback": "-", "CallbackY": "0",
			"Method": "-", "Go": "-", "DeferClosure": "-",
			"DynErrorf": "-", "Multi": "-", "SecondY": "0", "SecondX": "-", "bound": "-",
			"second": "0",
		}},
	})
}

// TestCallsCHA runs the pass over a program that is not whole: a dynamic
// call then reaches every function that matches it.
func TestCallsCHA(t *testing.T) {
	t.Parallel()
	runMarkTests(t, "calls.txtar", upper(t, "Unreached"), []markTest{
		{"is ErrX", map[string]string{
			"CallerX": "0", "CallerY": "-", "RecY": "-", "MutualY": "-",
			"Iface": "0", "IfaceY": "0", "Unreached": "0", "CallbackY": "0",
		}},
	})
}
