// Package summary computes per-function summaries of error results: for
// each error result of a function, the abstract error values it can carry,
// expressed in terms of the function's own parameters (Param) and captured
// variables (FreeVar), each with the provenance that Propagation Paths are
// rebuilt from.
//
// Summaries are computed on demand, for the functions a Query's error
// results depend on (the demand graph), and solved bottom-up over the
// graph's strongly connected components with a capped fixpoint inside each
// one. Call results are replaced by the callees' summaries, substituting
// the call's arguments for the callees' parameters.
package summary

import (
	"slices"

	"golang.org/x/tools/go/ssa"

	"github.com/killedKenny/errtrace/internal/errset"
)

// SourceKind says how a fact reached a function's error result.
type SourceKind uint8

const (
	// Local is a fact created in the function itself.
	Local SourceKind = iota + 1
	// Via is a fact that came from a callee's summary through a call.
	Via
)

// MaxSources is how many sources a fact keeps, ranked local first, then
// static calls, then dynamic calls.
const MaxSources = 4

// Source is one piece of provenance of a fact: where its value came from.
type Source struct {
	// Kind says whether the value was created locally or came through a
	// call.
	Kind SourceKind
	// Site is, for a Local source, the SSA value or instruction where the
	// value enters the function (hop 0 of a Propagation Path); for a Via
	// source, the ssa.CallInstruction.
	Site any
	// Callee is the function called at a Via source's Site.
	Callee *ssa.Function
	// CalleeResult is the index of the callee's error result the value
	// came from.
	CalleeResult int
	// CalleeValue is the value in the callee's summary that became this
	// fact. It differs from the fact's value when it was a Param (or held
	// one) substituted with this call's argument.
	CalleeValue errset.ValueID
	// ArgSources are the sources of the argument value substituted for a
	// Param in CalleeValue, in the calling function; empty if nothing was
	// substituted.
	ArgSources []Source
	// Dynamic reports whether a Via source's call is a dynamic call.
	Dynamic bool
}

// rank orders sources: local first, then static calls, then dynamic calls.
func (s Source) rank() int {
	switch {
	case s.Kind == Local:
		return 0
	case !s.Dynamic:
		return 1
	}
	return 2
}

func sourceEqual(a, b Source) bool {
	return a.Kind == b.Kind && a.Site == b.Site && a.Callee == b.Callee &&
		a.CalleeResult == b.CalleeResult && a.CalleeValue == b.CalleeValue &&
		a.Dynamic == b.Dynamic && slices.EqualFunc(a.ArgSources, b.ArgSources, sourceEqual)
}

// addSource adds src to srcs unless it is there already, keeping srcs
// ranked and at most MaxSources long.
func addSource(srcs []Source, src Source) []Source {
	if slices.ContainsFunc(srcs, func(s Source) bool { return sourceEqual(s, src) }) {
		return srcs
	}
	i := len(srcs)
	for i > 0 && srcs[i-1].rank() > src.rank() {
		i--
	}
	if i >= MaxSources {
		return srcs
	}
	srcs = slices.Insert(srcs, i, src)
	if len(srcs) > MaxSources {
		srcs = srcs[:MaxSources]
	}
	return srcs
}

// Fact is one abstract value an error result can carry, with its sources.
type Fact struct {
	// Value is the abstract value: an Error Origin, a Wrap, an Unresolved
	// place, or a Param or FreeVar of the summarised function.
	Value errset.ValueID
	// Sources is the value's provenance, ranked (see MaxSources).
	Sources []Source
}

// ResultSummary is the summary of one error result.
type ResultSummary struct {
	// Index is the result's index in the function's signature.
	Index int
	// Facts are the values the result can carry, in a deterministic order.
	Facts []Fact
}

// Summary is what a function's error results can carry, symbolic in its
// parameters and captured variables.
type Summary struct {
	// Fn is the summarised function.
	Fn *ssa.Function
	// Results has one element per error result, in signature order.
	Results []ResultSummary
}

// Result returns the summary of error result index, or nil if index is
// not an error result of the function.
func (s *Summary) Result(index int) *ResultSummary {
	for i := range s.Results {
		if s.Results[i].Index == index {
			return &s.Results[i]
		}
	}
	return nil
}

// factSet collects facts in insertion order, merging the sources of equal
// values.
type factSet struct {
	facts []Fact
	index map[errset.ValueID]int
}

func (fs *factSet) add(id errset.ValueID, src Source) {
	if fs.index == nil {
		fs.index = make(map[errset.ValueID]int)
	}
	if i, ok := fs.index[id]; ok {
		fs.facts[i].Sources = addSource(fs.facts[i].Sources, src)
		return
	}
	fs.index[id] = len(fs.facts)
	fs.facts = append(fs.facts, Fact{Value: id, Sources: []Source{src}})
}
