package resolve

import (
	"sort"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// Roots returns the Query roots that Candidates are resolved from: the
// main and init functions of the main packages among initial (the packages
// the command line matched), plus the init function of every package of
// prog. They are sorted by name.
func Roots(prog *ssa.Program, initial []*ssa.Package) []*ssa.Function {
	seen := make(map[*ssa.Function]bool)
	var out []*ssa.Function
	add := func(fn *ssa.Function) {
		if fn != nil && !seen[fn] {
			seen[fn] = true
			out = append(out, fn)
		}
	}
	var pkgs []*ssa.Package
	for _, p := range initial {
		if p != nil {
			pkgs = append(pkgs, p)
		}
	}
	for _, p := range ssautil.MainPackages(pkgs) {
		add(p.Func("main"))
		add(p.Func("init"))
	}
	for _, p := range prog.AllPackages() {
		add(p.Func("init"))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
