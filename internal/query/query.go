// Package query turns a resolved Target into its Error Set: the entries of
// every Error Origin and Unresolved place that can reach the Target's error
// results, each with a Propagation Path, de-duplicated and ordered.
//
// For now a Query uses only the Target functions' own bodies: calls are not
// followed (they are Unresolved, kind pending), and a call-site Target is a
// single pending entry.
package query

import (
	"context"
	"fmt"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
	"github.com/killedKenny/errtrace/internal/flow"
	"github.com/killedKenny/errtrace/internal/load"
	"github.com/killedKenny/errtrace/internal/span"
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

	prog *load.Program
}

// Run computes the Error Set of tgt in prog.
func Run(ctx context.Context, prog *load.Program, tgt *target.Target) (*Result, error) {
	r := &Result{Target: tgt, Table: errset.NewTable(), prog: prog}
	var entries []errset.Entry
	for _, fn := range tgt.Funcs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch tgt.Kind {
		case target.Declaration:
			entries = append(entries, r.declaration(fn)...)
		case target.CallSite:
			entries = append(entries, r.callSite(fn, tgt.SiteByFunc[fn])...)
		default:
			return nil, fmt.Errorf("unknown Target kind %v", tgt.Kind)
		}
	}
	entries = errset.Dedup(entries)
	errset.Sort(r.Table, entries, r.position)
	r.Entries = entries
	return r, nil
}

// declaration returns the entries of fn's error results.
func (r *Result) declaration(fn *ssa.Function) []errset.Entry {
	var entries []errset.Entry
	lf := flow.Analyze(r.Table, fn)
	for _, res := range lf.Results {
		for _, f := range res.Facts {
			path := errset.Path{{Site: f.Source, Func: fn.String()}}
			entries = append(entries, errset.Flatten(r.Table, r.top(f.Value), path)...)
		}
	}
	return entries
}

// callSite returns the entries of a call-site Target in fn. Its semantics
// come later; for now the call is one pending entry.
func (r *Result) callSite(fn *ssa.Function, site ssa.CallInstruction) []errset.Entry {
	if site == nil {
		return nil
	}
	id := r.Table.Intern(errset.Value{
		Kind:       errset.Unresolved,
		Site:       site,
		Pos:        site.Common().Pos(),
		Unresolved: errset.Pending,
		Detail:     pendingDetail(flow.CalleeName(site.Common())),
	})
	return errset.Flatten(r.Table, id, errset.Path{{Site: site, Func: fn.String()}})
}

// top maps a local value to what it is at the top of a Query: calls not
// followed yet become pending, and parameters and captured variables
// depend on the caller. Wraps are rebuilt around their mapped inner
// values.
func (r *Result) top(id errset.ValueID) errset.ValueID {
	v := r.Table.Get(id)
	switch v.Kind {
	case errset.CallRef:
		return r.Table.Intern(errset.Value{
			Kind: errset.Unresolved, Site: v.Site, Pos: v.Pos,
			Unresolved: errset.Pending, Detail: pendingDetail(v.Detail),
		})
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

func pendingDetail(callee string) string {
	return fmt.Sprintf("call to %s not followed yet", callee)
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
