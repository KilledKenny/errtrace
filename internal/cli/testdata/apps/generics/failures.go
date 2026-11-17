package main

import (
	"errors"
	"fmt"

	"example.com/generics/errs"
)

// classify returns fresh generic error values and the per-instantiation
// sentinels.
func /*@classify*/classify(n int, s string) error {
	switch {
	case n > 100:
		return &errs.TypedErr[int]{Op: "classify", Val: n}
	case s == "":
		return &errs.TypedErr[string]{Op: "classify", Val: s}
	case n < 0:
		return errs.ErrIntRange
	case len(s) > 9:
		return errs.ErrStrRange
	}
	return nil
}

// onlyStrings drops every *TypedErr[int] with errors.As.
func /*@onlystrings*/onlyStrings(n int, s string) error {
	err := classify(n, s)
	var ie *errs.TypedErr[int]
	if errors.As(err, &ie) {
		return nil
	}
	return err
}

// notRange drops only the ErrIntRange sentinel with errors.Is.
func /*@notrange*/notRange(n int, s string) error {
	err := classify(n, s)
	if errors.Is(err, errs.ErrIntRange) {
		return nil
	}
	return err
}

// eqRange drops the ErrIntRange sentinel with a plain ==.
func /*@eqrange*/eqRange(n int, s string) error {
	err := classify(n, s)
	if err == errs.ErrIntRange {
		return nil
	}
	return err
}

// describe type-switches over the instantiations.
func /*@describe*/describe(n int, s string) error {
	switch err := classify(n, s).(type) {
	case nil:
		return nil
	case *errs.TypedErr[string]:
		return fmt.Errorf("string: %w", err)
	case *errs.TypedErr[int]:
		return nil
	default:
		return err
	}
}

// liftBoth instantiates Lift with a pointer type and with error itself.
func liftBoth(n int) error {
	if n > 0 {
		return errs./*@liftptr*/Lift(&errs.TypedErr[int]{Op: "lift", Val: n})
	}
	return errs./*@liftsent*/Lift(errs.ErrNotFound)
}

// liftNil converts a nil *TypedErr[int] to a non-nil error.
func /*@liftnil*/liftNil() error {
	var te *errs.TypedErr[int]
	return errs.Lift(te)
}

// dropInts filters with errors.As on a type parameter.
func dropInts(n int, s string) error {
	return errs./*@dropints*/Drop[*errs.TypedErr[int]](classify(n, s))
}

// skipRange instantiates the comparable helper with the error interface, so
// its == compares error values and its T result is an error result.
func skipRange(n int, s string) error {
	return errs./*@skiprange*/Skip[error](classify(n, s), errs.ErrIntRange)
}

// skipMissing uses the same helper with an inferred T = error and a plain
// errors.New sentinel.
func skipMissing(c *Scores, k string) error {
	_, err := c.Get(k)
	return errs./*@skipmissing*/Skip(err, errs.ErrNotFound)
}

// viaAsType guards through the generic AsType helper.
func /*@viaastype*/viaAsType(n int, s string) error {
	err := classify(n, s)
	if _, ok := errs.AsType[*errs.TypedErr[string]](err); ok {
		return nil
	}
	return err
}

// deepWrap nests two instantiations of the generic wrapper around %w.
func /*@deepwrap*/deepWrap() error {
	return errs.Wrap(1, errs.Wrap("job", fmt.Errorf("stop: %w", errs.ErrStop)))
}
