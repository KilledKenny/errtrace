package flow

import (
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
)

// BindingFacts returns the local facts, in the function that creates the
// closure fn (its parent), of what fn's captured variable index can hold
// when read as a value of type typ: for every MakeClosure of fn, a binding
// of type typ (captured by value) is walked as a value, and a binding that
// is the address of a local variable (captured by reference) gives the
// values stored to it (see StoredFacts). It reports false if fn is not a
// closure with a parent, has no MakeClosure there, or a binding is neither.
func BindingFacts(tbl *errset.Table, fn *ssa.Function, index int, typ types.Type, opts Options) (creator *ssa.Function, facts []Fact, ok bool) {
	creator = fn.Parent()
	if creator == nil || typ == nil {
		return nil, nil, false
	}
	found := false
	for _, b := range creator.Blocks {
		for _, instr := range b.Instrs {
			mc, isMC := instr.(*ssa.MakeClosure)
			if !isMC || mc.Fn != fn || index >= len(mc.Bindings) {
				continue
			}
			found = true
			switch bv := mc.Bindings[index]; {
			case types.Identical(bv.Type(), typ):
				facts = append(facts, ValueFacts(tbl, creator, bv, mc, opts)...)
			case isAlloc(bv):
				facts = append(facts, StoredFacts(tbl, creator, bv.(*ssa.Alloc), opts)...)
			default:
				return nil, nil, false
			}
		}
	}
	if !found {
		return nil, nil, false
	}
	return creator, facts, true
}

func isAlloc(v ssa.Value) bool {
	_, ok := v.(*ssa.Alloc)
	return ok
}

// StoredFacts returns the local facts of the values that fn stores to the
// local variable alloc: the union over its stores, in instruction order.
// Stores made through a closure's captured reference are not included.
func StoredFacts(tbl *errset.Table, fn *ssa.Function, alloc *ssa.Alloc, opts Options) []Fact {
	var facts []Fact
	refs := alloc.Referrers()
	if refs == nil {
		return nil
	}
	for _, r := range *refs {
		if st, ok := r.(*ssa.Store); ok && st.Addr == alloc && st.Parent() == fn {
			facts = append(facts, ValueFacts(tbl, fn, st.Val, st, opts)...)
		}
	}
	return facts
}
