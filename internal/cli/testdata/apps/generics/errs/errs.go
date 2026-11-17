// Package errs holds the sentinels and the generic error types shared by
// the collections library.
package errs

import (
	"errors"
	"fmt"
)

// Plain sentinels.
var (
	ErrNotFound  = errors.New("not found")
	ErrEmpty     = errors.New("empty")
	ErrNegative  = errors.New("negative")
	ErrTooLong   = errors.New("too long")
	ErrDuplicate = errors.New("duplicate")
	ErrExpired   = errors.New("expired")
	ErrStop      = errors.New("stop")
)

// TypedErr reports a rejected value of type T.
type /*@typedecl*/TypedErr[T any] struct {
	Op  string
	Val T
}

func (e *TypedErr[T]) Error() string { return fmt.Sprintf("%s: bad value %v", e.Op, e.Val) }

// Failure is a generic alias: Failure[T] and TypedErr[T] are the same type.
type Failure[T any] = TypedErr[T]

// Sentinels by instantiation: same generic type, distinct type arguments.
var (
	ErrIntRange = &TypedErr[int]{Op: "range"}
	ErrStrRange = &TypedErr[string]{Op: "range"}
)

// Wrapped attaches a typed context value to an error and unwraps to it.
type Wrapped[C any] struct {
	Ctx C
	Err error
}

func (w *Wrapped[C]) Error() string { return fmt.Sprintf("%v: %v", w.Ctx, w.Err) }
func (w *Wrapped[C]) Unwrap() error { return w.Err }

// Wrap returns err wrapped with ctx, or nil when err is nil.
func /*@wrap*/Wrap[C any](ctx C, err error) error {
	if err == nil {
		return nil
	}
	return &Wrapped[C]{Ctx: ctx, Err: err}
}

// Tagged embeds an error without offering Unwrap: the inner error is hidden
// from errors.Is and errors.As.
type Tagged[T any] struct {
	error
	Tag T
}

// Tag returns err labelled with tag.
func Tag[T any](err error, tag T) error { return Tagged[T]{error: err, Tag: tag} }

// Lift converts any concrete error type to the error interface.
func /*@lift*/Lift[E error](e E) error { return e }

// AsType is errors.As with the target type as a type argument.
func AsType[E error](err error) (E, bool) {
	var e E
	ok := errors.As(err, &e)
	return e, ok
}

// Drop returns nil when err's chain holds an E, and err otherwise.
func /*@drop*/Drop[E error](err error) error {
	var e E
	if errors.As(err, &e) {
		return nil
	}
	return err
}

// Equal reports a == b for any comparable type.
func Equal[T comparable](a, b T) bool { return a == b }

// Skip returns the zero value when v equals skip, and v otherwise.
func /*@skip*/Skip[T comparable](v, skip T) T {
	if v == skip {
		var zero T
		return zero
	}
	return v
}

// NewErr builds a TypedErr; its result is the concrete pointer type.
func /*@newerr*/NewErr[T any](op string, v T) *TypedErr[T] {
	return &TypedErr[T]{Op: op, Val: v}
}
