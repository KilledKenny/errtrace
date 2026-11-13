package query_test

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/errset"
	"github.com/KilledKenny/errtrace/internal/fixture"
	"github.com/KilledKenny/errtrace/internal/mark"
	"github.com/KilledKenny/errtrace/internal/query"
	"github.com/KilledKenny/errtrace/internal/resolve"
	"github.com/KilledKenny/errtrace/internal/target"
)

func TestMembership(t *testing.T) {
	t.Parallel()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "membership.txtar"))
	pkg := l.SSAPackage(l.Initial[0])
	base, err := resolve.NewVTA(t.Context(), l.SSA, resolve.Roots(l.SSA, []*ssa.Package{pkg}))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		target, sentinel string
		verdict          query.Verdict
		matches          int
		// carriers are the details of the Carriers' leaves, in order.
		carriers            string
		reached, incomplete bool
	}{
		{"yes", "ErrX", query.Yes, 1, "", false, false},
		{"yes", "ErrY", query.No, 0, "", false, false},
		{"handled", "ErrU", query.Maybe, 1, "", false, false},
		// The key case: an Unresolved entry that cannot carry ErrX.
		{"hole", "ErrX", query.No, 0, "", false, false},
		// other matches the call and returns ErrY.
		{"hole", "ErrY", query.Maybe, 0, "dynamic call to function value example.com/fx.hook", true, false},
		{"stored", "ErrX", query.Maybe, 0, "load of field err of *example.com/fx.box", true, false},
		{"stored", "ErrY", query.No, 0, "", false, false},
		// Of three Unresolved entries only the field that holds ErrX is a
		// Carrier.
		{"two", "ErrX", query.Maybe, 0, "load of field err of *example.com/fx.box", true, false},
		{"two", "ErrY", query.Maybe, 0, "dynamic call to function value example.com/fx.hook", true, false},
		// The guard removes ErrX: reached, but nothing carries it.
		{"guarded", "ErrX", query.No, 0, "", true, false},
		{"guarded", "ErrY", query.Yes, 1, "", false, false},
		// Lib is not reachable from main: the same answer is not proven.
		{"lib", "ErrX", query.Maybe, 0, "", true, true},
	}
	for _, tt := range tests {
		tgt, err := target.Resolve(l.Program, l.Pos(t, "a.go:@"+tt.target))
		if err != nil {
			t.Fatal(err)
		}
		v := base
		for _, fn := range tgt.Funcs {
			if !v.Reachable(fn) {
				if v, err = base.WithRoots(t.Context(), tgt.Funcs); err != nil {
					t.Fatal(err)
				}
				break
			}
		}
		m, err := query.Membership(t.Context(), l.Program, tgt, query.MembershipOptions{
			Options:      query.Options{Resolver: v},
			Subject:      mark.Subject{Sentinel: pkg.Members[tt.sentinel].(*ssa.Global)},
			Upper:        v.Upper(),
			WholeProgram: v.WholeProgram(),
		})
		if err != nil {
			t.Fatal(err)
		}
		var carriers []string
		for _, c := range m.Carriers {
			leaf := m.Table.Get(c.Entry.Leaf)
			if leaf.Kind != errset.Unresolved {
				t.Errorf("%s %s: Carrier %v is not Unresolved", tt.target, tt.sentinel, leaf.Kind)
			}
			if len(c.Trace) == 0 {
				t.Errorf("%s %s: Carrier %q has no trace", tt.target, tt.sentinel, leaf.Detail)
			}
			carriers = append(carriers, leaf.Detail)
		}
		got := strings.Join(carriers, "; ")
		if m.Verdict != tt.verdict || len(m.Matches) != tt.matches || got != tt.carriers || m.Reached != tt.reached || m.Incomplete != tt.incomplete {
			t.Errorf("Membership(%s, %s) = %v, %d matches, carriers %q, reached %v, incomplete %v; want %v, %d, %q, %v, %v",
				tt.target, tt.sentinel, m.Verdict, len(m.Matches), got, m.Reached, m.Incomplete,
				tt.verdict, tt.matches, tt.carriers, tt.reached, tt.incomplete)
		}
	}
}
