// Package target resolves the Target of a Query: the function declaration
// or call site that a command-line position points at.
package target

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/KilledKenny/errtrace/internal/flow"
	"github.com/KilledKenny/errtrace/internal/load"
	"github.com/KilledKenny/errtrace/internal/span"
)

// Kind says whether a Target is a declaration or a call site.
type Kind int

const (
	// Declaration is a Target that names a function, method or closure: its
	// Error Set is everything its error results can carry.
	Declaration Kind = iota + 1
	// CallSite is a Target that names one call: its Error Set is what that
	// call can return to its caller.
	CallSite
)

// String returns "declaration" or "call-site", as in the JSON output.
func (k Kind) String() string {
	switch k {
	case Declaration:
		return "declaration"
	case CallSite:
		return "call-site"
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Target is the resolved Target of a Query.
type Target struct {
	// Kind is Declaration or CallSite.
	Kind Kind
	// Name is how output names the Target: the SSA function string for a
	// declaration (the generic origin for a generic function), and
	// "call to <callee> in <enclosing function>" for a call site.
	Name string
	// Span is the declaration's name (or a closure's func keyword), or the
	// whole call expression of a call site.
	Span span.Span
	// Funcs are the functions to analyse, sorted by name: the declared
	// function, or the function enclosing the call site. For a generic
	// function they are its instances; there may be none.
	Funcs []*ssa.Function
	// Site is the call instruction of a call site Target, in the function
	// that encloses it (for a generic function, its origin, whose body must
	// not be analysed: use SiteByFunc). It is nil for a declaration.
	Site ssa.CallInstruction
	// SiteByFunc maps each of Funcs to its call instruction of the call
	// site. It is nil for a declaration.
	SiteByFunc map[*ssa.Function]ssa.CallInstruction
	// Generic is, for a Target in a generic function, the generic
	// function (the declaration, or the one enclosing the call site),
	// whose body is never analysed; Funcs are its instances. It is nil
	// otherwise.
	Generic *ssa.Function
}

// KeepReachable narrows the Funcs of a generic Target to the instances
// for which reachable reports true: those the analysed program can run.
// If none is reachable, Funcs are left as they are, and the caller adds
// them as extra roots with the unreachable-Target warning; this fallback is
// the intended S06 behaviour until S12 revisits the roots. It does nothing for a
// Target that is not generic.
func (t *Target) KeepReachable(reachable func(*ssa.Function) bool) {
	if t.Generic == nil {
		return
	}
	var keep []*ssa.Function
	for _, fn := range t.Funcs {
		if reachable(fn) {
			keep = append(keep, fn)
		}
	}
	if len(keep) > 0 {
		t.Funcs = keep
	}
}

// errNoTarget is the error for a position that names nothing errtrace can
// query.
var errNoTarget = errors.New("no function declaration or call at position")

// Resolve resolves the position Target at the start of s, a span parsed
// from the command line, in prog. Errors are prefixed with the position,
// as file:line:col.
func Resolve(prog *load.Program, s span.Span) (*Target, error) {
	pkg, file, ok := prog.FileFor(s.File)
	if !ok {
		return nil, fmt.Errorf("%s: file is not in any loaded package", s.File)
	}
	tokFile := prog.Fset.File(file.FileStart)
	rs, err := span.Resolve(s, tokFile)
	if err != nil {
		return nil, err
	}
	pos := tokFile.Pos(rs.Start.Offset)
	t, err := resolve(prog, pkg.TypesInfo, prog.SSAPackage(pkg), file, pos)
	if err != nil {
		return nil, fmt.Errorf("%s:%d:%d: %w", rs.File, rs.Start.Line, rs.Start.Col, err)
	}
	return t, nil
}

func resolve(prog *load.Program, info *types.Info, ssaPkg *ssa.Package, file *ast.File, pos token.Pos) (*Target, error) {
	if ssaPkg == nil {
		return nil, errors.New("package has no SSA form")
	}
	path, _ := astutil.PathEnclosingInterval(file, pos, pos)
	if len(path) == 0 {
		return nil, errNoTarget
	}

	// A position on a declaration: a FuncDecl's name, or the func keyword
	// of a FuncDecl or FuncLit.
	for _, n := range path {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if inIdent(n.Name, pos) || onFuncKeyword(n.Type, pos) {
				return declTarget(prog, info, ssaPkg, n)
			}
		case *ast.FuncLit:
			if onFuncKeyword(n.Type, pos) {
				return closureTarget(prog, ssaPkg, path, n)
			}
		}
	}

	// A position on an interface method's name.
	if id, ok := path[0].(*ast.Ident); ok {
		if fn, ok := info.Defs[id].(*types.Func); ok {
			if recv := fn.Signature().Recv(); recv != nil && types.IsInterface(recv.Type()) {
				return nil, errors.New("interface method has no body; query a call site")
			}
		}
	}

	if call := callAt(path, pos); call != nil {
		return callTarget(prog, info, ssaPkg, path, call)
	}
	return nil, errNoTarget
}

// inIdent reports whether pos is within id.
func inIdent(id *ast.Ident, pos token.Pos) bool {
	return id != nil && id.Pos() <= pos && pos < id.End()
}

// onFuncKeyword reports whether pos is within the func keyword of ft.
func onFuncKeyword(ft *ast.FuncType, pos token.Pos) bool {
	return ft.Func.IsValid() && ft.Func <= pos && pos < ft.Func+token.Pos(len("func"))
}

// callAt returns the call expression whose callee name (an identifier, or
// the Sel of a selector, possibly instantiated) or whose Lparen is at pos.
func callAt(path []ast.Node, pos token.Pos) *ast.CallExpr {
	if call, ok := path[0].(*ast.CallExpr); ok && call.Lparen == pos {
		return call
	}
	id, ok := path[0].(*ast.Ident)
	if !ok {
		return nil
	}
	var node ast.Node = id
	i := 1
	if i < len(path) {
		if sel, ok := path[i].(*ast.SelectorExpr); ok && sel.Sel == id {
			node = sel
			i++
		}
	}
	if i < len(path) {
		switch ix := path[i].(type) {
		case *ast.IndexExpr:
			if ix.X == node {
				node = ix
				i++
			}
		case *ast.IndexListExpr:
			if ix.X == node {
				node = ix
				i++
			}
		}
	}
	if i < len(path) {
		if call, ok := path[i].(*ast.CallExpr); ok && call.Fun == node {
			return call
		}
	}
	return nil
}

func declTarget(prog *load.Program, info *types.Info, ssaPkg *ssa.Package, decl *ast.FuncDecl) (*Target, error) {
	obj, ok := info.Defs[decl.Name].(*types.Func)
	if !ok {
		return nil, errNoTarget
	}
	fn := ssaPkg.Prog.FuncValue(obj)
	if fn == nil {
		return nil, fmt.Errorf("no SSA function for %s", obj.FullName())
	}
	if len(flow.ErrorResults(fn.Signature)) == 0 {
		return nil, fmt.Errorf("%s has no error result", fn)
	}
	return &Target{
		Kind:    Declaration,
		Name:    fn.String(),
		Span:    span.Of(prog.Fset, decl.Name.Pos(), decl.Name.End()),
		Funcs:   instances(prog, fn),
		Generic: genericOf(fn),
	}, nil
}

func closureTarget(prog *load.Program, ssaPkg *ssa.Package, path []ast.Node, lit *ast.FuncLit) (*Target, error) {
	fn := ssa.EnclosingFunction(ssaPkg, path)
	if fn == nil || fn.Pos() != lit.Type.Func {
		return nil, errors.New("no SSA function for closure")
	}
	if len(flow.ErrorResults(fn.Signature)) == 0 {
		return nil, fmt.Errorf("%s has no error result", fn)
	}
	return &Target{
		Kind:    Declaration,
		Name:    fn.String(),
		Span:    span.Of(prog.Fset, lit.Type.Func, lit.Type.Func+token.Pos(len("func"))),
		Funcs:   instances(prog, fn),
		Generic: genericOf(fn),
	}, nil
}

func callTarget(prog *load.Program, info *types.Info, ssaPkg *ssa.Package, path []ast.Node, call *ast.CallExpr) (*Target, error) {
	callee := types.ExprString(call.Fun)
	tv, ok := info.Types[call.Fun]
	if !ok || tv.IsType() || tv.IsBuiltin() {
		return nil, errors.New("not a call to a function")
	}
	sig, ok := tv.Type.Underlying().(*types.Signature)
	if !ok {
		return nil, errors.New("not a call to a function")
	}
	if len(flow.ErrorResults(sig)) == 0 {
		return nil, fmt.Errorf("%s has no error result", callee)
	}
	enclosing := ssa.EnclosingFunction(ssaPkg, path)
	if enclosing == nil {
		return nil, errors.New("call is not inside a function")
	}
	site := findCall(enclosing, call.Lparen)
	if site == nil {
		return nil, fmt.Errorf("no SSA call instruction for call to %s", callee)
	}
	t := &Target{
		Kind:       CallSite,
		Name:       fmt.Sprintf("call to %s in %s", callee, enclosing),
		Span:       span.Of(prog.Fset, call.Pos(), call.End()),
		Funcs:      instances(prog, enclosing),
		Site:       site,
		SiteByFunc: make(map[*ssa.Function]ssa.CallInstruction),
		Generic:    genericOf(enclosing),
	}
	for _, fn := range t.Funcs {
		s := site
		if fn != enclosing {
			s = findCall(fn, call.Lparen)
		}
		if s == nil {
			return nil, fmt.Errorf("no SSA call instruction for call to %s in %s", callee, fn)
		}
		t.SiteByFunc[fn] = s
	}
	return t, nil
}

// findCall returns the call instruction of fn whose call position is
// lparen. Calls in go and defer statements are found too.
func findCall(fn *ssa.Function, lparen token.Pos) ssa.CallInstruction {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			if c, ok := instr.(ssa.CallInstruction); ok && c.Common().Pos() == lparen {
				return c
			}
		}
	}
	return nil
}

// instances returns the functions to analyse for fn, sorted by name: fn
// itself, or, if fn is generic (or nested in a generic function), its
// instances. The generic origin itself is never returned, because its body
// gets no call-graph edges.
func instances(prog *load.Program, fn *ssa.Function) []*ssa.Function {
	if !isGeneric(fn) {
		return []*ssa.Function{fn}
	}
	var out []*ssa.Function
	for f := range ssautil.AllFunctions(prog.SSA) {
		if f != fn && f.Origin() == fn {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// genericOf returns fn if it is generic (see isGeneric), and nil
// otherwise.
func genericOf(fn *ssa.Function) *ssa.Function {
	if isGeneric(fn) {
		return fn
	}
	return nil
}

// isGeneric reports whether fn or a function enclosing it has type
// parameters.
func isGeneric(fn *ssa.Function) bool {
	for f := fn; f != nil; f = f.Parent() {
		if f.TypeParams().Len() > 0 {
			return true
		}
	}
	return false
}
