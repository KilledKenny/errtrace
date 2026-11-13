// Package cli is the command-line front end: it parses flags and the Target
// argument, applies the exit-code conventions and runs a Query. With -is or
// -as it runs a Membership Query instead, and the exit code is its Verdict.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/load"
	"github.com/KilledKenny/errtrace/internal/mark"
	"github.com/KilledKenny/errtrace/internal/query"
	"github.com/KilledKenny/errtrace/internal/render"
	"github.com/KilledKenny/errtrace/internal/resolve"
	"github.com/KilledKenny/errtrace/internal/span"
	"github.com/KilledKenny/errtrace/internal/target"
)

// Exit codes, as in gopls.
const (
	// ExitOK is returned on success, including an Error Set with Unresolved
	// entries or no entries at all.
	ExitOK = 0
	// ExitError is returned for usage errors, Targets that can't be resolved,
	// package load or type errors, and internal failures.
	ExitError = 2
	// ExitNo is returned by a Membership Query (-is, -as) whose Verdict is
	// no; a yes Verdict returns ExitOK.
	ExitNo = 1
	// ExitMaybe is returned by a Membership Query whose Verdict is maybe.
	ExitMaybe = 3
)

// Options is everything one Query run depends on, so that tests can run the
// CLI in-process without touching the process's working directory,
// environment or standard streams.
type Options struct {
	// Args are the command-line arguments, without the program name.
	Args []string
	// Dir is the working directory; relative Target paths resolve against it.
	Dir string
	// Env is the environment passed to the go command (os.Environ() in Main).
	Env []string
	// Stdout receives only the result of the Query.
	Stdout io.Writer
	// Stderr receives usage text, diagnostics and error messages.
	Stderr io.Writer
}

// Main runs the command with the process's working directory and environment
// and returns the exit code. It is all that cmd/errtrace calls.
func Main(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	dir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", BinaryName, err)
		return ExitError
	}
	return Run(ctx, Options{Args: args, Dir: dir, Env: os.Environ(), Stdout: stdout, Stderr: stderr})
}

// DefaultFanout is the default of -fanout: entries are grouped under a
// dynamic call site with at least this many Candidates.
const DefaultFanout = 8

// debugModes are the values accepted by the hidden -debug flag.
var debugModes = []string{"target", "timing", "summary"}

// hiddenFlags are registered but left out of the usage text.
var hiddenFlags = map[string]bool{"debug": true}

// membershipFlags select a Membership Query.
var membershipFlags = map[string]bool{"is": true, "as": true}

// config holds the parsed flags.
type config struct {
	tags       string
	patterns   string
	chainDepth int
	fanout     int
	// is and as are the Subject of a Membership Query: a Sentinel
	// (<pkgpath>.<Name>) or a Concrete Type; at most one is set.
	is, as string
	// full prints every Carrier of a Membership Query in full.
	full  bool
	debug map[string]bool
}

// usageError is an error caused by how the command was invoked; the usage
// text is printed after its message.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// Run executes one Query described by opts and returns the exit code.
func Run(ctx context.Context, opts Options) int {
	fs, cfg := newFlagSet()
	code, err := run(ctx, opts, fs, cfg)
	switch {
	case err == nil:
		return code
	case errors.Is(err, flag.ErrHelp):
		printUsage(opts.Stdout, fs)
		return ExitOK
	}
	fmt.Fprintf(opts.Stderr, "%s: %v\n", BinaryName, err)
	var uerr usageError
	if errors.As(err, &uerr) {
		printUsage(opts.Stderr, fs)
	}
	return ExitError
}

// run executes the Query and returns its exit code, or an error.
func run(ctx context.Context, opts Options, fs *flag.FlagSet, cfg *config) (int, error) {
	code, err := runQuery(ctx, opts, fs, cfg)
	if err != nil {
		return ExitError, err
	}
	return code, nil
}

func runQuery(ctx context.Context, opts Options, fs *flag.FlagSet, cfg *config) (int, error) {
	if err := fs.Parse(opts.Args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, err
		}
		return 0, usageError{err}
	}
	membership := false
	var empty error
	fs.Visit(func(f *flag.Flag) {
		if membershipFlags[f.Name] {
			membership = true
			if f.Value.String() == "" && empty == nil {
				empty = fmt.Errorf("-%s needs a value", f.Name)
			}
		}
	})
	if empty != nil {
		return 0, usageError{empty}
	}
	if cfg.is != "" && cfg.as != "" {
		return 0, usageError{errors.New("-is and -as are mutually exclusive")}
	}
	debug, err := parseDebug(fs.Lookup("debug").Value.String())
	if err != nil {
		return 0, usageError{err}
	}
	cfg.debug = debug
	if cfg.chainDepth < 1 {
		return 0, usageError{fmt.Errorf("invalid -chain-depth %d (want at least 1)", cfg.chainDepth)}
	}
	if cfg.fanout < 0 {
		return 0, usageError{fmt.Errorf("invalid -fanout %d (want 0 or more)", cfg.fanout)}
	}
	if fs.NArg() != 1 {
		return 0, usageError{fmt.Errorf("expected exactly one Target argument, got %d", fs.NArg())}
	}
	pos, err := span.Parse(fs.Arg(0), opts.Dir)
	if err != nil {
		return 0, err
	}
	prog, err := load.Load(ctx, load.Config{
		Dir:        opts.Dir,
		Env:        opts.Env,
		Tags:       splitList(cfg.tags),
		Patterns:   splitList(cfg.patterns),
		TargetFile: pos.File,
	})
	if err != nil {
		return 0, err
	}
	if cfg.debug["timing"] {
		fmt.Fprintf(opts.Stderr, "timing: load=%s ssa=%s\n", prog.Timing.Load, prog.Timing.SSA)
	}
	tgt, err := target.Resolve(prog, pos)
	if err != nil {
		return 0, err
	}
	var subj mark.Subject
	if membership {
		if subj, err = resolveSubject(prog, cfg.is, cfg.as); err != nil {
			return 0, usageError{err}
		}
	}
	resolver, open, err := newResolver(ctx, prog, tgt, opts.Stderr, cfg.debug["timing"])
	if err != nil {
		return 0, err
	}
	if cfg.debug["target"] {
		names := make([]string, len(tgt.Funcs))
		for i, fn := range tgt.Funcs {
			names[i] = fn.String()
		}
		fmt.Fprintf(opts.Stderr, "target: %s %s funcs=[%s] span=%s\n",
			tgt.Kind, tgt.Name, strings.Join(names, " "), tgt.Span)
	}
	if membership {
		return runMembership(ctx, opts, cfg, prog, tgt, resolver, subj, open)
	}
	res, err := query.Run(ctx, prog, tgt, query.Options{ChainDepth: cfg.chainDepth, Resolver: resolver})
	if err != nil {
		return 0, err
	}
	if cfg.debug["summary"] {
		fmt.Fprintf(opts.Stderr, "summary: functions=%d sccs=%d\n", res.Stats.Functions, res.Stats.SCCs)
	}
	return ExitOK, render.Text(opts.Stdout, render.Input{
		TargetSpan: tgt.Span,
		TargetName: tgt.Name,
		Table:      res.Table,
		Entries:    res.Entries,
		Locator:    res,
		Fanout:     cfg.fanout,
	})
}

// newResolver builds the VTA Resolver from the main and init roots of the
// loaded program. A generic Target is narrowed to its reachable instances.
// Target functions that are not reachable from the roots are added as
// roots, with a warning: their parameters have no callers to take
// Candidates from. The loaded packages that are not main packages, and the
// package of a Target the roots do not reach, are open (code that is not
// loaded calls them): the Resolver is told so, and they are returned.
func newResolver(ctx context.Context, prog *load.Program, tgt *target.Target, stderr io.Writer, timing bool) (*resolve.VTA, []*ssa.Package, error) {
	var initial []*ssa.Package
	for _, p := range prog.Initial {
		if sp := prog.SSAPackage(p); sp != nil {
			initial = append(initial, sp)
		}
	}
	v, err := resolve.NewVTA(ctx, prog.SSA, resolve.Roots(prog.SSA, initial))
	if err != nil {
		return nil, nil, err
	}
	// A loaded package that is not a main package is open: code that is
	// not loaded may call it. An internal package is not: only the code
	// around its internal directory can import it, and that code is
	// taken to be loaded with it (load the whole module).
	var open []*ssa.Package
	isOpen := make(map[*ssa.Package]bool)
	for _, p := range initial {
		if p.Pkg.Name() != "main" && !internalPackage(p.Pkg.Path()) && !isOpen[p] {
			isOpen[p] = true
			open = append(open, p)
		}
	}
	var unreachable []*ssa.Function
	for _, fn := range tgt.Funcs {
		if !v.Reachable(fn) {
			unreachable = append(unreachable, fn)
		}
	}
	// So is the package of a Target that the roots do not reach: what
	// calls it is not loaded.
	for _, fn := range unreachable {
		pkg := fn.Pkg
		for f := fn; pkg == nil && f != nil; f = f.Parent() {
			pkg = f.Pkg
		}
		if pkg != nil && !isOpen[pkg] {
			isOpen[pkg] = true
			open = append(open, pkg)
		}
	}
	if len(open) > 0 {
		v.SetOpen()
	}
	if len(unreachable) > 0 {
		fmt.Fprintf(stderr, "%s: warning: target not reachable from main/init; parameters may lack candidates\n", BinaryName)
		if v, err = v.WithRoots(ctx, unreachable); err != nil {
			return nil, nil, err
		}
	}
	if timing {
		fmt.Fprintf(stderr, "timing: cha=%s vta1=%s vta2=%s\n", v.Timing.CHA, v.Timing.VTA1, v.Timing.VTA2)
	}
	return v, open, nil
}

// internalPackage reports whether path is the import path of an internal
// package: it has an element "internal".
func internalPackage(path string) bool {
	return path == "internal" || strings.HasPrefix(path, "internal/") ||
		strings.HasSuffix(path, "/internal") || strings.Contains(path, "/internal/")
}

// splitList splits a comma-separated flag value, dropping empty elements.
func splitList(value string) []string {
	var out []string
	for _, s := range strings.Split(value, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func newFlagSet() (*flag.FlagSet, *config) {
	cfg := new(config)
	fs := flag.NewFlagSet(BinaryName, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // Run reports errors and usage itself.
	fs.StringVar(&cfg.tags, "tags", "", "comma-separated `list` of build tags")
	fs.StringVar(&cfg.patterns, "p", "./...", "comma-separated package `patterns` to load")
	fs.IntVar(&cfg.chainDepth, "chain-depth", query.DefaultChainDepth, "max Wrap Chain depth `N` before truncation")
	fs.IntVar(&cfg.fanout, "fanout", DefaultFanout, "group entries under dynamic call sites with at least `N` Candidates (0 = never group)")
	fs.StringVar(&cfg.is, "is", "", "Membership Query: is the Sentinel `pkgpath.Name` (such as io.EOF) in the Error Set?")
	fs.StringVar(&cfg.as, "as", "", "Membership Query: is the Concrete Type `type` (such as '*io/fs.PathError') in the Error Set?")
	fs.BoolVar(&cfg.full, "full", false, "Membership Query: print every entry that could carry the error in full, instead of grouping those whose traces share a pivot")
	fs.String("debug", "", "comma-separated `list` of "+strings.Join(debugModes, ",")+" (stderr only)")
	return fs, cfg
}

func parseDebug(value string) (map[string]bool, error) {
	modes := make(map[string]bool)
	if value == "" {
		return modes, nil
	}
	for _, m := range strings.Split(value, ",") {
		valid := false
		for _, d := range debugModes {
			valid = valid || m == d
		}
		if !valid {
			return nil, fmt.Errorf("invalid -debug value %q (want a comma-separated list of %s)", m, strings.Join(debugModes, ","))
		}
		modes[m] = true
	}
	return modes, nil
}

func printUsage(w io.Writer, fs *flag.FlagSet) {
	defer fmt.Fprintf(w, `
With -is or -as, %[1]s answers a Membership Query: whether that error is in
the Target's Error Set. The exit status is then the Verdict:
  0 yes | 1 no | 3 maybe | 2 error
`, BinaryName)
	fmt.Fprintf(w, `usage: %[1]s [flags] <target>

%[1]s reports the Error Set of a Target: every Error Origin that can reach
the Target's error result, with its Propagation Path.

<target> is a position in a Go file, with byte columns:
  file.go:LINE:COL | file.go:#OFFSET | file.go:L:C-L2:C2 (the start is used)

flags:
`, BinaryName)
	fs.VisitAll(func(f *flag.Flag) {
		if hiddenFlags[f.Name] {
			return
		}
		name, usage := flag.UnquoteUsage(f)
		fmt.Fprintf(w, "  -%s %s\n    \t%s", f.Name, name, usage)
		if f.DefValue != "" {
			// As flag.PrintDefaults: only string defaults are quoted.
			if g, ok := f.Value.(flag.Getter); ok && isString(g.Get()) {
				fmt.Fprintf(w, " (default %q)", f.DefValue)
			} else {
				fmt.Fprintf(w, " (default %s)", f.DefValue)
			}
		}
		fmt.Fprintln(w)
	})
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}
