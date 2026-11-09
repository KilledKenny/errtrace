package fmtverb

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

// runtimeWrapped returns the indexes of the operands that fmt.Errorf
// actually wraps for format and args, found by unwrapping its result.
func runtimeWrapped(format string, args []any) []int {
	err := fmt.Errorf(format, args...)
	var inner []error
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		inner = []error{u.Unwrap()}
	case interface{ Unwrap() []error }:
		inner = u.Unwrap()
	}
	var out []int
	for _, e := range inner {
		for i, a := range args {
			if ae, ok := a.(error); ok && ae == e {
				out = append(out, i)
			}
		}
	}
	slices.Sort(out)
	return out
}

// errs returns n distinct error operands.
func errs(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = errors.New(fmt.Sprint("e", i))
	}
	return out
}

func TestWrapped(t *testing.T) {
	for _, tc := range []struct {
		format string
		args   []any
		want   []int
	}{
		{"%w", errs(1), []int{0}},
		{"%w and %w", errs(2), []int{0, 1}},
		{"%[2]w then %[1]v", errs(2), []int{1}},
		{"%%w", errs(1), nil},
		{"%*d %w", append([]any{5, 7}, errs(1)...), []int{2}},
		{"%v %w", errs(2), []int{1}},
		{"%[3]w %w", errs(4), []int{2, 3}},
		{"%[x]w", errs(1), nil},
		{"%[2]w %[2]w %[1]w", errs(2), []int{0, 1}},
		{"%[5]w", errs(2), nil},
		{"%w %w", errs(1), []int{0}},
		{"%-+#0 w", errs(1), []int{0}},
		{"%.*w", append([]any{3}, errs(1)...), []int{1}},
		{"%[2]*[1]w", append(errs(1), 4), []int{0}},
		{"%[2]3w", errs(2), nil},
		{"%[1].2w", errs(1), nil},
		{"x: %w%", errs(1), []int{0}},
		{"no verbs", nil, nil},
		{"%d: %w", append([]any{1}, errs(1)...), []int{1}},
	} {
		got := Wrapped(Parse(tc.format, len(tc.args)))
		if !slices.Equal(got, tc.want) {
			t.Errorf("Wrapped(Parse(%q, %d)) = %v, want %v", tc.format, len(tc.args), got, tc.want)
		}
		if rt := runtimeWrapped(tc.format, tc.args); !slices.Equal(rt, tc.want) {
			t.Errorf("fmt.Errorf(%q) wraps %v at run time, the table says %v", tc.format, rt, tc.want)
		}
	}
}

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		format  string
		numArgs int
		want    []Verb
	}{
		{"%%w", 1, []Verb{{Verb: '%', Arg: -1, Start: 0, End: 2}}},
		{"%*d %w", 3, []Verb{{Verb: 'd', Arg: 1, Start: 0, End: 3}, {Verb: 'w', Arg: 2, Start: 4, End: 6}}},
		{"%[3]w %w", -1, []Verb{{Verb: 'w', Arg: 2, Start: 0, End: 5}, {Verb: 'w', Arg: 3, Start: 6, End: 8}}},
		{"%[x]w", 1, []Verb{{Verb: 'w', Arg: -1, BadIndex: true, Start: 0, End: 5}}},
		{"%w", 0, []Verb{{Verb: 'w', Arg: -1, Missing: true, Start: 0, End: 2}}},
		{"a %", 0, []Verb{{Verb: 0xFFFD, Arg: -1, NoVerb: true, Start: 2, End: 3}}},
		{"%é", 1, []Verb{{Verb: 'é', Arg: 0, Start: 0, End: 3}}},
	} {
		if got := Parse(tc.format, tc.numArgs); !slices.Equal(got, tc.want) {
			t.Errorf("Parse(%q, %d) =\n\t%+v\nwant\n\t%+v", tc.format, tc.numArgs, got, tc.want)
		}
	}
}
