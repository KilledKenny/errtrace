package query_test

import (
	"go/types"
	"os"
	"sort"
	"strconv"
	"testing"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/KilledKenny/errtrace/internal/errset"
	"github.com/KilledKenny/errtrace/internal/flow"
	"github.com/KilledKenny/errtrace/internal/load"
	"github.com/KilledKenny/errtrace/internal/mark"
	"github.com/KilledKenny/errtrace/internal/query"
	"github.com/KilledKenny/errtrace/internal/resolve"
	"github.com/KilledKenny/errtrace/internal/target"
)

// TestUpperBoundCoversLowerBound checks the upward pass against the
// downward one on errtrace's own code: every Sentinel and Concrete Type
// that the Error Set of a function holds (each has a Propagation Path as
// witness) must be marked on that function's results by the upward pass
// for that Subject. A miss is a hole in the upper bound: a wrong "no".
func TestUpperBoundCoversLowerBound(t *testing.T) {
	if testing.Short() {
		t.Skip("loads the whole module")
	}
	prog, err := load.Load(t.Context(), load.Config{Dir: "../..", Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	var initial []*ssa.Package
	for _, p := range prog.Initial {
		if sp := prog.SSAPackage(p); sp != nil {
			initial = append(initial, sp)
		}
	}
	vta, err := resolve.NewVTA(t.Context(), prog.SSA, resolve.Roots(prog.SSA, initial))
	if err != nil {
		t.Fatal(err)
	}
	// The Targets: the functions of the module that main reaches (so that
	// one Resolver and one whole-program upward pass serve them all) and
	// that have an error result.
	var targets []*ssa.Function
	for _, pkg := range initial {
		for _, m := range pkg.Members {
			fn, ok := m.(*ssa.Function)
			if !ok || fn.Blocks == nil || fn.TypeParams().Len() > 0 || !vta.Reachable(fn) || len(flow.ErrorResults(fn.Signature)) == 0 {
				continue
			}
			targets = append(targets, fn)
		}
	}
	// With ERRTRACE_BOUND_STRIDE=n, also every n-th function that main
	// reaches in the whole program (the standard library included): a
	// wider, slower check.
	if n, _ := strconv.Atoi(os.Getenv("ERRTRACE_BOUND_STRIDE")); n > 0 {
		var all []*ssa.Function
		for fn := range ssautil.AllFunctions(prog.SSA) {
			if fn.Blocks != nil && fn.TypeParams().Len() == 0 && fn.Synthetic == "" && vta.Reachable(fn) && len(flow.ErrorResults(fn.Signature)) > 0 {
				all = append(all, fn)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].String() < all[j].String() })
		for i := 0; i < len(all); i += n {
			targets = append(targets, all[i])
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].String() < targets[j].String() })
	if len(targets) < 10 {
		t.Fatalf("only %d Targets", len(targets))
	}

	globals := make(map[*types.Var]*ssa.Global)
	for _, pkg := range prog.SSA.AllPackages() {
		for _, m := range pkg.Members {
			if g, ok := m.(*ssa.Global); ok {
				if v, ok := g.Object().(*types.Var); ok {
					globals[v] = g
				}
			}
		}
	}
	type subjectKey struct {
		v *types.Var
		t string
	}
	upper := vta.Upper()
	marks := make(map[subjectKey]*mark.Result)
	run := func(k subjectKey, subj mark.Subject) *mark.Result {
		if r, ok := marks[k]; ok {
			return r
		}
		r, err := mark.Run(t.Context(), prog.SSA, upper, subj)
		if err != nil {
			t.Fatal(err)
		}
		marks[k] = r
		return r
	}
	checked := 0
	for _, fn := range targets {
		tgt := &target.Target{Kind: target.Declaration, Name: fn.String(), Funcs: []*ssa.Function{fn}}
		res, err := query.Run(t.Context(), prog, tgt, query.Options{Resolver: vta})
		if err != nil {
			t.Fatal(err)
		}
		seen := make(map[subjectKey]bool)
		for _, e := range res.Entries {
			v := res.Table.Get(e.Leaf)
			var k subjectKey
			var subj mark.Subject
			switch v.Kind {
			case errset.Sentinel:
				g := globals[v.Var]
				if g == nil {
					continue
				}
				k, subj = subjectKey{v: v.Var}, mark.Subject{Sentinel: g}
			case errset.Concrete:
				k, subj = subjectKey{t: types.TypeString(v.Type, nil)}, mark.Subject{Type: v.Type}
			default:
				continue
			}
			if seen[k] {
				continue
			}
			seen[k] = true
			checked++
			r := run(k, subj)
			marked := false
			for _, idx := range flow.ErrorResults(fn.Signature) {
				marked = marked || r.ResultMarked(fn, idx)
			}
			if !marked {
				t.Errorf("%s: the Error Set holds %s (%s), but the upward pass does not mark the result", fn, subj, res.Table.String(e.Leaf))
			}
		}
	}
	t.Logf("%d Targets, %d Subjects, %d (Target, Subject) pairs checked", len(targets), len(marks), checked)
}
