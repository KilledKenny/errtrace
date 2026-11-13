package flow

import (
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/errset"
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

// StoredFacts returns the local facts of the values stored to the local
// variable alloc of fn: the union over fn's own stores, in instruction
// order, and a StoreRef for every store made by a closure that captured the
// variable. A load of alloc inside a stored value (err = wrap(err)) stands
// for the same stores. If the variable can be written in a way errtrace
// does not follow to a store (its address is passed to a call or stored
// elsewhere), an Unresolved value of kind memory is added, so the facts
// never claim more completeness than they have.
func StoredFacts(tbl *errset.Table, fn *ssa.Function, alloc *ssa.Alloc, opts Options) []Fact {
	w := variableWriters(alloc)
	a := &analyzer{tbl: tbl, fn: fn, at: alloc, pt: pointOf(alloc), opts: opts, seen: make(map[seenKey]bool)}
	a.captured = alloc
	a.active = make(map[*ssa.Store]bool)
	for _, st := range w.stores {
		if st.Parent() != fn {
			a.at, a.pt = alloc, pointOf(alloc)
			a.storeRef(st, alloc, alloc)
			continue
		}
		a.at, a.pt = st, pointOf(st)
		a.seen = make(map[seenKey]bool)
		a.active[st] = true
		a.walk(st.Val)
		delete(a.active, st)
	}
	if w.escapes {
		a.at, a.pt = alloc, pointOf(alloc)
		a.memory(alloc, escapedVariable(alloc))
	}
	return a.facts
}
