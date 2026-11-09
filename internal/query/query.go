// Package query turns a resolved Target into its Error Set: the entries of
// every Error Origin and Unresolved place that can reach the Target's error
// results, each with a Propagation Path, de-duplicated and ordered.
//
// The Target functions' summaries follow static calls across functions,
// packages and the stdlib. A call-site Target's set is what the call's
// callees can return, with the site's arguments substituted.
package query

import (
	"context"
	"fmt"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
	"github.com/killedKenny/errtrace/internal/flow"
	"github.com/killedKenny/errtrace/internal/load"
	"github.com/killedKenny/errtrace/internal/resolve"
	"github.com/killedKenny/errtrace/internal/span"
	"github.com/killedKenny/errtrace/internal/summary"
	"github.com/killedKenny/errtrace/internal/target"
)

// Result is the Error Set of one Query.
type Result struct {
	// Target is the Target the Query was run on.
	Target *target.Target
	// Table holds every value the entries refer to.
	Table *errset.Table
	// Entries are the Error Set's entries, de-duplicated and in output
	// order.
	Entries []errset.Entry
	// Stats counts the summaries computed for the Query.
	Stats summary.Stats

	prog *load.Program
}

// DefaultChainDepth is the default Wrap Chain depth limit of a Query.
const DefaultChainDepth = flow.DefaultChainDepth

// Options configures a Query.
type Options struct {
	// ChainDepth is the most Wraps a Wrap Chain can have before it is
	// marked truncated; zero means DefaultChainDepth.
	ChainDepth int
}

// Run computes the Error Set of tgt in prog.
func Run(ctx context.Context, prog *load.Program, tgt *target.Target, opts Options) (*Result, error) {
	r := &Result{Target: tgt, Table: errset.NewTable(), prog: prog}
	solver := summary.NewSolver(r.Table, resolve.Static{}, flow.Options{ChainDepth: opts.ChainDepth})
	var entries []errset.Entry
	switch tgt.Kind {
	case target.Declaration:
		if err := solver.Solve(ctx, tgt.Funcs); err != nil {
			return nil, err
		}
		for _, fn := range tgt.Funcs {
			for _, res := range solver.Summary(fn).Results {
				entries = append(entries, r.entries(solver, fn, res.Facts)...)
			}
		}
	case target.CallSite:
		for _, fn := range tgt.Funcs {
			site := tgt.SiteByFunc[fn]
			if site == nil {
				continue
			}
			if err := solver.SolveSite(ctx, fn, site); err != nil {
				return nil, err
			}
			for _, idx := range flow.ErrorResults(site.Common().Signature()) {
				entries = append(entries, r.entries(solver, fn, solver.SiteFacts(fn, site, idx))...)
			}
		}
	default:
		return nil, fmt.Errorf("unknown Target kind %v", tgt.Kind)
	}
	r.Stats = solver.Stats()
	entries = errset.Dedup(r.Table, entries)
	errset.Sort(r.Table, entries, r.position)
	r.Entries = entries
	return r, nil
}

// entries flattens the facts that reach fn at the top of the Query into
// one entry per leaf, each with the best Propagation Path of that leaf and
// a count of the leaf's other sources.
func (r *Result) entries(solver *summary.Solver, fn *ssa.Function, facts []summary.Fact) []errset.Entry {
	var out []errset.Entry
	for _, f := range facts {
		entries := errset.Flatten(r.Table, r.top(f.Value))
		if len(entries) != len(f.Leaves) && testing.Testing() {
			// A fact has one source list per leaf; under go test a
			// mismatch fails loudly instead of losing paths.
			panic(fmt.Sprintf("query: fact %v has %d leaves but %d leaf source lists", f.Value, len(entries), len(f.Leaves)))
		}
		for k := range entries {
			if k < len(f.Leaves) {
				entries[k].Path, _ = solver.Path(fn, f.Leaves[k])
				entries[k].MorePaths = len(f.Leaves[k]) - 1
			}
		}
		out = append(out, entries...)
	}
	return out
}

// top maps a summary value to what it is at the top of a Query:
// parameters and captured variables depend on the caller, and a format
// forwarded to fmt.Errorf from a parameter is a dynamic format. Wraps are
// rebuilt around their mapped inner values.
func (r *Result) top(id errset.ValueID) errset.ValueID {
	v := r.Table.Get(id)
	switch v.Kind {
	case errset.Param:
		return r.Table.Intern(errset.Value{
			Kind: errset.Unresolved, Site: v.Site, Pos: v.Pos,
			Unresolved: errset.DependsOnCaller,
			Detail:     fmt.Sprintf("parameter %s (%s)", v.Name, types.TypeString(v.Type, nil)),
		})
	case errset.FreeVar:
		return r.Table.Intern(errset.Value{
			Kind: errset.Unresolved, Site: v.Site, Pos: v.Pos,
			Unresolved: errset.DependsOnCaller,
			Detail:     fmt.Sprintf("captured variable %s (%s)", v.Name, types.TypeString(v.Type, nil)),
		})
	case errset.ErrorfLike:
		return r.Table.Intern(errset.Value{
			Kind: errset.Unresolved, Site: v.Site, Pos: v.Pos,
			Unresolved: errset.DynamicFormat,
			Detail:     fmt.Sprintf("format of fmt.Errorf comes from parameter %s of %s", v.Name, v.Fn),
		})
	case errset.Wrap, errset.Truncated:
		inner := make([]errset.ValueID, len(v.Inner))
		changed := false
		for i, in := range v.Inner {
			inner[i] = r.top(in)
			changed = changed || inner[i] != in
		}
		if !changed {
			return id
		}
		v.Inner = inner
		return r.Table.Intern(v)
	}
	return id
}

// Unresolved returns the number of entries with an Unresolved leaf.
func (r *Result) Unresolved() int { return errset.CountUnresolved(r.Table, r.Entries) }

// Locate returns the span of a value's or hop's Site: the identifier of a
// variable (a Sentinel), or the source expression of an SSA value or
// instruction. It reports false if the site has no known position.
func (r *Result) Locate(site any) (span.Span, bool) {
	switch s := site.(type) {
	case types.Object:
		if s.Pos().IsValid() {
			return span.Of(r.prog.Fset, s.Pos(), s.Pos()+token.Pos(len(s.Name()))), true
		}
	case ssa.Value:
		return r.prog.SpanOfValue(s)
	case ssa.Instruction:
		return r.prog.SpanOfInstr(s)
	}
	return span.Span{}, false
}

// position is the errset.PosFunc of r: the file and start offset of a
// value's span.
func (r *Result) position(id errset.ValueID) (string, int) {
	s, ok := r.Locate(r.Table.Get(id).Site)
	if !ok {
		return "", -1
	}
	return s.File, s.Start.Offset
}
