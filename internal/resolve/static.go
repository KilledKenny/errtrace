package resolve

import "golang.org/x/tools/go/ssa"

// Static is the Resolver that knows only static calls: the Candidate of a
// call is its static callee (including an immediately applied closure), and
// a dynamic call has no Candidates.
type Static struct{}

var _ Resolver = Static{}

// Callees returns the static callee of site, or nothing for a dynamic call.
func (Static) Callees(site ssa.CallInstruction) []*ssa.Function {
	if callee := site.Common().StaticCallee(); callee != nil {
		return []*ssa.Function{callee}
	}
	return nil
}

// IsDynamic reports whether site has no static callee.
func (Static) IsDynamic(site ssa.CallInstruction) bool {
	return site.Common().StaticCallee() == nil
}

// Reachable reports true for every function: Static has no call graph.
func (Static) Reachable(*ssa.Function) bool { return true }
