// Package errset is errtrace's data model: the interned abstract error
// values a Query works with (Error Origins, Wraps, Unresolved places and the
// symbolic values that only live inside summaries), the Error Set entries
// that flattening a value tree yields, their Propagation Paths, and the
// deterministic order in which entries are reported. It contains no SSA
// logic.
package errset

import (
	"fmt"
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"
)

// ValueID names an interned Value in a Table. Equal values have equal IDs.
type ValueID int32

// Kind is the kind of an abstract error value.
type Kind uint8

const (
	// Sentinel is an Error Origin: a package-level error variable returned
	// by identity (io.EOF).
	Sentinel Kind = iota + 1
	// Concrete is an Error Origin: a value of a named type that implements
	// error, created at a known place (*fs.PathError).
	Concrete
	// Opaque is an Error Origin with no identity and no inspectable type
	// (errors.New).
	Opaque
	// Wrap is a node of a Wrap Chain: an error that keeps its Inner errors
	// reachable through errors.Is and errors.As.
	Wrap
	// Truncated marks a Wrap Chain that was cut at the chain depth limit.
	Truncated
	// Unresolved is a place the analysis could not see through. It is
	// never dropped from an Error Set.
	Unresolved
	// Param is symbolic: whatever the caller passed as parameter Index of
	// function Fn. It only exists inside summaries.
	Param
	// FreeVar is symbolic: the variable captured by closure Fn as free
	// variable Index.
	FreeVar
	// CallRef is symbolic: result Index of the call at Site, to be replaced
	// by the callee's summary.
	CallRef
	// ErrorfLike is symbolic: the result of a fmt.Errorf call in function
	// Fn whose format is Fn's parameter Index and whose operands are the
	// variadic parameter ArgIndex (a forwarded format, as in
	// wrapf(format string, args ...any)). Each caller evaluates it against
	// its own arguments into a Wrap or an Opaque Error.
	ErrorfLike
)

var kindNames = [...]string{
	Sentinel:   "sentinel",
	Concrete:   "concrete",
	Opaque:     "opaque",
	Wrap:       "wrap",
	Truncated:  "truncated",
	Unresolved: "unresolved",
	Param:      "param",
	FreeVar:    "freevar",
	CallRef:    "callref",
	ErrorfLike: "errorf-like",
}

// String returns the lower-case name of k.
func (k Kind) String() string {
	if int(k) < len(kindNames) && kindNames[k] != "" {
		return kindNames[k]
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// IsOrigin reports whether k is an Error Origin kind.
func (k Kind) IsOrigin() bool { return k == Sentinel || k == Concrete || k == Opaque }

// IsLeaf reports whether a value of kind k is a leaf of a value tree: an
// Error Origin, an Unresolved place, or (only inside summaries) a symbolic
// value.
func (k Kind) IsLeaf() bool { return k != Wrap && k != Truncated }

// UnresolvedKind says why a value is Unresolved. The set is closed.
type UnresolvedKind string

const (
	// NoBody is a call to a function without a Go body (assembly,
	// //go:linkname, cgo, or no syntax loaded).
	NoBody UnresolvedKind = "no-body"
	// NoCandidates is a dynamic call for which no Candidate was found.
	NoCandidates UnresolvedKind = "no-candidates"
	// Memory is an error loaded from a struct field, a map, slice or array
	// element, a pointer that isn't a local variable, or a channel.
	Memory UnresolvedKind = "memory"
	// DependsOnCaller is a parameter or captured variable that reaches the
	// top of a Query: its errors depend on the caller.
	DependsOnCaller UnresolvedKind = "depends-on-caller"
	// DynamicFormat is a fmt.Errorf whose format is neither a constant nor
	// a format forwarded from a caller, so which operands it wraps is not
	// known.
	DynamicFormat UnresolvedKind = "dynamic-format"
	// FixpointLimit is a summary that did not converge within the
	// iteration cap of its SCC, or an error result whose set of values
	// passed the size cap.
	FixpointLimit UnresolvedKind = "fixpoint-limit"
)

// WrapKind says how a Wrap was made.
type WrapKind string

const (
	// WrapErrorf is a fmt.Errorf with one or more %w verbs.
	WrapErrorf WrapKind = "errorf"
	// WrapJoin is errors.Join.
	WrapJoin WrapKind = "join"
	// WrapUnwrapMethod is a value of a type with a custom Unwrap method.
	WrapUnwrapMethod WrapKind = "unwrap-method"
	// WrapBraces is braces.dev/errtrace.Wrap.
	WrapBraces WrapKind = "braces-wrap"
)

// Value is one abstract error value. Which fields are set depends on Kind;
// the others are zero. Values are immutable once interned: Inner must not
// be modified.
type Value struct {
	Kind Kind

	// Site is where the value is made, for printing its span: the
	// *types.Var of a Sentinel, and the SSA value or instruction that
	// creates the other kinds. It is opaque to this package and is not
	// part of the value's identity: of two values that differ only in Site,
	// the first one interned is kept.
	Site any
	// Pos is the source position of the value, part of its identity (two
	// Concrete Type Origins of the same type made at different places are
	// different Origins).
	Pos token.Pos

	// Var is the variable of a Sentinel.
	Var *types.Var
	// Type is the type of a Concrete Type Origin or of a pointer Sentinel,
	// the wrapper type of a custom-Unwrap Wrap, and the type of a Param or
	// FreeVar.
	Type types.Type
	// TypedNil marks a Concrete Type Origin that is a nil pointer in a
	// non-nil interface.
	TypedNil bool

	// Constructor is the function that made an Opaque Error
	// ("errors.New"), or empty if unknown.
	Constructor string
	// Message is the constant message of an Opaque Error, if known.
	Message string
	// HasMessage reports whether Message is a known constant.
	HasMessage bool

	// WrapKind is the kind of a Wrap.
	WrapKind WrapKind
	// Format is the format string of an errorf Wrap. It is empty when the
	// format is not a constant (dynamic-format): the Wrap then holds the
	// Unresolved dynamic-format value and the operands it may wrap.
	Format string
	// Inner are the wrapped values of a Wrap, or the single value under a
	// Truncated marker.
	Inner []ValueID

	// Unresolved is the kind of an Unresolved value.
	Unresolved UnresolvedKind
	// Detail describes an Unresolved value, or the callee of a CallRef.
	Detail string

	// Fn is the SSA name of the function a Param or FreeVar belongs to.
	Fn string
	// Index is the parameter index of a Param, the free variable index of
	// a FreeVar, the result index of a CallRef, or the format parameter
	// index of an ErrorfLike.
	Index int
	// ArgIndex is the index of the variadic operands parameter of an
	// ErrorfLike.
	ArgIndex int
	// Name is the source name of a Param or FreeVar.
	Name string
}

// key is the identity of a Value: every field except Site, in comparable
// form.
type key struct {
	kind        Kind
	pos         token.Pos
	v           *types.Var
	typ         string
	typedNil    bool
	constructor string
	message     string
	hasMessage  bool
	wrapKind    WrapKind
	format      string
	inner       string
	unresolved  UnresolvedKind
	detail      string
	fn          string
	index       int
	argIndex    int
	name        string
}

func keyOf(v Value) key {
	k := key{
		kind:        v.Kind,
		pos:         v.Pos,
		v:           v.Var,
		typedNil:    v.TypedNil,
		constructor: v.Constructor,
		message:     v.Message,
		hasMessage:  v.HasMessage,
		wrapKind:    v.WrapKind,
		format:      v.Format,
		unresolved:  v.Unresolved,
		detail:      v.Detail,
		fn:          v.Fn,
		index:       v.Index,
		argIndex:    v.ArgIndex,
		name:        v.Name,
	}
	if v.Type != nil {
		k.typ = types.TypeString(v.Type, nil)
	}
	if len(v.Inner) > 0 {
		var b strings.Builder
		for i, id := range v.Inner {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Itoa(int(id)))
		}
		k.inner = b.String()
	}
	return k
}

// Table interns Values: it hash-conses them, so that equal values share a
// ValueID and set operations and fixpoint checks compare small integers. A
// Table is not safe for concurrent use.
type Table struct {
	values []Value
	// leaves is the number of leaves of each value's tree.
	leaves []int
	index  map[key]ValueID
}

// NewTable returns an empty Table.
func NewTable() *Table {
	return &Table{index: make(map[key]ValueID)}
}

// Intern returns the ValueID of v, adding v to the table if no equal value
// is there yet. It panics if v has no Kind or refers to an unknown ValueID.
func (t *Table) Intern(v Value) ValueID {
	if v.Kind == 0 {
		panic("errset: Intern of a Value with no Kind")
	}
	for _, id := range v.Inner {
		if id < 0 || int(id) >= len(t.values) {
			panic(fmt.Sprintf("errset: Intern of a Value with unknown inner ValueID %d", id))
		}
	}
	k := keyOf(v)
	if id, ok := t.index[k]; ok {
		return id
	}
	id := ValueID(len(t.values))
	v.Inner = append([]ValueID(nil), v.Inner...)
	n := 0
	for _, in := range v.Inner {
		n += t.leaves[in]
	}
	if len(v.Inner) == 0 {
		n = 1
	}
	t.values = append(t.values, v)
	t.leaves = append(t.leaves, n)
	t.index[k] = id
	return id
}

// Get returns the Value named by id. It panics if id is not in the table.
func (t *Table) Get(id ValueID) Value { return t.values[id] }

// Leaves returns the number of leaves of the value tree id: the entries
// Flatten makes of it. A leaf value has one; a Wrap has the sum of its
// inner values' leaves; a Truncated marker has its inner value's.
func (t *Table) Leaves(id ValueID) int { return t.leaves[id] }

// Len returns the number of distinct values in the table.
func (t *Table) Len() int { return len(t.values) }

// String returns a short description of the value id, without positions,
// for tests, debugging and as an ordering tie-break.
func (t *Table) String(id ValueID) string {
	v := t.Get(id)
	switch v.Kind {
	case Sentinel:
		if v.Var.Pkg() != nil {
			return "sentinel " + v.Var.Pkg().Path() + "." + v.Var.Name()
		}
		return "sentinel " + v.Var.Name()
	case Concrete:
		s := "concrete " + types.TypeString(v.Type, nil)
		if v.TypedNil {
			s += " [typed nil]"
		}
		return s
	case Opaque:
		s := "opaque"
		if v.Constructor != "" {
			s += " " + v.Constructor
		}
		if v.HasMessage {
			s += " " + strconv.Quote(v.Message)
		}
		return s
	case Wrap:
		parts := make([]string, len(v.Inner))
		for i, in := range v.Inner {
			parts[i] = t.String(in)
		}
		return fmt.Sprintf("wrap(%s %s)[%s]", v.WrapKind, t.WrapDescription(id), strings.Join(parts, "; "))
	case Truncated:
		return "truncated[" + t.String(v.Inner[0]) + "]"
	case Unresolved:
		return fmt.Sprintf("unresolved(%s): %s", v.Unresolved, v.Detail)
	case Param:
		return fmt.Sprintf("param %s#%d %s", v.Fn, v.Index, v.Name)
	case FreeVar:
		return fmt.Sprintf("freevar %s#%d %s", v.Fn, v.Index, v.Name)
	case CallRef:
		return fmt.Sprintf("callref %s #%d", v.Detail, v.Index)
	case ErrorfLike:
		return fmt.Sprintf("errorf-like %s format#%d args#%d", v.Fn, v.Index, v.ArgIndex)
	}
	return v.Kind.String()
}

// WrapDescription returns how the Wrap id is described in a Wrap Chain:
// fmt.Errorf "<fmt>", errors.Join, <type>.Unwrap or errtrace.Wrap (and
// "fmt.Errorf with dynamic format" for a format that is not constant).
func (t *Table) WrapDescription(id ValueID) string {
	v := t.Get(id)
	switch v.WrapKind {
	case WrapErrorf:
		if v.Format == "" {
			return "fmt.Errorf with dynamic format"
		}
		return "fmt.Errorf " + strconv.Quote(v.Format)
	case WrapJoin:
		return "errors.Join"
	case WrapUnwrapMethod:
		if v.Type != nil {
			return types.TypeString(v.Type, nil) + ".Unwrap"
		}
		return "Unwrap"
	case WrapBraces:
		return "errtrace.Wrap"
	}
	return string(v.WrapKind)
}

// Cap limits the Wrap Chains of the value tree id to at most limit Wraps:
// a Wrap that would be the (limit+1)th on the way from the top down to a
// leaf is replaced by a Truncated marker over each leaf below it. Truncated
// markers are kept directly above leaves. The result is usually the single
// rebuilt value; it has several values only when id itself is cut (limit
// 0). Interning keeps capped trees finite, so a fixpoint over them
// terminates.
func (t *Table) Cap(id ValueID, limit int) []ValueID {
	capped := t.CapLeaves(id, limit)
	out := make([]ValueID, len(capped))
	for i, c := range capped {
		out[i] = c.Value
	}
	return out
}

// Capped is one value made by CapLeaves.
type Capped struct {
	// Value is the capped value tree.
	Value ValueID
	// From maps each leaf of Value, in leaf order, to the index of the
	// leaf of the original tree it is.
	From []int
}

// CapLeaves is Cap, which also says where each leaf of the capped values
// was in the original tree, so per-leaf information follows the leaves.
// Of equal leaves or inner values merged by capping, the first is kept.
func (t *Table) CapLeaves(id ValueID, limit int) []Capped {
	return t.capAt(id, 0, limit, 0)
}

func (t *Table) capAt(id ValueID, level, limit, base int) []Capped {
	v := t.Get(id)
	switch v.Kind {
	case Wrap:
		if level >= limit {
			return t.truncateLeaves(id, base)
		}
		var inner []Capped
		changed := false
		off := base
		for _, in := range v.Inner {
			c := t.capAt(in, level+1, limit, off)
			off += t.leaves[in]
			changed = changed || len(c) != 1 || c[0].Value != in
			inner = append(inner, c...)
		}
		if !changed {
			return []Capped{{Value: id, From: seq(base, t.leaves[id])}}
		}
		var from []int
		v.Inner = nil
		for _, c := range inner {
			if slices.Contains(v.Inner, c.Value) {
				continue
			}
			v.Inner = append(v.Inner, c.Value)
			from = append(from, c.From...)
		}
		return []Capped{{Value: t.Intern(v), From: from}}
	case Truncated:
		if t.Get(v.Inner[0]).Kind.IsLeaf() {
			return []Capped{{Value: id, From: []int{base}}}
		}
		return t.truncateLeaves(v.Inner[0], base)
	}
	return []Capped{{Value: id, From: []int{base}}}
}

// seq returns n consecutive indices from base.
func seq(base, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = base + i
	}
	return out
}

// truncateLeaves returns a Truncated marker over each distinct leaf of the
// tree id, whose first leaf has index base.
func (t *Table) truncateLeaves(id ValueID, base int) []Capped {
	var out []Capped
	seen := make(map[ValueID]bool)
	next := base
	var walk func(ValueID)
	walk = func(id ValueID) {
		v := t.Get(id)
		if v.Kind.IsLeaf() {
			if !seen[id] {
				seen[id] = true
				out = append(out, Capped{
					Value: t.Intern(Value{Kind: Truncated, Site: v.Site, Pos: v.Pos, Inner: []ValueID{id}}),
					From:  []int{next},
				})
			}
			next++
			return
		}
		for _, in := range v.Inner {
			walk(in)
		}
	}
	walk(id)
	return out
}
