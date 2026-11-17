package cli

import (
	"context"
	"fmt"
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/flow"
	"github.com/KilledKenny/errtrace/internal/load"
	"github.com/KilledKenny/errtrace/internal/mark"
	"github.com/KilledKenny/errtrace/internal/query"
	"github.com/KilledKenny/errtrace/internal/render"
	"github.com/KilledKenny/errtrace/internal/resolve"
	"github.com/KilledKenny/errtrace/internal/target"
)

// runMembership runs the Membership Query for subj on tgt, prints its
// Verdict and returns the exit code of the Verdict.
func runMembership(ctx context.Context, opts Options, cfg *config, prog *load.Program, tgt *target.Target, resolver *resolve.VTA, subj mark.Subject, open []*ssa.Package) (int, error) {
	if g := subj.Sentinel; g != nil && flow.NewSentinels().Info(g).Unstable {
		fmt.Fprintf(opts.Stderr, "%s: warning: %s is assigned outside its initialization (or its address is used); the Verdict assumes it keeps its initial value\n", BinaryName, subj)
	}
	res, err := query.Membership(ctx, prog, tgt, query.MembershipOptions{
		Options: query.Options{ChainDepth: cfg.chainDepth, Resolver: resolver},
		Subject: subj,
		Upper:   resolver.Upper(),
		Open:    open,
	})
	if err != nil {
		return 0, err
	}
	if cfg.debug["summary"] {
		fmt.Fprintf(opts.Stderr, "summary: functions=%d sccs=%d\n", res.Stats.Functions, res.Stats.SCCs)
		if res.MarkRan {
			st := res.MarkStats
			fmt.Fprintf(opts.Stderr, "mark: functions=%d marked=%d values=%d locations=%d reflectstores=%d\n", st.Functions, st.MarkedFunctions, st.Values, st.Locations, st.ReflectStores)
		}
	}
	if cfg.debug["timing"] {
		// The upward pass is skipped when an entry settles the Verdict.
		fmt.Fprintf(opts.Stderr, "timing: mark=%s\n", res.MarkTime)
	}
	in := render.MembershipInput{
		Input: render.Input{
			TargetSpan: tgt.Span,
			TargetName: tgt.Name,
			Table:      res.Table,
			Locator:    res,
		},
		Subject:     subj.String(),
		Verdict:     res.Verdict.String(),
		Matches:     res.Matches,
		TargetTrace: steps(res.TargetTrace),
		Full:        cfg.full,
	}
	for _, rs := range res.ReflectStores {
		in.NotFollowed = append(in.NotFollowed, render.ReflectStore{Site: rs.Site, Function: rs.Fn.String()})
	}
	for _, c := range res.Carriers {
		in.Carriers = append(in.Carriers, render.CarrierEntry{Entry: c.Entry, Unresolved: c.Unresolved, Trace: steps(c.Trace), Wrap: c.Wrap, HasWrap: c.HasWrap})
	}
	if err := render.Membership(opts.Stdout, in); err != nil {
		return 0, err
	}
	switch res.Verdict {
	case query.Yes:
		return ExitOK, nil
	case query.Maybe:
		return ExitMaybe, nil
	}
	if res.ApartFromReflection() {
		// Printed as "no, apart from reflection": not a proof.
		return ExitMaybe, nil
	}
	return ExitNo, nil
}

// steps converts the steps of a trace for the renderer.
func steps(trace []mark.Step) []render.TraceStep {
	out := make([]render.TraceStep, len(trace))
	for i, s := range trace {
		out[i] = render.TraceStep{Site: s.Site, Desc: s.Desc, Wide: s.Wide}
	}
	return out
}

// resolveSubject finds the Subject of a Membership Query in the loaded
// program: the package-level variable is names (<pkgpath>.<Name>), which
// must have an error type, or the type as names ([*]<pkgpath>.<Name>),
// which must be a Concrete Type: not an interface, and implementing
// error. An alias names the type it stands for (*os.PathError is
// *io/fs.PathError). The errors name what was looked up.
func resolveSubject(prog *load.Program, is, as string) (mark.Subject, error) {
	if is != "" {
		pkg, name, err := lookupPackage(prog, "-is", is)
		if err != nil {
			return mark.Subject{}, err
		}
		g, ok := pkg.Members[name].(*ssa.Global)
		if !ok {
			return mark.Subject{}, fmt.Errorf("-is %s: package %s has no package-level variable %s", is, pkg.Pkg.Path(), name)
		}
		elem := g.Type().Underlying().(*types.Pointer).Elem()
		if !flow.IsErrorType(elem) {
			return mark.Subject{}, fmt.Errorf("-is %s: variable %s.%s is not of an error type (its type is %s)", is, pkg.Pkg.Path(), name, types.TypeString(elem, nil))
		}
		return mark.Subject{Sentinel: g}, nil
	}
	rest, pointer := strings.CutPrefix(as, "*")
	switch {
	case strings.ContainsAny(rest, "*[]{}() "):
		if i := strings.IndexByte(rest, '['); i > 0 && strings.HasSuffix(rest, "]") {
			return mark.Subject{}, fmt.Errorf("-as %s: an instance of a generic type is not supported", as)
		}
		return mark.Subject{}, fmt.Errorf("-as %s: want a named type or a pointer to one: [*]<pkgpath>.<Name>", as)
	}
	pkg, name, err := lookupPackage(prog, "-as", rest)
	if err != nil {
		return mark.Subject{}, err
	}
	obj, ok := pkg.Pkg.Scope().Lookup(name).(*types.TypeName)
	if !ok {
		return mark.Subject{}, fmt.Errorf("-as %s: package %s has no type %s", as, pkg.Pkg.Path(), name)
	}
	typ := types.Unalias(obj.Type())
	if n, ok := typ.(*types.Named); ok && n.TypeParams().Len() > 0 {
		return mark.Subject{}, fmt.Errorf("-as %s: %s is a generic type, which -as does not support", as, types.TypeString(typ, nil))
	}
	if pointer {
		typ = types.NewPointer(typ)
	}
	errorType := types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
	switch {
	case types.IsInterface(typ):
		return mark.Subject{}, fmt.Errorf("-as %s: %s is an interface, not a Concrete Type", as, types.TypeString(typ, nil))
	case !types.Implements(typ, errorType):
		hint := ""
		if !pointer && types.Implements(types.NewPointer(typ), errorType) {
			hint = fmt.Sprintf(" (*%s does)", types.TypeString(typ, nil))
		}
		return mark.Subject{}, fmt.Errorf("-as %s: type %s does not implement error%s", as, types.TypeString(typ, nil), hint)
	}
	return mark.Subject{Type: typ}, nil
}

// lookupPackage splits <pkgpath>.<Name> at its last dot and finds the
// package in the loaded program.
func lookupPackage(prog *load.Program, flagName, value string) (*ssa.Package, string, error) {
	i := strings.LastIndex(value, ".")
	if i <= 0 || i == len(value)-1 {
		return nil, "", fmt.Errorf("%s %s: want <pkgpath>.<Name>", flagName, value)
	}
	path, name := value[:i], value[i+1:]
	pkg := prog.SSA.ImportedPackage(path)
	if pkg == nil {
		return nil, "", fmt.Errorf("%s %s: package %s is not in the loaded program", flagName, value, path)
	}
	return pkg, name, nil
}
