package resolve

import (
	"sort"

	"golang.org/x/tools/go/ssa"
)

// Upper gives the call edges of a program as an upper bound: every
// function that may run at a call site, and every call site that may call
// a function. It serves the upward pass of a Membership Query, which must
// never miss an edge, where Resolver serves the downward pass, which shows
// only the Candidates it can witness.
//
// A static call has its static callee. For a dynamic call, Upper uses the
// Candidates VTA found when there are any and the program is whole (see
// WholeProgram); otherwise it falls back to the callees of the CHA call
// graph: every function or method of the loaded program that matches the
// call, whether or not the roots reach it.
//
// An Upper is not safe for concurrent use.
type Upper struct {
	v *VTA
	// out are the CHA callees of the dynamic call sites of a function,
	// built when one of its sites is first asked about.
	out map[*ssa.Function]map[ssa.CallInstruction][]*ssa.Function
	// vtaCallers are the dynamic call sites that have fn among their VTA
	// Candidates; it is built on first use, for a whole program only.
	vtaCallers map[*ssa.Function][]ssa.CallInstruction
	names      map[*ssa.Function]string
}

// Upper returns the upper-bound call edges of v's program.
func (v *VTA) Upper() *Upper {
	return &Upper{
		v:     v,
		out:   make(map[*ssa.Function]map[ssa.CallInstruction][]*ssa.Function),
		names: make(map[*ssa.Function]string),
	}
}

// WholeProgram reports whether v's roots are only the main and init
// functions it was built from, so that the functions VTA calls reachable
// are all that can run and its Candidates are complete. It is false after
// WithRoots added a Target that nothing reaches (such a Target has callers
// outside what VTA saw, so the Candidates of the calls it leads to may be
// incomplete), and after SetOpen.
func (v *VTA) WholeProgram() bool { return !v.extended }

// SetOpen records that the program is not whole: it was loaded without a
// main package, so code outside it calls its functions.
func (v *VTA) SetOpen() { v.extended = true }

// Reachable reports whether fn may run: in a whole program, whether the
// roots reach it; otherwise always.
func (u *Upper) Reachable(fn *ssa.Function) bool {
	return !u.v.WholeProgram() || u.v.Reachable(fn)
}

// WholeProgram reports whether u trusts the Candidates of VTA (see
// VTA.WholeProgram).
func (u *Upper) WholeProgram() bool { return u.v.WholeProgram() }

// trusted returns the callees VTA found for the dynamic call site, if u
// uses them for it. They are the ones before splicing: the wrapper of a
// promoted or bound method is the callee, not the method it calls, so that
// the operands of the call are the parameters of the callee.
func (u *Upper) trusted(site ssa.CallInstruction) []*ssa.Function {
	if !u.v.WholeProgram() {
		return nil
	}
	return u.v.raw[site]
}

// Callees returns the functions that may run at site, sorted by name: the
// static callee, the VTA Candidates of a dynamic call in a whole program,
// or else its CHA callees. It returns nothing for a call of a builtin, and
// for a dynamic call that no function of the program matches.
func (u *Upper) Callees(site ssa.CallInstruction) []*ssa.Function {
	common := site.Common()
	if callee := common.StaticCallee(); callee != nil {
		return []*ssa.Function{callee}
	}
	if _, ok := common.Value.(*ssa.Builtin); ok {
		return nil
	}
	if fns := u.trusted(site); len(fns) > 0 {
		return fns
	}
	parent := site.Parent()
	bySite, ok := u.out[parent]
	if !ok {
		bySite = u.chaCallees(parent)
		u.out[parent] = bySite
	}
	return bySite[site]
}

// chaCallees groups the CHA callees of the dynamic call sites of fn by
// site, each sorted by name.
func (u *Upper) chaCallees(fn *ssa.Function) map[ssa.CallInstruction][]*ssa.Function {
	n := u.v.cha.Nodes[fn]
	if n == nil {
		return nil
	}
	bySite := make(map[ssa.CallInstruction][]*ssa.Function)
	seen := make(map[ssa.CallInstruction]map[*ssa.Function]bool)
	for _, e := range n.Out {
		if e.Site == nil || e.Callee == nil || e.Callee.Func == nil || e.Site.Common().StaticCallee() != nil {
			continue
		}
		s := seen[e.Site]
		if s == nil {
			s = make(map[*ssa.Function]bool)
			seen[e.Site] = s
		}
		if !s[e.Callee.Func] {
			s[e.Callee.Func] = true
			bySite[e.Site] = append(bySite[e.Site], e.Callee.Func)
		}
	}
	for site, fns := range bySite {
		sort.SliceStable(fns, func(i, j int) bool { return u.name(fns[i]) < u.name(fns[j]) })
		bySite[site] = fns
	}
	return bySite
}

func (u *Upper) name(fn *ssa.Function) string {
	s, ok := u.names[fn]
	if !ok {
		s = fn.String()
		u.names[fn] = s
	}
	return s
}

// Callers returns the call sites where fn may run: those whose Callees
// include fn. Their order is not specified.
func (u *Upper) Callers(fn *ssa.Function) []ssa.CallInstruction {
	var out []ssa.CallInstruction
	seen := make(map[ssa.CallInstruction]bool)
	add := func(site ssa.CallInstruction) {
		if !seen[site] {
			seen[site] = true
			out = append(out, site)
		}
	}
	if n := u.v.cha.Nodes[fn]; n != nil {
		for _, e := range n.In {
			if e.Site == nil {
				continue
			}
			// A dynamic call with trusted Candidates has only those.
			if e.Site.Common().StaticCallee() == nil && len(u.trusted(e.Site)) > 0 {
				continue
			}
			add(e.Site)
		}
	}
	if u.v.WholeProgram() {
		if u.vtaCallers == nil {
			u.vtaCallers = make(map[*ssa.Function][]ssa.CallInstruction)
			for site, fns := range u.v.raw {
				for _, g := range fns {
					u.vtaCallers[g] = append(u.vtaCallers[g], site)
				}
			}
		}
		for _, site := range u.vtaCallers[fn] {
			add(site)
		}
	}
	return out
}
