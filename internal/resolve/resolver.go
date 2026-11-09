// Package resolve finds the Candidates of call sites: the functions that may
// run where the code calls a function, statically or through an interface
// method or a function value. Resolver hides how they are found, so that
// the strategy can be swapped.
package resolve

import "golang.org/x/tools/go/ssa"

// Resolver gives the Candidates of call sites.
type Resolver interface {
	// Callees returns the Candidates of site, sorted by their SSA name: the
	// static callee of a static call, or the functions that may run at a
	// dynamic call. It returns nothing for a dynamic call whose Candidates
	// are unknown.
	Callees(site ssa.CallInstruction) []*ssa.Function
	// IsDynamic reports whether site calls through an interface method or
	// a function value, rather than a statically known function.
	IsDynamic(site ssa.CallInstruction) bool
	// Reachable reports whether fn may run in the analysed program.
	Reachable(fn *ssa.Function) bool
}
