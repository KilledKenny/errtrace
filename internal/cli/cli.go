// Package cli is the command-line front end: it parses flags and the Target
// argument, applies the exit-code conventions and runs a Query.
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

	"github.com/killedKenny/errtrace/internal/load"
	"github.com/killedKenny/errtrace/internal/query"
	"github.com/killedKenny/errtrace/internal/render"
	"github.com/killedKenny/errtrace/internal/span"
	"github.com/killedKenny/errtrace/internal/target"
)

// Exit codes, as in gopls.
const (
	// ExitOK is returned on success, including an Error Set with Unresolved
	// entries or no entries at all.
	ExitOK = 0
	// ExitError is returned for usage errors, Targets that can't be resolved,
	// package load or type errors, and internal failures.
	ExitError = 2
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

// debugModes are the values accepted by the hidden -debug flag.
var debugModes = []string{"target", "timing", "summary"}

// hiddenFlags are registered but left out of the usage text.
var hiddenFlags = map[string]bool{"debug": true}

// config holds the parsed flags.
type config struct {
	tags     string
	patterns string
	debug    map[string]bool
}

// usageError is an error caused by how the command was invoked; the usage
// text is printed after its message.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// Run executes one Query described by opts and returns the exit code.
func Run(ctx context.Context, opts Options) int {
	fs, cfg := newFlagSet()
	err := run(ctx, opts, fs, cfg)
	switch {
	case err == nil:
		return ExitOK
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

func run(ctx context.Context, opts Options, fs *flag.FlagSet, cfg *config) error {
	if err := fs.Parse(opts.Args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError{err}
	}
	debug, err := parseDebug(fs.Lookup("debug").Value.String())
	if err != nil {
		return usageError{err}
	}
	cfg.debug = debug
	if fs.NArg() != 1 {
		return usageError{fmt.Errorf("expected exactly one Target argument, got %d", fs.NArg())}
	}
	pos, err := span.Parse(fs.Arg(0), opts.Dir)
	if err != nil {
		return err
	}
	prog, err := load.Load(ctx, load.Config{
		Dir:        opts.Dir,
		Env:        opts.Env,
		Tags:       splitList(cfg.tags),
		Patterns:   splitList(cfg.patterns),
		TargetFile: pos.File,
	})
	if err != nil {
		return err
	}
	if cfg.debug["timing"] {
		fmt.Fprintf(opts.Stderr, "timing: load=%s ssa=%s\n", prog.Timing.Load, prog.Timing.SSA)
	}
	tgt, err := target.Resolve(prog, pos)
	if err != nil {
		return err
	}
	if cfg.debug["target"] {
		names := make([]string, len(tgt.Funcs))
		for i, fn := range tgt.Funcs {
			names[i] = fn.String()
		}
		fmt.Fprintf(opts.Stderr, "target: %s %s funcs=[%s] span=%s\n",
			tgt.Kind, tgt.Name, strings.Join(names, " "), tgt.Span)
	}
	res, err := query.Run(ctx, prog, tgt)
	if err != nil {
		return err
	}
	if cfg.debug["summary"] {
		fmt.Fprintf(opts.Stderr, "summary: functions=%d sccs=%d\n", res.Stats.Functions, res.Stats.SCCs)
	}
	return render.Text(opts.Stdout, render.Input{
		TargetSpan: tgt.Span,
		TargetName: tgt.Name,
		Table:      res.Table,
		Entries:    res.Entries,
		Locator:    res,
	})
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
			fmt.Fprintf(w, " (default %q)", f.DefValue)
		}
		fmt.Fprintln(w)
	})
}
