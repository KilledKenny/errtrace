package mark

import (
	"fmt"
	"testing"
)

// The operands of TestFormatNames record the methods package fmt calls
// on them.

type calls struct{ got opNames }

type errOnly struct{ c *calls }

func (e errOnly) Error() string { e.c.got |= opError; return "e" }

type strOnly struct{ c *calls }

func (s strOnly) String() string { s.c.got |= opString; return "s" }

type goStr struct{ c *calls }

func (g goStr) Error() string    { g.c.got |= opError; return "g" }
func (g goStr) GoString() string { g.c.got |= opGoString; return "g" }

type formatter struct{ c *calls }

func (f formatter) Error() string          { f.c.got |= opError; return "f" }
func (f formatter) Format(fmt.State, rune) { f.c.got |= opFormat }
func (f formatter) GoString() string       { f.c.got |= opGoString; return "f" }
func (f formatter) String() string         { f.c.got |= opString; return "f" }
func (f formatter) Unwrap() error          { f.c.got |= opChain; return nil }
func (f formatter) Is(error) bool          { f.c.got |= opChain; return false }
func (f formatter) As(any) bool            { f.c.got |= opChain; return false }
func (e errOnly) Unwrap() error            { e.c.got |= opChain; return nil }
func (e errOnly) Is(error) bool            { e.c.got |= opChain; return false }
func (e errOnly) As(any) bool              { e.c.got |= opChain; return false }
func (g goStr) Unwrap() error              { g.c.got |= opChain; return nil }
func (g goStr) Is(error) bool              { g.c.got |= opChain; return false }
func (g goStr) As(any) bool                { g.c.got |= opChain; return false }
func (s strOnly) Unwrap() error            { s.c.got |= opChain; return nil }
func (s strOnly) Is(error) bool            { s.c.got |= opChain; return false }
func (s strOnly) As(any) bool              { s.c.got |= opChain; return false }

// TestFormatNames checks formatNames against package fmt: whatever
// fmt.Errorf, and errors.Is, errors.As and errors.Unwrap on the error it
// returns, call on an operand is in the set formatNames gives for it.
func TestFormatNames(t *testing.T) {
	formats := []string{
		"%v", "%+v", "%#v", "%s", "%x", "%X", "%q", "%d", "%t", "%T", "%p", "%w", "%#w", "%+w",
		"%5v", "%.2s", "%-8q", "%#x", "% x", "%08d", "%#s", "%#q", "%+s", "%c", "%U", "%e", "%z", "%!",
		"%v %v", "%d %d", "%T %p", "%p %T %v", "%w %w", "%d %w", "%w %d", "%T %w",
		"%[1]d %[1]v", "%[2]d %[1]T", "%[2]v", "%[2]T", "%[1]T %[1]p", "%[3]v", "%[x]v", "%[1]", "%[2]*[1]d",
		"%*d", "%.*d", "%*v", "%*T %T", "%[1]*d", "%d", "%T", "", "no verbs", "%%", "%% %T", "%T %%d",
		"%", "%T %", "%v %", "%T %T %T", "%T%T", "%!T", "%-T", "%#T", "%#p", "%10p", "%[1]2T", "%[1].2T",
		"%T %[5]T", "%[2]T %T", "%é", "%T é %T",
	}
	makers := []func(c *calls) any{
		func(c *calls) any { return errOnly{c} },
		func(c *calls) any { return &errOnly{c} },
		func(c *calls) any { return strOnly{c} },
		func(c *calls) any { return goStr{c} },
		func(c *calls) any { return formatter{c} },
		func(c *calls) any { return &formatter{c} },
	}
	for _, format := range formats {
		for n := 1; n <= 3; n++ {
			names := formatNames(format, n)
			for mi, mk := range makers {
				cs := make([]*calls, n)
				args := make([]any, n)
				for i := range args {
					cs[i] = new(calls)
					args[i] = mk(cs[i])
				}
				err := fmt.Errorf(format, args...)
				_ = err.Error()
				// What the returned error hands on to what it holds.
				for e := error(err); e != nil; {
					switch u := e.(type) {
					case interface{ Unwrap() error }:
						e = u.Unwrap()
						if e != nil {
							callChain(e)
						}
					case interface{ Unwrap() []error }:
						for _, x := range u.Unwrap() {
							callChain(x)
						}
						e = nil
					default:
						e = nil
					}
				}
				for i, c := range cs {
					if c.got&^names[i] != 0 {
						t.Errorf("fmt.Errorf(%q) with %d operands of kind %d: methods %05b are called on operand %d, formatNames says %05b", format, n, mi, c.got, i, names[i])
					}
				}
			}
		}
	}
}

// callChain calls on e what errors.Is, errors.As and errors.Unwrap call
// on an error in a Wrap Chain.
func callChain(e error) {
	if x, ok := e.(interface{ Is(error) bool }); ok {
		x.Is(nil)
	}
	if x, ok := e.(interface{ As(any) bool }); ok {
		x.As(nil)
	}
	if x, ok := e.(interface{ Unwrap() error }); ok {
		x.Unwrap()
	}
}

// TestFormatNamesPrecise pins the operands that formatNames clears.
func TestFormatNamesPrecise(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   []opNames
	}{
		{"%d", []opNames{opFormat}},
		{"%T %p", []opNames{0, 0}},
		{"%v", []opNames{opFormat | opError | opString}},
		{"%#v %x", []opNames{opFormat | opGoString, opFormat | opError | opString}},
		{"%w: %d", []opNames{opAll, opFormat}},
		{"%d", []opNames{opFormat, opAll}},
		{"%[1]T %[1]s %T", []opNames{opFormat | opError | opString, 0}},
		{"%T %[3]v", []opNames{opAll, opAll}},
	} {
		got := formatNames(tc.format, len(tc.want))
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("formatNames(%q, %d)[%d] = %05b, want %05b", tc.format, len(tc.want), i, got[i], tc.want[i])
			}
		}
	}
}
