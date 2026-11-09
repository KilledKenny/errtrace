package flow

import (
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// SentinelInfo is what errtrace knows about the value of a package-level
// variable.
type SentinelInfo struct {
	// Type is the dynamic type of the value, or nil if errtrace does not
	// know it: the variable's own type if it is not an interface, else the
	// one type that every store of its initialization gives it.
	Type types.Type
	// Canon is the variable whose value it is: the variable itself, or,
	// when its only store copies another variable (var ErrX = io.EOF, or
	// os.ErrNotExist = fs.ErrNotExist, maybe through a function that
	// returns that variable), the Canon of that one. Two variables with
	// the same Canon hold the same value.
	Canon *ssa.Global
	// Fresh reports that the value of Canon is a pointer that was
	// allocated for it and for nothing else (errors.New, &T{}). Two
	// variables whose Canons differ and are both Fresh hold different
	// values.
	Fresh bool
}

// Sentinels finds and caches what errtrace knows about the values of
// package-level variables in one analysis run (see Info). A Sentinels is
// not safe for concurrent use. Its zero value is not ready; use
// NewSentinels.
type Sentinels struct {
	info map[*ssa.Global]SentinelInfo
	// scanned are the programs whose stores have been collected.
	scanned map[*ssa.Program]bool
	// initStores are the stores to each variable in its package's init
	// function (the variable initializations).
	initStores map[*ssa.Global][]*ssa.Store
	// unstable are the variables stored to outside their package's init
	// function, or whose address is used other than to load or store.
	unstable map[*ssa.Global]bool
}

// NewSentinels returns an empty Sentinels.
func NewSentinels() *Sentinels {
	return &Sentinels{
		info:       make(map[*ssa.Global]SentinelInfo),
		scanned:    make(map[*ssa.Program]bool),
		initStores: make(map[*ssa.Global][]*ssa.Store),
		unstable:   make(map[*ssa.Global]bool),
	}
}

// Info returns what errtrace knows about the value of g. A variable keeps
// the value of its initialization only if nothing else in the program
// stores to it or takes its address; otherwise its dynamic type (for an
// interface variable) is unknown and it is its own Canon, not Fresh.
// Stored values are followed through conversions, loads of other
// package-level variables and calls of functions whose every return
// gives the same value or type (errors.New gives a Fresh
// *errors.errorString).
func (s *Sentinels) Info(g *ssa.Global) SentinelInfo {
	if g == nil {
		return SentinelInfo{}
	}
	if inf, ok := s.info[g]; ok {
		return inf
	}
	if g.Pkg != nil {
		s.scan(g.Pkg.Prog)
	}
	inf := s.compute(g, make(map[any]bool), 0)
	s.info[g] = inf
	return inf
}

// scan collects the stores to package-level variables in every function
// of prog's packages, and marks the variables whose address is used.
func (s *Sentinels) scan(prog *ssa.Program) {
	if prog == nil || s.scanned[prog] {
		return
	}
	s.scanned[prog] = true
	seen := make(map[*ssa.Function]bool)
	var visit func(fn *ssa.Function)
	visit = func(fn *ssa.Function) {
		if fn == nil || seen[fn] {
			return
		}
		seen[fn] = true
		var init *ssa.Function
		if fn.Pkg != nil {
			init = fn.Pkg.Func("init")
		}
		var rands []*ssa.Value
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				rands = instr.Operands(rands[:0])
				for _, r := range rands {
					g, ok := (*r).(*ssa.Global)
					if !ok {
						continue
					}
					switch instr := instr.(type) {
					case *ssa.Store:
						if r == &instr.Addr {
							if fn == init && fn.Pkg == g.Pkg {
								s.initStores[g] = append(s.initStores[g], instr)
							} else {
								s.unstable[g] = true
							}
							continue
						}
					case *ssa.UnOp:
						if instr.Op == token.MUL {
							continue
						}
					}
					s.unstable[g] = true
				}
			}
		}
		for _, anon := range fn.AnonFuncs {
			visit(anon)
		}
	}
	for _, pkg := range prog.AllPackages() {
		for _, m := range pkg.Members {
			switch m := m.(type) {
			case *ssa.Function:
				visit(m)
			case *ssa.Type:
				named, ok := m.Type().(*types.Named)
				if !ok {
					continue
				}
				for i := range named.NumMethods() {
					visit(prog.FuncValue(named.Method(i)))
				}
			}
		}
	}
}

// maxDynDepth bounds how deep Sentinels follows values.
const maxDynDepth = 8

func (s *Sentinels) compute(g *ssa.Global, visiting map[any]bool, depth int) SentinelInfo {
	out := SentinelInfo{Canon: g}
	p, ok := g.Type().Underlying().(*types.Pointer)
	if !ok {
		return out
	}
	iface := types.IsInterface(p.Elem())
	if !iface {
		out.Type = unalias(p.Elem())
	}
	if visiting[g] || depth > maxDynDepth || s.unstable[g] {
		return out
	}
	visiting[g] = true
	defer delete(visiting, g)
	stores := s.initStores[g]
	if iface {
		vals := make([]ssa.Value, len(stores))
		for i, st := range stores {
			vals[i] = st.Val
		}
		if len(vals) > 0 {
			out.Type = s.sameType(vals, visiting, depth)
		}
	}
	if len(stores) == 1 {
		if h := s.aliasOf(stores[0].Val, visiting, depth+1); h != nil {
			hi := s.compute(h, visiting, depth+1)
			out.Canon, out.Fresh = hi.Canon, hi.Fresh
		} else {
			out.Fresh = s.fresh(stores[0].Val, visiting, depth+1)
		}
	}
	return out
}

// aliasOf returns the package-level variable whose value v always is, or
// nil: v loads it (maybe converted), or v is a call of a function whose
// every return gives the same variable.
func (s *Sentinels) aliasOf(v ssa.Value, visiting map[any]bool, depth int) *ssa.Global {
	if depth > maxDynDepth {
		return nil
	}
	switch v := v.(type) {
	case *ssa.ChangeInterface:
		return s.aliasOf(v.X, visiting, depth+1)
	case *ssa.ChangeType:
		return s.aliasOf(v.X, visiting, depth+1)
	case *ssa.MakeInterface:
		return s.aliasOf(v.X, visiting, depth+1)
	case *ssa.UnOp:
		if g, ok := v.X.(*ssa.Global); ok && v.Op == token.MUL {
			return g
		}
	case *ssa.Call:
		results := s.returns(v, visiting)
		if results == nil {
			return nil
		}
		fn := v.Common().StaticCallee()
		visiting[fn] = true
		defer delete(visiting, fn)
		var out *ssa.Global
		for _, r := range results {
			h := s.aliasOf(r, visiting, depth+1)
			if h == nil || (out != nil && h != out) {
				return nil
			}
			out = h
		}
		return out
	}
	return nil
}

// fresh reports whether v is always a pointer allocated where v is made
// (maybe converted to an interface), not one stored anywhere else.
func (s *Sentinels) fresh(v ssa.Value, visiting map[any]bool, depth int) bool {
	if depth > maxDynDepth {
		return false
	}
	switch v := v.(type) {
	case *ssa.Alloc:
		return v.Heap
	case *ssa.MakeInterface:
		if _, ok := v.X.Type().Underlying().(*types.Pointer); !ok {
			// A value of a non-pointer type may equal another one.
			return false
		}
		return s.fresh(v.X, visiting, depth+1)
	case *ssa.ChangeInterface:
		return s.fresh(v.X, visiting, depth+1)
	case *ssa.ChangeType:
		return s.fresh(v.X, visiting, depth+1)
	case *ssa.Phi:
		return s.allFresh(v.Edges, visiting, depth)
	case *ssa.Call:
		results := s.returns(v, visiting)
		if results == nil {
			return false
		}
		fn := v.Common().StaticCallee()
		visiting[fn] = true
		defer delete(visiting, fn)
		return s.allFresh(results, visiting, depth)
	}
	return false
}

func (s *Sentinels) allFresh(vs []ssa.Value, visiting map[any]bool, depth int) bool {
	for _, e := range vs {
		if !s.fresh(e, visiting, depth+1) {
			return false
		}
	}
	return len(vs) > 0
}

// returns returns the values the static callee of call returns, if it has
// a body, one result and is not being followed already, or nil.
func (s *Sentinels) returns(call *ssa.Call, visiting map[any]bool) []ssa.Value {
	fn := call.Common().StaticCallee()
	if fn == nil || fn.Blocks == nil || fn.Signature.Results().Len() != 1 || visiting[fn] {
		return nil
	}
	var results []ssa.Value
	for _, b := range fn.Blocks {
		if len(b.Instrs) == 0 {
			continue
		}
		if r, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return); ok {
			results = append(results, r.Results[0])
		}
	}
	return results
}

// dynType returns the dynamic type of the interface value v, if every
// value it can have has the same one that errtrace can find, or nil.
func (s *Sentinels) dynType(v ssa.Value, visiting map[any]bool, depth int) types.Type {
	if depth > maxDynDepth {
		return nil
	}
	switch v := v.(type) {
	case *ssa.MakeInterface:
		if t := v.X.Type(); !types.IsInterface(t) {
			return unalias(t)
		}
	case *ssa.ChangeInterface:
		return s.dynType(v.X, visiting, depth+1)
	case *ssa.UnOp:
		if g, ok := v.X.(*ssa.Global); ok && v.Op == token.MUL {
			return s.compute(g, visiting, depth+1).Type
		}
	case *ssa.Phi:
		return s.sameType(v.Edges, visiting, depth)
	case *ssa.Call:
		results := s.returns(v, visiting)
		if results == nil {
			return nil
		}
		fn := v.Common().StaticCallee()
		visiting[fn] = true
		defer delete(visiting, fn)
		return s.sameType(results, visiting, depth)
	}
	return nil
}

// sameType returns the dynamic type all of vs have, or nil.
func (s *Sentinels) sameType(vs []ssa.Value, visiting map[any]bool, depth int) types.Type {
	var out types.Type
	for _, e := range vs {
		t := s.dynType(e, visiting, depth+1)
		if t == nil || (out != nil && !types.Identical(out, t)) {
			return nil
		}
		out = t
	}
	return out
}

// canonVar returns the variable of the Canon of inf, or of g if the Canon
// has none.
func canonVar(inf SentinelInfo, g *types.Var) *types.Var {
	if inf.Canon != nil {
		if v, ok := inf.Canon.Object().(*types.Var); ok {
			return v
		}
	}
	return g
}
