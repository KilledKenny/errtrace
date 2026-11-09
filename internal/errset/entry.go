package errset

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// Hop is one step of a Propagation Path. Hop 0 is the instruction where the
// leaf value enters the innermost function; each later hop is a call in a
// caller, up to the Target.
type Hop struct {
	// Site is the SSA value or instruction of the hop, for printing its
	// span. It is opaque to this package and compared by identity.
	Site any
	// Func is the SSA name of the function that contains Site.
	Func string
	// Callee is the SSA name of the function called at a call hop, or
	// empty for hop 0.
	Callee string
	// Dynamic reports whether a call hop is a dynamic call.
	Dynamic bool
	// Method names what a dynamic call hop calls through: the interface
	// method ((io.Reader).Read) or the function value.
	Method string
	// Candidates is how many Candidates a dynamic call hop has, and
	// CandidatesBefore how many it had before narrowing.
	Candidates, CandidatesBefore int
}

// Path is a Propagation Path, from the Error Origin (or Unresolved place)
// up to the Target.
type Path []Hop

// Entry is one line of an Error Set: a leaf (an Error Origin or an
// Unresolved place) with the Wrap Chain that leads from it to the top-level
// value, and one Propagation Path.
type Entry struct {
	// Leaf is the Error Origin or Unresolved value.
	Leaf ValueID
	// Chain are the Wrap values from the leaf outwards to the top-level
	// value, innermost first.
	Chain []ValueID
	// Truncated reports whether the Wrap Chain was cut at the chain depth
	// limit.
	Truncated bool
	// Path is the Propagation Path shown for the entry.
	Path Path
	// MorePaths is how many other distinct Propagation Paths were found.
	MorePaths int
	// PossiblyHandled are the guards that might have removed the entry on
	// its way to the Target but that errtrace cannot decide.
	PossiblyHandled []GuardNote
}

// Flatten turns the value tree rooted at top into entries, one per leaf,
// each with its Wrap Chain. The entries are in leaf order (inner values
// left to right), so entry k is the leaf k whose Path the caller fills in;
// there are t.Leaves(top) of them.
func Flatten(t *Table, top ValueID) []Entry {
	out := make([]Entry, 0, t.Leaves(top))
	var walk func(id ValueID, outer []ValueID, truncated bool)
	walk = func(id ValueID, outer []ValueID, truncated bool) {
		v := t.Get(id)
		switch v.Kind {
		case Wrap:
			next := append(outer[:len(outer):len(outer)], id)
			for _, in := range v.Inner {
				walk(in, next, truncated)
			}
		case Truncated:
			walk(v.Inner[0], outer, true)
		case Guarded:
			walk(v.Inner[0], outer, truncated)
		default:
			chain := make([]ValueID, len(outer))
			for i, w := range outer {
				chain[len(outer)-1-i] = w
			}
			out = append(out, Entry{Leaf: id, Chain: chain, Truncated: truncated})
		}
	}
	walk(top, nil, false)
	return out
}

// Dedup merges entries with the same leaf, Wrap Chain and truncation. Wrap
// Chains are compared by what is shown of each Wrap (its place, kind,
// format and type), not by its wrapped values: the same leaf under one
// errors.Join is one entry whatever the Join's other operands are. The
// first entry's Path and Chain are kept; every other distinct Path adds to
// MorePaths. A merged entry is possibly handled only by the guards that
// every one of its entries is possibly handled by. The order of first
// occurrences is kept.
func Dedup(t *Table, entries []Entry) []Entry {
	type group struct {
		entry Entry
		paths []Path
	}
	// shown numbers the distinct shown forms of Wraps; wrapShown caches
	// the number of each Wrap.
	shown := make(map[key]int)
	wrapShown := make(map[ValueID]int)
	shownOf := func(w ValueID) int {
		if n, ok := wrapShown[w]; ok {
			return n
		}
		v := t.Get(w)
		v.Inner, v.MayHoldNil = nil, false
		k := keyOf(v)
		n, ok := shown[k]
		if !ok {
			n = len(shown)
			shown[k] = n
		}
		wrapShown[w] = n
		return n
	}
	var groups []*group
	index := make(map[string]*group)
	var b strings.Builder
	for _, e := range entries {
		b.Reset()
		fmt.Fprintf(&b, "%d %t", e.Leaf, e.Truncated)
		for _, w := range e.Chain {
			fmt.Fprintf(&b, " %d", shownOf(w))
		}
		g := index[b.String()]
		if g == nil {
			g = &group{entry: e, paths: []Path{e.Path}}
			groups = append(groups, g)
			index[b.String()] = g
			continue
		}
		g.entry.MorePaths += e.MorePaths
		g.entry.PossiblyHandled = IntersectNotes(g.entry.PossiblyHandled, e.PossiblyHandled)
		if !slices.ContainsFunc(g.paths, func(p Path) bool { return slices.Equal(p, e.Path) }) {
			g.paths = append(g.paths, e.Path)
			g.entry.MorePaths++
		}
	}
	out := make([]Entry, len(groups))
	for i, g := range groups {
		out[i] = g.entry
	}
	return out
}

// PosFunc returns the position of a value's span start as a file name and
// byte offset, for ordering. Values with no position return "" and -1.
type PosFunc func(ValueID) (file string, offset int)

// kindRank orders leaf kinds: Sentinel, Concrete Type, Opaque Error,
// Unresolved, then symbolic values.
func kindRank(k Kind) int {
	switch k {
	case Sentinel:
		return 0
	case Concrete:
		return 1
	case Opaque:
		return 2
	case Unresolved:
		return 3
	}
	return 4
}

// Sort orders entries deterministically by leaf kind (Sentinel, Concrete
// Type, Opaque Error, Unresolved), then by the leaf's position, then by the
// Wrap Chain (positions and descriptions), then by description.
func Sort(t *Table, entries []Entry, pos PosFunc) {
	cmpPos := func(a, b ValueID) int {
		fa, oa := pos(a)
		fb, ob := pos(b)
		return cmp.Or(cmp.Compare(fa, fb), cmp.Compare(oa, ob))
	}
	slices.SortStableFunc(entries, func(a, b Entry) int {
		la, lb := t.Get(a.Leaf), t.Get(b.Leaf)
		if c := cmp.Compare(kindRank(la.Kind), kindRank(lb.Kind)); c != 0 {
			return c
		}
		if c := cmpPos(a.Leaf, b.Leaf); c != 0 {
			return c
		}
		for i := 0; i < len(a.Chain) && i < len(b.Chain); i++ {
			if c := cmpPos(a.Chain[i], b.Chain[i]); c != 0 {
				return c
			}
			if c := cmp.Compare(t.WrapDescription(a.Chain[i]), t.WrapDescription(b.Chain[i])); c != 0 {
				return c
			}
		}
		if c := cmp.Compare(len(a.Chain), len(b.Chain)); c != 0 {
			return c
		}
		if c := cmp.Compare(t.String(a.Leaf), t.String(b.Leaf)); c != 0 {
			return c
		}
		switch {
		case a.Truncated == b.Truncated:
			return 0
		case !a.Truncated:
			return -1
		}
		return 1
	})
}

// CountUnresolved returns how many entries have an Unresolved leaf.
func CountUnresolved(t *Table, entries []Entry) int {
	n := 0
	for _, e := range entries {
		if t.Get(e.Leaf).Kind == Unresolved {
			n++
		}
	}
	return n
}
