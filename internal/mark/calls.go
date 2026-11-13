package mark

import "golang.org/x/tools/go/ssa"

// The calls between functions are not followed yet: a Mark stays inside
// the function (and the memory) it was made in.

func (a *analysis) indexCallees(ssa.CallInstruction) {}

func (a *analysis) linkArgs(key, tags, ssa.CallInstruction) {}

func (a *analysis) retChanged(*ssa.Function, int, tags) {}

func (a *analysis) siteResultFromCallees(ssa.CallInstruction, int) bool { return true }
