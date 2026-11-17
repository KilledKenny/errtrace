package errset

import (
	"cmp"
	"fmt"
	"go/types"
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
	// Repeats reports that the leaf went around a cycle of stores (a
	// variable or field that is stored a Wrap of itself): between the
	// leaf and the Wrap Chain shown there may be any number of further
	// Wraps, those shown among them.
	Repeats bool
	// ViaLocation is set if every way the leaf reaches the Target goes
	// through a field location: it was loaded from a struct field whose
	// value errtrace knows only as "what the program stores to that field
	// of that type, anywhere". It names the field ("field Err of
	// io/fs.PathError"). Such an entry is an over-approximation: the
	// struct that was loaded from may never hold this value.
	ViaLocation string
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
	var walk func(id ValueID, outer []ValueID, truncated, repeats bool)
	walk = func(id ValueID, outer []ValueID, truncated, repeats bool) {
		v := t.Get(id)
		switch v.Kind {
		case Wrap:
			next := append(outer[:len(outer):len(outer)], id)
			for _, in := range v.Inner {
				walk(in, next, truncated, repeats)
			}
		case Truncated:
			walk(v.Inner[0], outer, truncated || !v.Repeat, repeats || v.Repeat)
		case Guarded, FieldOf:
			walk(v.Inner[0], outer, truncated, repeats)
		default:
			chain := make([]ValueID, len(outer))
			for i, w := range outer {
				chain[len(outer)-1-i] = w
			}
			out = append(out, Entry{Leaf: id, Chain: chain, Truncated: truncated, Repeats: repeats})
		}
	}
	walk(top, nil, false, false)
	return out
}

// Dedup merges entries with the same leaf, Wrap Chain and truncation,
// compared by what is shown of them. Wrap Chains are compared by what is
// shown of each Wrap (its place, kind, format and type), not by its
// wrapped values: the same leaf under one errors.Join is one entry
// whatever the Join's other operands are. Leaves are compared by their
// description and, if pos is not nil, the position it gives them: two
// leaves that an Error Set would print as the same line (two values of
// one Concrete Type that have no position of their own, say) are one
// entry. With a nil pos, leaves are compared by identity. The first
// entry's Path and Chain are kept; every other distinct Path adds to
// MorePaths. A merged entry is via a field location only if every one of
// its entries is. A merged entry is possibly handled only by the guards that
// every one of its entries is possibly handled by. The order of first
// occurrences is kept. A leaf that is shown under the Wrap it is the
// nil-inner form of is first taken out of that Wrap (see unnest).
func Dedup(t *Table, entries []Entry, pos PosFunc) []Entry {
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
	// leafShown numbers the distinct shown forms of leaves.
	leaves := make(map[string]int)
	leafShown := make(map[ValueID]int)
	leafOf := func(l ValueID) int {
		if pos == nil {
			return int(l)
		}
		if n, ok := leafShown[l]; ok {
			return n
		}
		file, offset := pos(l)
		k := fmt.Sprintf("%s\x00%d\x00%s", file, offset, t.String(l))
		n, ok := leaves[k]
		if !ok {
			n = len(leaves)
			leaves[k] = n
		}
		leafShown[l] = n
		return n
	}
	var groups []*group
	index := make(map[string]*group)
	var b strings.Builder
	for _, e := range entries {
		e = t.unnest(e)
		b.Reset()
		fmt.Fprintf(&b, "%d %t %t", leafOf(e.Leaf), e.Truncated, e.Repeats)
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
		if e.ViaLocation == "" {
			// One way that does not go through a field location: the
			// entry is not location-derived.
			g.entry.ViaLocation = ""
		}
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

// unnest returns e without the innermost Wrap of its Wrap Chain if the
// leaf is that very Wrap wrapping nothing, read back around a cycle of
// stores (err = fmt.Errorf("x: %w", err) with err nil at first: the
// Opaque Error of the fmt.Errorf, under the same fmt.Errorf, chain may
// repeat). The entry then shows the leaf once, as what it is; that it may
// also be nested in itself says nothing errors.Is or errors.As could
// tell. The values themselves keep the nesting: unwrapping one gives the
// leaf.
func (t *Table) unnest(e Entry) Entry {
	if !e.Repeats || e.Truncated || len(e.Chain) == 0 {
		return e
	}
	l, w := t.Get(e.Leaf), t.Get(e.Chain[0])
	if w.Kind != Wrap || w.Pos != l.Pos {
		return e
	}
	switch {
	case w.WrapKind == WrapErrorf && l.Kind == Opaque && l.Constructor == "fmt.Errorf" && l.Message == w.Format && w.Format != "":
	case w.WrapKind == WrapUnwrapMethod && l.Kind == Concrete && l.Allocated && !l.TypedNil && l.Type != nil && w.Type != nil && types.Identical(l.Type, w.Type):
	default:
		return e
	}
	e.Chain = e.Chain[1:]
	e.Repeats = false
	return e
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
		case a.Truncated != b.Truncated:
			if !a.Truncated {
				return -1
			}
			return 1
		case a.Repeats != b.Repeats:
			if !a.Repeats {
				return -1
			}
			return 1
		}
		return 0
	})
}

// CountViaLocation returns how many entries are via a field location.
func CountViaLocation(entries []Entry) int {
	n := 0
	for _, e := range entries {
		if e.ViaLocation != "" {
			n++
		}
	}
	return n
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
