// Package seq holds slice pipeline helpers with error-returning callbacks.
package seq

import (
	"fmt"

	"example.com/generics/errs"
)

// Map applies f to every element and stops at the first error.
func /*@map*/Map[T, U any](xs []T, f func(T) (U, error)) ([]U, error) {
	out := make([]U, 0, len(xs))
	for _, x := range xs {
		y, err := /*@mapcb*/f(x)
		if err != nil {
			return nil, err
		}
		out = append(out, y)
	}
	return out, nil
}

// Filter keeps the elements for which keep reports true.
func Filter[T any](xs []T, keep func(T) (bool, error)) ([]T, error) {
	var out []T
	for _, x := range xs {
		ok, err := keep(x)
		if err != nil {
			return nil, fmt.Errorf("filter: %w", err)
		}
		if ok {
			out = append(out, x)
		}
	}
	return out, nil
}

// Reduce folds xs into acc.
func Reduce[T, A any](xs []T, acc A, f func(A, T) (A, error)) (A, error) {
	for _, x := range xs {
		next, err := f(acc, x)
		if err != nil {
			return acc, err
		}
		acc = next
	}
	return acc, nil
}

// Each calls f on every element and reports the failing index.
func /*@each*/Each[T any](xs []T, f func(T) error) error {
	for i, x := range xs {
		if err := f(x); err != nil {
			return fmt.Errorf("each[%d]: %w", i, err)
		}
	}
	return nil
}

// First forwards err, or returns the first element of xs.
func First[T any](xs []T, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	if len(xs) == 0 {
		return zero, errs.ErrEmpty
	}
	return xs[0], nil
}

// Retry runs op up to n times and wraps the last failure.
func Retry[T any](n int, op func() (T, error)) (T, error) {
	var last error
	for range n {
		v, err := op()
		if err == nil {
			return v, nil
		}
		last = err
	}
	var zero T
	return zero, fmt.Errorf("after %d attempts: %w", n, last)
}

// Keys returns the keys of m; it cannot fail.
func Keys[K comparable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
