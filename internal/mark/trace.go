package mark

import (
	"fmt"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// Step is one step of a trace: a place where the Mark of the Subject was
// made or moved.
type Step struct {
	// Site is where the step happens, for printing its span: an SSA
	// instruction or value, the variable (a types.Object) of a Sentinel,
	// or nil if the step has no place.
	Site any
	// Desc says what happens to the Mark there.
	Desc string
	// Wide reports a step that is not about one value: from here on the
	// Mark is on every value of a type, on a memory location that many
	// places share, or on whatever comes from outside. Traces that pass
	// the same wide step share what comes before it.
	Wide bool
}

// maxTrace bounds the length of a trace.
const maxTrace = 4096

// Trace returns how the Mark reached v: the steps from an origin of the
// Subject to v, following the first reason the pass found for each Mark.
// It returns nothing if v carries no Mark. The steps are the same on every
// run.
func (r *Result) Trace(v ssa.Value) []Step {
	a := r.a
	if v == nil {
		return nil
	}
	if tup, ok := v.Type().(*types.Tuple); ok {
		for i := range tup.Len() {
			if k := (key{v, int32(i)}); a.explicit(k) {
				return a.trace(ref{kind: refValue, k: k})
			}
		}
		return nil
	}
	if a.explicit(key{v, 0}) {
		return a.trace(ref{kind: refValue, k: key{v, 0}})
	}
	if a.implicitQuery(v.Type()) {
		return []Step{a.implicitStep(v)}
	}
	switch v.(type) {
	case *ssa.Alloc, *ssa.FreeVar, *ssa.Global, *ssa.FieldAddr, *ssa.IndexAddr:
		locs, n := a.addrLocs(v)
		for _, l := range locs[:n] {
			if a.locMarked(l) {
				return a.trace(ref{kind: refLoc, l: l})
			}
		}
	}
	return nil
}

// TraceResult returns how the Mark reached result index of fn (see
// Trace).
func (r *Result) TraceResult(fn *ssa.Function, index int) []Step {
	a := r.a
	if rs := a.rets[fn]; fn != nil && index >= 0 && index < len(rs) && rs[index].t != 0 {
		return a.trace(ref{kind: refRet, fn: fn, idx: int32(index)})
	}
	return nil
}

func (a *analysis) implicitStep(v ssa.Value) Step {
	return Step{Site: v, Desc: fmt.Sprintf("value of type %s, which is or may wrap %s", typeString(v.Type()), a.subj)}
}

func typeString(t types.Type) string { return types.TypeString(t, nil) }

// trace walks the reasons back from cur to a seed and returns the steps
// in order, the origin first.
func (a *analysis) trace(cur ref) []Step {
	var rev []Step
	for n := 0; cur.kind != refNone && n < maxTrace; n++ {
		var why reason
		switch cur.kind {
		case refValue:
			in := a.marks[cur.k]
			if in == nil || in.t == 0 {
				// A value marked by its type alone.
				rev = append(rev, a.implicitStep(cur.k.v))
				cur = ref{}
				continue
			}
			why = pick(in, cur.free)
		case refRet:
			rs := a.rets[cur.fn]
			if int(cur.idx) >= len(rs) {
				cur = ref{}
				continue
			}
			why = pick(&rs[cur.idx], cur.free)
		case refLoc:
			li := a.lookLoc(cur.l)
			if li == nil || li.marked == 0 {
				cur = ref{}
				continue
			}
			why = li.why
		}
		if s, ok := a.describe(why, cur); ok {
			s.Wide = wideStep(why)
			rev = append(rev, s)
		}
		cur = why.from
	}
	out := make([]Step, len(rev))
	for i, s := range rev {
		out[len(rev)-1-i] = s
	}
	return out
}

// wideStep reports whether the reason why makes a wide step (see
// Step.Wide).
func wideStep(why reason) bool {
	switch why.kind {
	case kSeedType, kSeedWrapper, kSeedReflect, kSeedOpaque, kSeedCaller, kSeedOpen, kReflectArg, kReflectRet, kOutside, kFlood:
		return true
	case kLoad:
		switch why.from.l.kind {
		case locField, locTyped, locMapKey, locMapVal, locChan, locAtomic:
			return true
		}
	}
	return false
}

// pick returns the reason of a Mark: the one that made it free if the
// user needs it free and it was not free at first.
func pick(in *info, needFree bool) reason {
	if needFree && !in.firstFree && in.freeR != nil {
		return *in.freeR
	}
	return in.first
}

func site(i ssa.Instruction) any {
	if i == nil {
		return nil
	}
	return i
}

// describe returns the step of the reason why of the Mark at, or false
// for a reason that is only a copy.
func (a *analysis) describe(why reason, at ref) (Step, bool) {
	switch why.kind {
	case kSeedSentinel:
		var obj any
		for _, g := range a.class {
			if (Subject{Sentinel: g}).String() == why.text && g.Object() != nil {
				obj = g.Object()
			}
		}
		return Step{Site: obj, Desc: "Sentinel " + why.text}, true
	case kSeedStored:
		return Step{Site: site(why.site), Desc: "value stored into Sentinel " + why.text}, true
	case kSeedType:
		if a.subj.Type != nil {
			return Step{Site: site(why.site), Desc: "value of Concrete Type " + a.subj.String()}, true
		}
		return Step{Site: site(why.site), Desc: fmt.Sprintf("value of type %s, which may equal %s", typeString(why.typ), a.subj)}, true
	case kSeedWrapper:
		return Step{Site: site(why.site), Desc: fmt.Sprintf("value of type %s, which may wrap %s", typeString(why.typ), a.subj)}, true
	case kSeedReflect:
		if a.subj.Type != nil {
			return Step{Site: site(why.site), Desc: fmt.Sprintf("value made by %s, of a type chosen at run time: it may be of Concrete Type %s", why.fn, a.subj)}, true
		}
		return Step{Site: site(why.site), Desc: fmt.Sprintf("value made by %s, of a type chosen at run time: it may equal %s", why.fn, a.subj)}, true
	case kSeedCaller:
		name := "a parameter"
		if p, ok := at.k.v.(*ssa.Parameter); ok {
			name = "parameter " + p.Name()
		}
		var where any
		if at.kind == refValue {
			where = at.k.v
		}
		return Step{Site: where, Desc: fmt.Sprintf("%s of %s: a caller outside the loaded program may pass %s", name, why.fn, a.subj)}, true
	case kSeedOpen:
		return Step{Site: site(why.site), Desc: fmt.Sprintf("exported variable or field: code outside the loaded program may set it to %s", a.subj)}, true
	case kReflectArg:
		return Step{Desc: fmt.Sprintf("passed to %s by a call through reflection (reflect.Value.Call)", why.fn)}, true
	case kReflectRet:
		return Step{Desc: fmt.Sprintf("returned by %s to a call through reflection (reflect.Value.Call)", why.fn)}, true
	case kReflectFresh:
		return Step{Site: site(why.site), Desc: fmt.Sprintf("stored through reflection into the memory made by %s", why.fn)}, true
	case kFlood:
		if at.kind == refLoc {
			return Step{Site: site(why.site), Desc: "stored through reflection, not followed: taken to write " + a.locString(at.l)}, true
		}
		if why.fn != nil {
			return Step{Site: site(why.site), Desc: fmt.Sprintf("stored through reflection, not followed: taken to be what a call of %s hands out", why.fn)}, true
		}
	case kOutside:
		return Step{Site: site(why.site), Desc: "result of a dynamic call on it: the code called may be outside the loaded program"}, true
	case kSeedOpaque:
		return Step{Site: site(why.site), Desc: why.text + ": it may hold anything"}, true
	case kAssert:
		return Step{Site: site(why.site), Desc: "type assertion to " + typeString(why.typ)}, true
	case kLoad:
		return Step{Site: site(why.site), Desc: "loaded from " + a.locString(why.from.l)}, true
	case kStore:
		if at.kind == refLoc {
			return Step{Site: site(why.site), Desc: "stored to " + a.locString(at.l)}, true
		}
	case kWrap:
		return Step{Site: site(why.site), Desc: "wrapped by " + why.text}, true
	case kUnwrap:
		return Step{Site: site(why.site), Desc: "unwrapped"}, true
	case kParam:
		name := "a parameter"
		if p, ok := at.k.v.(*ssa.Parameter); ok {
			name = "parameter " + p.Name()
		}
		return Step{Site: site(why.site), Desc: fmt.Sprintf("passed as %s of %s (this caller passes it)", name, why.fn)}, true
	case kThrough:
		return Step{Site: site(why.site), Desc: fmt.Sprintf("passed to %s, which returns it", why.fn)}, true
	case kReturn:
		return Step{Site: site(why.site), Desc: fmt.Sprintf("returned by %s", why.fn)}, true
	case kCapture:
		return Step{Site: site(why.site), Desc: fmt.Sprintf("captured by %s", why.fn)}, true
	case kAs:
		if at.kind == refLoc {
			return Step{Site: site(why.site), Desc: "set by errors.As in " + a.locString(at.l)}, true
		}
	}
	return Step{}, false
}

// locString names a memory location in a trace.
func (a *analysis) locString(l loc) string {
	t := a.locTypes[l]
	ts := "?"
	if t != nil {
		ts = typeString(t)
	}
	switch l.kind {
	case locCell:
		switch v := l.v.(type) {
		case *ssa.Alloc:
			if v.Comment != "" {
				return "variable " + v.Comment
			}
			return "a local variable"
		case *ssa.FreeVar:
			return "captured variable " + v.Name()
		}
		return "a variable"
	case locGlobal:
		if g, ok := l.v.(*ssa.Global); ok {
			return "variable " + Subject{Sentinel: g}.String()
		}
	case locField:
		if st, ok := t.(*types.Struct); ok && int(l.idx) < st.NumFields() {
			name := "a struct"
			if owner := a.fieldOwner[fieldKey{l.id, int(l.idx)}]; owner != nil {
				name = typeString(owner)
			}
			return fmt.Sprintf("field %s of %s", st.Field(int(l.idx)).Name(), name)
		}
		return "a struct field"
	case locTyped:
		return "memory of type " + ts
	case locMapKey:
		return "the keys of " + ts
	case locMapVal:
		return "the values of " + ts
	case locChan:
		return "a channel of " + ts
	case locPanic:
		return "the panic value"
	case locVarargs:
		if p, ok := l.v.(*ssa.Parameter); ok && p.Parent() != nil {
			return "the variadic arguments of " + p.Parent().String()
		}
		return "variadic arguments"
	case locAtomic:
		return "an atomic value of type " + ts
	}
	return "memory"
}
