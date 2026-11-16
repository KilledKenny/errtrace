// Package result has Result and Option containers.
package result

import "example.com/generics/errs"

// Result carries a value or an error in its fields.
type Result[T any] struct {
	val T
	err error
}

// Try runs f and captures its outcome.
func Try[T any](f func() (T, error)) Result[T] {
	v, err := f()
	return Result[T]{val: v, err: err}
}

// Get unpacks the result.
func (r Result[T]) /*@get*/Get() (T, error) { return r.val, r.err }

// Option is a value that may be absent.
type Option[T any] struct{ val *T }

// Some returns a present option.
func Some[T any](v T) Option[T] { return Option[T]{val: &v} }

// Unwrap returns the value or ErrEmpty. Despite its name it is unrelated
// to error unwrapping: Option is not an error.
func (o Option[T]) /*@unwrap*/Unwrap() (T, error) {
	if o.val == nil {
		var zero T
		return zero, errs.ErrEmpty
	}
	return *o.val, nil
}

// Either is never instantiated in this program.
type Either[L, R any] struct {
	left  *L
	right *R
}

// Right returns the right value.
func (e Either[L, R]) /*@right*/Right() (R, error) {
	if e.right == nil {
		var zero R
		return zero, errs.ErrEmpty
	}
	return *e.right, nil
}
