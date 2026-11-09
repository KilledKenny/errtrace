package flow

import (
	"go/token"
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
// local variable alloc: the union over its stores, in instruction order. A
// load of alloc inside a stored value (err = wrap(err)) stands for the same
// stores. If the variable can be written other than by a Store in fn (its
// address is passed to a call or stored elsewhere, or a closure writes it
// through its captured reference), an Unresolved value of kind memory is
// added, so the facts never claim more completeness than they have.
func StoredFacts(tbl *errset.Table, fn *ssa.Function, alloc *ssa.Alloc, opts Options) []Fact {
	refs := alloc.Referrers()
	if refs == nil {
		return nil
	}
	escapes := writtenElsewhere(alloc, true, make(map[ssa.Value]bool))
	a := &analyzer{tbl: tbl, fn: fn, at: alloc, opts: opts, seen: make(map[ssa.Value]bool)}
	if !escapes {
		a.captured = alloc
		a.active = make(map[*ssa.Store]bool)
	}
	for _, r := range *refs {
		if st, ok := r.(*ssa.Store); ok && st.Addr == alloc && st.Parent() == fn {
			a.at = st
			a.seen = make(map[ssa.Value]bool)
			if a.active != nil {
				a.active[st] = true
			}
			a.walk(st.Val)
			delete(a.active, st)
		}
	}
	if escapes {
		a.at = alloc
		a.memory(alloc, "load of escaping variable "+alloc.Comment)
	}
	return a.facts
}

// writtenElsewhere reports whether the variable that addr points to (a
// local Alloc, or a closure's FreeVar bound to it) may be written by
// anything but a Store in the creator: a Store through a closure's
// reference, or any use of the address other than a load, a debug
// reference, or a binding into a closure that only reads it. creator is
// set when addr is the Alloc itself, whose direct Stores are followed.
func writtenElsewhere(addr ssa.Value, creator bool, seen map[ssa.Value]bool) bool {
	if seen[addr] {
		return false
	}
	seen[addr] = true
	refs := addr.Referrers()
	if refs == nil {
		return false
	}
	for _, r := range *refs {
		switch r := r.(type) {
		case *ssa.Store:
			if r.Addr != addr || !creator {
				return true
			}
		case *ssa.UnOp:
			if r.Op != token.MUL {
				return true
			}
		case *ssa.DebugRef:
		case *ssa.MakeClosure:
			fn, ok := r.Fn.(*ssa.Function)
			if !ok {
				return true
			}
			for i, b := range r.Bindings {
				if b != addr {
					continue
				}
				if i >= len(fn.FreeVars) || writtenElsewhere(fn.FreeVars[i], false, seen) {
					return true
				}
			}
		default:
			return true
		}
	}
	return false
}
