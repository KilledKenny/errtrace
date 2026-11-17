package errset

import (
	"fmt"
	"go/token"
	"go/types"
	"slices"
	"strings"
)

// GuardKind is the kind of check a guard makes on an error value.
type GuardKind uint8

const (
	// GuardEqual is a comparison with a Sentinel: err == io.EOF (or !=,
	// or a case of a value switch).
	GuardEqual GuardKind = iota + 1
	// GuardNil is a comparison with nil: err == nil.
	GuardNil
	// GuardIs is a call errors.Is(err, S) with a Sentinel S.
	GuardIs
	// GuardAs is a call errors.As(err, &target) whose target has type
	// Type.
	GuardAs
	// GuardAssert is a comma-ok type assertion err.(Type), or a case of a
	// type switch.
	GuardAssert
	// GuardHelper is a check errtrace cannot decide: a call to a helper
	// function that takes the error (isRetryable(err)), or an errors.Is or
	// errors.As call whose target is not modelled. It never removes a
	// value.
	GuardHelper
	// GuardCompanion is a check of another result of the call that gave
	// the value, which says that the call succeeded: err == nil for an
	// error result, ok for a bool result (e, err := find(); if err != nil
	// { return err }; return e). It only exists where it holds, and
	// removes the typed nil that the callee returns together with a
	// failing value of that result (see Value.Index), and nothing else.
	GuardCompanion
)

// Guard is one check of an error value that holds, or does not hold, on
// the return path a value takes: "err == io.EOF is false here". Applying
// a guard to a value tree keeps it, removes it (only when errtrace can
// prove the check excludes it), or keeps it and marks it possibly handled.
type Guard struct {
	// Kind is the kind of check.
	Kind GuardKind
	// Site is the SSA value of the check (the comparison, the call, the
	// type assertion), for printing its span. It is not part of the
	// guard's identity.
	Site any
	// Pos is the position of the check.
	Pos token.Pos
	// Var is the Sentinel of a GuardEqual or GuardIs.
	Var *types.Var
	// Canon and Fresh are those of the Sentinel Var (see Value); they
	// follow from Var and are not part of the guard's identity.
	Canon *types.Var
	Fresh bool
	// Unstable reports that Canon is assigned outside its package's
	// initialization, so the value a Sentinel node loaded from it may not
	// be the one the check loads. It follows from Var and is not part of
	// the guard's identity.
	Unstable bool
	// Type is the target type of a GuardAs or GuardAssert, and the
	// dynamic type of the Sentinel of a GuardEqual or GuardIs when
	// errtrace knows it.
	Type types.Type
	// Detail names the helper of a GuardHelper ("call to
	// example.com/fx.isRetryable"), or why it is not modelled.
	Detail string
	// Index is the index of the checked result of a GuardCompanion.
	Index int
	// Holds reports whether the check is true on the path (the error is
	// io.EOF, errors.Is returned true, the assertion succeeded).
	Holds bool
}

// String describes the check, without its outcome: "comparison with
// io.EOF", "errors.Is with io.EOF", "errors.As to *example.com/fx.E",
// "type assertion to *example.com/fx.E", "comparison with nil", or the
// Detail of a helper guard.
func (g Guard) String() string {
	switch g.Kind {
	case GuardEqual:
		return "comparison with " + varName(g.Var)
	case GuardNil:
		return "comparison with nil"
	case GuardIs:
		return "errors.Is with " + varName(g.Var)
	case GuardAs:
		return "errors.As to " + types.TypeString(g.Type, nil)
	case GuardAssert:
		return "type assertion to " + types.TypeString(g.Type, nil)
	case GuardHelper:
		return g.Detail
	case GuardCompanion:
		return fmt.Sprintf("check of result %d of the call", g.Index)
	}
	return fmt.Sprintf("GuardKind(%d)", int(g.Kind))
}

func varName(v *types.Var) string {
	if v == nil {
		return "?"
	}
	if v.Pkg() != nil {
		return v.Pkg().Path() + "." + v.Name()
	}
	return v.Name()
}

// key returns the identity of g in comparable form.
func (g Guard) key() string {
	typ := ""
	if g.Type != nil {
		typ = types.TypeString(g.Type, nil)
	}
	return fmt.Sprintf("%d:%d:%p:%s:%s:%d:%t", g.Kind, g.Pos, g.Var, typ, g.Detail, g.Index, g.Holds)
}

func guardsKey(gs []Guard) string {
	if len(gs) == 0 {
		return ""
	}
	parts := make([]string, len(gs))
	for i, g := range gs {
		parts[i] = g.key()
	}
	return strings.Join(parts, ";")
}

// GuardNote says that an entry is possibly handled: a guard on its way to
// the Target might have removed it, but errtrace cannot decide the guard.
type GuardNote struct {
	// Site is the SSA value of the guard, for printing its span.
	Site any
	// Pos is the guard's position.
	Pos token.Pos
	// Reason says why the guard cannot be decided.
	Reason string
	// Assumed marks a note of another kind, which only exists inside
	// summaries: the value is on a path that it only takes if the guard's
	// check holds for it (the true branch of isTimeout(err), a type
	// assertion that succeeded), and errtrace cannot decide the check.
	// The value may then not be there at all, or not be of the type it
	// seems to be. An entry is not possibly handled by such a guard.
	Assumed bool
}

// NoteOf returns the GuardNote of guard g with the given reason.
func NoteOf(g Guard, reason string) GuardNote {
	return GuardNote{Site: g.Site, Pos: g.Pos, Reason: reason}
}

func noteEqual(a, b GuardNote) bool {
	return a.Pos == b.Pos && a.Reason == b.Reason && a.Assumed == b.Assumed
}

// HandledNotes returns the notes of notes that say a value is possibly
// handled: those that are not Assumed.
func HandledNotes(notes []GuardNote) []GuardNote {
	if !slices.ContainsFunc(notes, func(n GuardNote) bool { return n.Assumed }) {
		return notes
	}
	var out []GuardNote
	for _, n := range notes {
		if !n.Assumed {
			out = append(out, n)
		}
	}
	return out
}

// IsAssumed reports whether notes has an Assumed note.
func IsAssumed(notes []GuardNote) bool {
	return slices.ContainsFunc(notes, func(n GuardNote) bool { return n.Assumed })
}

// AddNotes returns notes with each of more added that is not there yet.
func AddNotes(notes []GuardNote, more ...GuardNote) []GuardNote {
	for _, n := range more {
		if !slices.ContainsFunc(notes, func(m GuardNote) bool { return noteEqual(m, n) }) {
			notes = append(notes, n)
		}
	}
	return notes
}

// IntersectNotes returns the notes of a that are also in b: a value that
// reaches a place both possibly handled and not handled at all is not
// possibly handled there.
func IntersectNotes(a, b []GuardNote) []GuardNote {
	var out []GuardNote
	for _, n := range a {
		if slices.ContainsFunc(b, func(m GuardNote) bool { return noteEqual(m, n) }) {
			out = append(out, n)
		}
	}
	return out
}

// CountPossiblyHandled returns how many entries are marked possibly
// handled.
func CountPossiblyHandled(entries []Entry) int {
	n := 0
	for _, e := range entries {
		if len(e.PossiblyHandled) > 0 {
			n++
		}
	}
	return n
}
