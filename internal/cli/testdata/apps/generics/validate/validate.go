// Package validate offers generic validation helpers over a method
// constraint and over numeric type sets.
package validate

import (
	"fmt"
	"strings"

	"example.com/generics/errs"
)

// Validator is used both as a constraint and as an ordinary interface.
type Validator interface {
	/*@ifacemethod*/Validate() error
}

// UserID is a positive numeric identifier.
type UserID int

func (id UserID) Validate() error {
	if id < 0 {
		return errs.ErrNegative
	}
	if id == 0 {
		return errs.ErrEmpty
	}
	return nil
}

// Name is a short display name.
type Name string

func (n Name) Validate() error {
	if n == "" {
		return errs.ErrEmpty
	}
	if len(n) > 32 {
		return &errs.TypedErr[string]{Op: "name", Val: string(n)}
	}
	return nil
}

// Email validates through a pointer receiver.
type Email struct{ Addr string }

func (e *Email) Validate() error {
	if !strings.Contains(e.Addr, "@") {
		return fmt.Errorf("email %q: missing @", e.Addr)
	}
	return nil
}

// Check validates one value; the errors depend on the type argument.
func /*@check*/Check[/*@tparam*/T Validator](v T) error { return v./*@checkcall*/Validate() }

// All validates every value and reports the first failure with its index.
func /*@all*/All[T Validator](vs ...T) error {
	for i, v := range vs {
		if err := /*@allcheck*/Check(v); err != nil {
			return fmt.Errorf("item %d: %w", i, err)
		}
	}
	return nil
}

// Unused is never instantiated anywhere in the program.
func /*@unused*/Unused[T Validator](v T) error {
	if err := v./*@unusedcall*/Validate(); err != nil {
		return fmt.Errorf("unused: %w", err)
	}
	return nil
}

// Ptr is the "pointer to T that validates" constraint.
type Ptr[T any] interface {
	*T
	Validator
}

// New builds a T, lets init fill it and validates it through *T.
func /*@new*/New[T any, P Ptr[T]](init func(P)) (*T, error) {
	v := new(T)
	init(v)
	if err := P(v).Validate(); err != nil {
		return nil, err
	}
	return v, nil
}

// Number is a type set of numeric underlying types.
type Number interface {
	~int | ~int64 | ~float64
}

// Positive rejects negative numbers.
func /*@positive*/Positive[N Number](n N) error {
	if n < 0 {
		return errs.ErrNegative
	}
	return nil
}

// InRange rejects n outside [lo, hi] with an error typed by N.
func /*@inrange*/InRange[N Number](n, lo, hi N) error {
	if clamped := /*@min*/min(max(n, lo), hi); clamped != n {
		return &errs.TypedErr[N]{Op: "range", Val: n}
	}
	return nil
}
