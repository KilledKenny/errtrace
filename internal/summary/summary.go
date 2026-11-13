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
//
// A load of a struct field is replaced by the summary of the field's
// location: the join of the values that the reachable program stores to
// that field of that struct type anywhere (see fields.go). Locations are
// nodes of the demand graph like functions: a function depends on the
// locations it loads from, and a location on what its writers store.
package summary

import (
	"slices"

	"golang.org/x/tools/go/ssa"

	"github.com/KilledKenny/errtrace/internal/errset"
)

// SourceKind says how a fact reached a function's error result.
type SourceKind uint8

const (
	// Local is a fact created in the function itself.
	Local SourceKind = iota + 1
	// Via is a fact that came from a callee's summary through a call.
	Via
	// Captured is a fact read from a variable a closure captured: it
	// enters the closure at Site, and came from the values the creating
	// function (ArgFn) bound to the variable, with sources ArgSources.
	Captured
	// Stored is a fact loaded, at Site, from a place that another
	// function writes: it is the value that function (ArgFn) stores with
	// the instruction Store, where it has the sources ArgSources.
	Stored
	// Passed is a fact that a function with no call context (the writer
	// of a field location) got through a parameter: it is the value that
	// the caller ArgFn passes at the call Site to Callee, where it has the
	// sources ArgSources; Then are the sources of the parameter itself, on
	// from there.
	Passed
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
	// CalleeLeaf is the index of the leaf of CalleeValue this leaf came
	// from (in errset.Flatten's leaf order): the leaf itself, or the Param
	// (or forwarded format) whose argument holds it.
	CalleeLeaf int
	// ArgSources are the sources, in the calling function, of this leaf
	// inside the argument substituted for the callee leaf CalleeLeaf;
	// empty if nothing was substituted.
	ArgSources []Source
	// Dynamic reports whether a Via source's call is a dynamic call.
	Dynamic bool
	// ArgFn is, for a Captured source, the function that created the
	// closure, and, for a Stored source, the function that makes the
	// store: where ArgSources are.
	ArgFn *ssa.Function
	// Store is, for a Stored source, the instruction of ArgFn that stores
	// the value.
	Store ssa.Instruction
	// Then are, for a Passed source, the sources of the parameter that
	// received the value, in the function the source belongs to.
	Then []Source
	// Field is, for a Stored source of a value loaded from a field
	// location, the location's name ("field Err of io/fs.PathError"). It
	// is empty for a variable that a closure stores to.
	Field string
	// viaField reports that every way the value comes through this source
	// goes through a field location (see locOnly).
	viaField bool
}

// locOnly reports whether every source of srcs goes through a field
// location: the value they belong to is known only as "what the program
// stores to that field". Lists are kept with the sources that do not
// first (see addSource), so the first one decides.
func locOnly(srcs []Source) bool {
	return len(srcs) > 0 && srcs[0].viaField
}

// rank orders sources: local first, then static calls, then dynamic calls.
func (s Source) rank() int {
	switch {
	case s.Kind == Local, s.Kind == Captured:
		return 0
	case s.Kind == Stored, s.Kind == Passed, !s.Dynamic:
		return 1
	}
	return 2
}

func sourceEqual(a, b Source) bool {
	return a.Kind == b.Kind && a.Site == b.Site && a.Callee == b.Callee && a.Field == b.Field && a.viaField == b.viaField &&
		a.CalleeResult == b.CalleeResult && a.CalleeValue == b.CalleeValue && a.CalleeLeaf == b.CalleeLeaf &&
		a.Dynamic == b.Dynamic && a.ArgFn == b.ArgFn && a.Store == b.Store &&
		sourcesEqual(a.ArgSources, b.ArgSources) && sourcesEqual(a.Then, b.Then)
}

// sourcesEqual compares two source lists; lists that share their backing
// array (as the argument sources of the sources made by one substitution
// do) are equal without looking at them.
func sourcesEqual(a, b []Source) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 || &a[0] == &b[0] {
		return true
	}
	return slices.EqualFunc(a, b, sourceEqual)
}

// addSource adds src to srcs unless it is there already, keeping srcs
// ranked and at most MaxSources long. The sources that do not go through a
// field location come before those that do, whatever their rank: a list
// with one of them is never cut down to location sources only.
func addSource(srcs []Source, src Source) []Source {
	if slices.ContainsFunc(srcs, func(s Source) bool { return sourceEqual(s, src) }) {
		return srcs
	}
	after := func(a, b Source) bool {
		if a.viaField != b.viaField {
			return a.viaField
		}
		return a.rank() > b.rank()
	}
	i := len(srcs)
	for i > 0 && after(srcs[i-1], src) {
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

// Fact is one abstract value an error result can carry, with the
// provenance of each of its leaves.
type Fact struct {
	// Value is the abstract value: an Error Origin, a Wrap, an Unresolved
	// place, or a Param or FreeVar of the summarised function.
	Value errset.ValueID
	// Leaves has the sources of each leaf of Value, in errset.Flatten's
	// leaf order, each ranked (see MaxSources). A leaf under a Wrap has
	// its own sources: the Wrap's other operands may come from elsewhere.
	Leaves [][]Source
	// Notes are the guards that might have removed the value on its way
	// here but could not be decided: the value is possibly handled.
	Notes []errset.GuardNote
}

// local returns the provenance of a value of n leaves that all have the
// single source src.
func local(n int, src Source) [][]Source {
	out := make([][]Source, n)
	for i := range out {
		out[i] = []Source{src}
	}
	return out
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

// factSet collects facts in insertion order, merging the sources of the
// leaves of equal values. A value is possibly handled only by the guards
// noted on every way it was added: one way without a note means it also
// arrives unhandled.
type factSet struct {
	facts []Fact
	index map[errset.ValueID]int
}

// add adds the value id whose leaves have the sources leaves (one list
// per leaf), possibly handled by the guards of notes.
func (fs *factSet) add(id errset.ValueID, leaves [][]Source, notes []errset.GuardNote) {
	if fs.index == nil {
		fs.index = make(map[errset.ValueID]int)
	}
	i, ok := fs.index[id]
	if !ok {
		i = len(fs.facts)
		fs.index[id] = i
		fs.facts = append(fs.facts, Fact{Value: id, Leaves: make([][]Source, len(leaves)), Notes: slices.Clone(notes)})
	} else {
		fs.facts[i].Notes = errset.IntersectNotes(fs.facts[i].Notes, notes)
	}
	f := &fs.facts[i]
	for k, srcs := range leaves {
		for _, src := range srcs {
			f.Leaves[k] = addSource(f.Leaves[k], src)
		}
	}
}
