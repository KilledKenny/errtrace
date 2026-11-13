// Package load loads the packages a Query needs with go/packages, builds
// their SSA form and maps SSA values and instructions back to source spans.
package load

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/KilledKenny/errtrace/internal/span"
)

// maxListedErrors is how many package errors a load error lists.
const maxListedErrors = 10

// Config describes which packages to load for a Query.
type Config struct {
	// Dir is the directory the go command runs in; relative patterns
	// resolve against it.
	Dir string
	// Env is the environment of the go command.
	Env []string
	// Tags are the build tags, passed as -tags.
	Tags []string
	// Patterns are the package patterns to load; empty means ./... .
	Patterns []string
	// Tests also loads the test variants of the matched packages.
	Tests bool
	// TargetFile, if set, is the absolute path of the file that holds a
	// position Target. Its package is loaded even when no pattern matches it.
	TargetFile string
}

// Timing records how long each loading phase took.
type Timing struct {
	// Load is the time go/packages took to load and type-check.
	Load time.Duration
	// SSA is the time it took to build the SSA program.
	SSA time.Duration
}

// Program is a loaded, type-checked program in SSA form: everything a Query
// needs to resolve its Target and print spans.
type Program struct {
	// Fset holds the positions of every loaded file.
	Fset *token.FileSet
	// SSA is the built SSA program, including generic instances.
	SSA *ssa.Program
	// Packages are all loaded packages (initial ones and their
	// dependencies), by package ID.
	Packages map[string]*packages.Package
	// Initial are the packages the patterns (and TargetFile) matched, in
	// the order go/packages returned them.
	Initial []*packages.Package
	// Timing records how long loading took.
	Timing Timing

	// byPath maps an absolute file name to the packages whose syntax
	// includes it, in preference order (see FileFor).
	byPath map[string][]fileRef
	// byTokFile maps a token.File to its syntax tree.
	byTokFile map[*token.File]*ast.File
}

type fileRef struct {
	pkg  *packages.Package
	file *ast.File
}

// Load loads the packages described by cfg, type-checks them and builds the
// SSA program. Package list, parse and type errors are returned as one
// error that lists the first few of them.
func Load(ctx context.Context, cfg Config) (*Program, error) {
	pcfg := &packages.Config{
		Context: ctx,
		Fset:    token.NewFileSet(),
		Mode:    packages.LoadAllSyntax | packages.NeedModule,
		Dir:     cfg.Dir,
		Env:     cfg.Env,
		Tests:   cfg.Tests,
	}
	if len(cfg.Tags) > 0 {
		pcfg.BuildFlags = []string{"-tags=" + strings.Join(cfg.Tags, ",")}
	}
	patterns := cfg.Patterns
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	if cfg.TargetFile != "" {
		patterns = append(patterns[:len(patterns):len(patterns)], "file="+cfg.TargetFile)
	}

	start := time.Now()
	pkgs, err := packages.Load(pcfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("loading packages: %w", err)
	}
	if err := packageErrors(pkgs); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	loaded := time.Now()

	prog := &Program{
		Fset:      pcfg.Fset,
		Packages:  make(map[string]*packages.Package),
		Initial:   pkgs,
		byPath:    make(map[string][]fileRef),
		byTokFile: make(map[*token.File]*ast.File),
	}
	ssaProg, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.SSA = ssaProg
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		prog.Packages[p.ID] = p
		if p.Module != nil && p.Module.Main {
			if sp := ssaProg.Package(p.Types); sp != nil {
				sp.SetDebugMode(true)
			}
		}
	})
	ssaProg.Build()
	prog.indexFiles()
	prog.Timing = Timing{Load: loaded.Sub(start), SSA: time.Since(loaded)}
	return prog, ctx.Err()
}

// packageErrors returns an error listing the errors packages.PrintErrors
// would print, or nil if there are none.
func packageErrors(pkgs []*packages.Package) error {
	var msgs []string
	seenModules := make(map[*packages.Module]bool)
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if m := p.Module; m != nil && m.Error != nil && !seenModules[m] {
			seenModules[m] = true
			msgs = append(msgs, m.Error.Err)
		}
		for _, e := range p.Errors {
			msgs = append(msgs, e.Error())
		}
	})
	switch {
	case len(msgs) == 0:
		return nil
	case len(msgs) == 1:
		return errors.New(msgs[0])
	}
	n := len(msgs)
	if n > maxListedErrors {
		msgs = append(msgs[:maxListedErrors], fmt.Sprintf("(and %d more)", n-maxListedErrors))
	}
	return fmt.Errorf("%d errors loading packages:\n\t%s", n, strings.Join(msgs, "\n\t"))
}

// indexFiles records which packages each file belongs to.
func (p *Program) indexFiles() {
	ids := make([]string, 0, len(p.Packages))
	for id := range p.Packages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		pkg := p.Packages[id]
		for _, f := range pkg.Syntax {
			tf := p.Fset.File(f.FileStart)
			if tf == nil {
				continue
			}
			p.byTokFile[tf] = f
			name := filepath.Clean(tf.Name())
			p.byPath[name] = append(p.byPath[name], fileRef{pkg, f})
		}
	}
	for _, refs := range p.byPath {
		// Non-test variants first, then by package ID (already sorted).
		sort.SliceStable(refs, func(i, j int) bool {
			return !isTestVariant(refs[i].pkg) && isTestVariant(refs[j].pkg)
		})
	}
}

func isTestVariant(p *packages.Package) bool {
	return p.ForTest != "" || strings.HasSuffix(p.PkgPath, ".test")
}

// FileFor returns the package and syntax tree of the file at the absolute
// path abs. When the file belongs to several packages (test variants), the
// non-test variant is preferred, then the lowest package ID. It reports
// false if no loaded package contains the file.
func (p *Program) FileFor(abs string) (*packages.Package, *ast.File, bool) {
	refs := p.byPath[filepath.Clean(abs)]
	if len(refs) == 0 {
		return nil, nil, false
	}
	return refs[0].pkg, refs[0].file, true
}

// SSAPackage returns the SSA package built for pkg, or nil.
func (p *Program) SSAPackage(pkg *packages.Package) *ssa.Package {
	return p.SSA.Package(pkg.Types)
}

// syntaxAt returns the syntax tree of the file that contains pos, or nil.
func (p *Program) syntaxAt(pos token.Pos) *ast.File {
	tf := p.Fset.File(pos)
	if tf == nil {
		return nil
	}
	return p.byTokFile[tf]
}

// Span returns the point span at pos. It reports false if pos is not a
// valid position.
func (p *Program) Span(pos token.Pos) (span.Span, bool) {
	if !pos.IsValid() || p.Fset.File(pos) == nil {
		return span.Span{}, false
	}
	return span.Of(p.Fset, pos, token.NoPos), true
}

// SpanOfValue returns the span of the source expression that computes v,
// such as an Error Origin. It uses, in order: the call expression of a call;
// the expression of a DebugRef (present in module packages, built in debug
// mode); the innermost expression enclosing v's position; the operand of an
// implicit MakeInterface; the position of a Return that yields v; and v's
// position as a point span. It reports false if none of these is known.
func (p *Program) SpanOfValue(v ssa.Value) (span.Span, bool) {
	if call, ok := v.(*ssa.Call); ok {
		return p.SpanOfInstr(call)
	}
	if d := debugRef(v); d != nil {
		return span.Of(p.Fset, d.Expr.Pos(), d.Expr.End()), true
	}
	switch v := v.(type) {
	case *ssa.Global:
		if obj := v.Object(); obj != nil && obj.Pos().IsValid() {
			return span.Of(p.Fset, obj.Pos(), obj.Pos()+token.Pos(len(obj.Name()))), true
		}
	case *ssa.Function:
		if obj := v.Object(); obj != nil && obj.Pos().IsValid() {
			return span.Of(p.Fset, obj.Pos(), obj.Pos()+token.Pos(len(obj.Name()))), true
		}
	case *ssa.Parameter:
		if obj := v.Object(); obj != nil && obj.Pos().IsValid() {
			return span.Of(p.Fset, obj.Pos(), obj.Pos()+token.Pos(len(obj.Name()))), true
		}
	case *ssa.MakeInterface:
		if !v.Pos().IsValid() {
			if s, ok := p.SpanOfValue(v.X); ok {
				return s, true
			}
		}
	}
	if s, ok := p.enclosingExpr(v.Pos()); ok {
		return s, true
	}
	if refs := v.Referrers(); refs != nil {
		for _, r := range *refs {
			if ret, ok := r.(*ssa.Return); ok {
				if s, ok := p.Span(ret.Pos()); ok {
					return s, true
				}
			}
		}
	}
	return p.Span(v.Pos())
}

// debugRef returns the DebugRef that names the expression whose value is v,
// preferring one over an expression whose address is v (such as the
// &T{...} of an allocated composite literal over the literal itself).
func debugRef(v ssa.Value) *ssa.DebugRef {
	refs := v.Referrers()
	if refs == nil {
		return nil
	}
	var addr *ssa.DebugRef
	for _, r := range *refs {
		d, ok := r.(*ssa.DebugRef)
		if !ok || d.X != v || d.Expr == nil {
			continue
		}
		if !d.IsAddr {
			return d
		}
		if addr == nil {
			addr = d
		}
	}
	return addr
}

// SpanOfInstr returns the span of the source of instr: for a call
// instruction (including go and defer), the ast.CallExpr whose Lparen is the
// call's position; for a value, SpanOfValue; otherwise the innermost
// expression enclosing its position, or a point span. It reports false if
// instr has no position.
func (p *Program) SpanOfInstr(instr ssa.Instruction) (span.Span, bool) {
	if c, ok := instr.(ssa.CallInstruction); ok {
		if call := p.callExprAt(c.Common().Pos()); call != nil {
			return span.Of(p.Fset, call.Pos(), call.End()), true
		}
		return p.Span(c.Common().Pos())
	}
	if v, ok := instr.(ssa.Value); ok {
		return p.SpanOfValue(v)
	}
	if s, ok := p.enclosingExpr(instr.Pos()); ok {
		return s, true
	}
	return p.Span(instr.Pos())
}

// callExprAt returns the call expression whose Lparen is at lparen, or nil.
func (p *Program) callExprAt(lparen token.Pos) *ast.CallExpr {
	f := p.syntaxAt(lparen)
	if f == nil || !lparen.IsValid() {
		return nil
	}
	path, _ := astutil.PathEnclosingInterval(f, lparen, lparen)
	for _, n := range path {
		if call, ok := n.(*ast.CallExpr); ok && call.Lparen == lparen {
			return call
		}
	}
	return nil
}

// enclosingExpr returns the span of the innermost expression that encloses
// pos, for code without debug information.
func (p *Program) enclosingExpr(pos token.Pos) (span.Span, bool) {
	f := p.syntaxAt(pos)
	if f == nil || !pos.IsValid() {
		return span.Span{}, false
	}
	path, _ := astutil.PathEnclosingInterval(f, pos, pos)
	for _, n := range path {
		if e, ok := n.(ast.Expr); ok {
			return span.Of(p.Fset, e.Pos(), e.End()), true
		}
	}
	return span.Span{}, false
}
