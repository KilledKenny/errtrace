package summary

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
	"github.com/killedKenny/errtrace/internal/fixture"
	"github.com/killedKenny/errtrace/internal/flow"
	"github.com/killedKenny/errtrace/internal/resolve"
)

func loadSolverFixture(t *testing.T) *ssa.Package {
	t.Helper()
	l := fixture.LoadProgram(t, filepath.Join("testdata", "solver.txtar"))
	return l.SSAPackage(l.Initial[0])
}

func factStrings(tbl *errset.Table, facts []Fact) string {
	var parts []string
	for _, f := range facts {
		parts = append(parts, tbl.String(f.Value))
	}
	return strings.Join(parts, "; ")
}

func TestSubstitutionMapsParam(t *testing.T) {
	t.Parallel()
	pkg := loadSolverFixture(t)
	tbl := errset.NewTable()
	s := NewSolver(tbl, resolve.Static{}, flow.Options{})
	a, id := pkg.Func("A"), pkg.Func("id")
	if err := s.Solve(t.Context(), []*ssa.Function{a}); err != nil {
		t.Fatal(err)
	}
	if got := factStrings(tbl, s.Summary(id).Results[0].Facts); got != "param example.com/fx.id#0 e" {
		t.Errorf("summary of id = %q, want its parameter", got)
	}
	facts := s.Summary(a).Results[0].Facts
	if got := factStrings(tbl, facts); got != "sentinel example.com/fx.ErrX" {
		t.Fatalf("summary of A = %q, want Param(0) of id replaced by the argument ErrX", got)
	}
	src := facts[0].Leaves[0][0]
	if src.Kind != Via || src.Callee != id || tbl.Get(src.CalleeValue).Kind != errset.Param || len(src.ArgSources) != 1 || src.ArgSources[0].Kind != Local {
		t.Errorf("source of ErrX in A = %+v, want Via id from its Param with a local argument source", src)
	}
	path, ok := s.Path(a, facts[0].Leaves[0])
	if !ok || len(path) != 3 || path[0].Func != "example.com/fx.A" || path[1].Func != "example.com/fx.id" || path[2].Callee != "example.com/fx.id" {
		t.Errorf("path of ErrX = %+v (%v), want the argument in A, the return in id, the call in A", path, ok)
	}
	if st := s.Stats(); st.Functions != 2 || st.SCCs != 2 {
		t.Errorf("Stats = %+v, want 2 functions in 2 SCCs", st)
	}
}

func TestSelfRecursionConverges(t *testing.T) {
	t.Parallel()
	pkg := loadSolverFixture(t)
	tbl := errset.NewTable()
	s := NewSolver(tbl, resolve.Static{}, flow.Options{})
	if err := s.Solve(t.Context(), []*ssa.Function{pkg.Func("Caller")}); err != nil {
		t.Fatal(err)
	}
	if got := factStrings(tbl, s.Summary(pkg.Func("Caller")).Results[0].Facts); got != "" {
		t.Errorf("summary of Caller = %q, want no values", got)
	}
}

func TestFixpointLimit(t *testing.T) {
	t.Parallel()
	pkg := loadSolverFixture(t)
	tbl := errset.NewTable()
	s := NewSolver(tbl, resolve.Static{}, flow.Options{})
	loop := pkg.Func("Loop")
	// A transfer function that never converges: every round adds a new
	// value.
	round := 0
	s.transfer = func(fn *ssa.Function) []ResultSummary {
		round++
		var facts []Fact
		for i := 1; i <= round; i++ {
			id := tbl.Intern(errset.Value{Kind: errset.Opaque, Constructor: "errors.New", Message: strings.Repeat("x", i), HasMessage: true})
			facts = append(facts, Fact{Value: id, Leaves: [][]Source{{{Kind: Local, Site: fn}}}})
		}
		return []ResultSummary{{Index: 0, Facts: facts}}
	}
	if err := s.Solve(t.Context(), []*ssa.Function{loop}); err != nil {
		t.Fatal(err)
	}
	if round != maxRounds {
		t.Errorf("transfer ran %d times, want %d", round, maxRounds)
	}
	facts := s.Summary(loop).Results[0].Facts
	last := tbl.Get(facts[len(facts)-1].Value)
	if last.Kind != errset.Unresolved || last.Unresolved != errset.FixpointLimit {
		t.Errorf("last fact = %s, want unresolved(fixpoint-limit)", tbl.String(facts[len(facts)-1].Value))
	}
}

func TestSizeCap(t *testing.T) {
	t.Parallel()
	pkg := loadSolverFixture(t)
	tbl := errset.NewTable()
	s := NewSolver(tbl, resolve.Static{}, flow.Options{})
	s.transfer = func(fn *ssa.Function) []ResultSummary {
		var facts []Fact
		for i := range maxValues + 5 {
			id := tbl.Intern(errset.Value{Kind: errset.Opaque, Pos: 1, Message: strings.Repeat("y", i+1), HasMessage: true})
			facts = append(facts, Fact{Value: id, Leaves: [][]Source{{{Kind: Local, Site: fn}}}})
		}
		return []ResultSummary{{Index: 0, Facts: facts}}
	}
	if err := s.Solve(t.Context(), []*ssa.Function{pkg.Func("id")}); err != nil {
		t.Fatal(err)
	}
	facts := s.Summary(pkg.Func("id")).Results[0].Facts
	if len(facts) != maxValues+1 || tbl.Get(facts[maxValues].Value).Unresolved != errset.FixpointLimit {
		t.Errorf("%d facts, last %s; want %d values and unresolved(fixpoint-limit)", len(facts), tbl.String(facts[len(facts)-1].Value), maxValues)
	}
}

func TestAddSourceRanksAndCaps(t *testing.T) {
	var srcs []Source
	srcs = addSource(srcs, Source{Kind: Via, Site: "d1", Dynamic: true})
	srcs = addSource(srcs, Source{Kind: Via, Site: "s1"})
	srcs = addSource(srcs, Source{Kind: Local, Site: "l1"})
	srcs = addSource(srcs, Source{Kind: Via, Site: "s1"}) // a duplicate
	srcs = addSource(srcs, Source{Kind: Via, Site: "d2", Dynamic: true})
	srcs = addSource(srcs, Source{Kind: Local, Site: "l2"})
	srcs = addSource(srcs, Source{Kind: Via, Site: "d3", Dynamic: true})
	var got []string
	for _, s := range srcs {
		got = append(got, s.Site.(string))
	}
	if strings.Join(got, ",") != "l1,l2,s1,d1" {
		t.Errorf("sources = %v, want l1,l2,s1,d1", got)
	}
}
