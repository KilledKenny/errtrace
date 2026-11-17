// Package domain holds the bank's core types and its error vocabulary.
package domain

import (
	"errors"
	"fmt"
)

// Sentinels shared by every layer.
var (
	ErrAccountClosed = errors.New("account closed")
	ErrFrozen        = errors.New("account frozen")
	ErrDuplicate     = errors.New("duplicate transfer")
	ErrLimitExceeded = errors.New("limit exceeded")
	ErrSameAccount   = errors.New("source and destination are the same account")
	// ErrNotFound matches every NotFoundError through its Is method.
	ErrNotFound = errors.New("not found")
)

// ValidationError reports a bad input field.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return "invalid " + e.Field + ": " + e.Reason }

// InsufficientFundsError is a value-receiver error carrying the shortfall.
type InsufficientFundsError struct {
	Account AccountID
	Need    Money
	Have    Money
}

func (e InsufficientFundsError) Error() string {
	return fmt.Sprintf("account %s: need %d, have %d", e.Account, e.Need, e.Have)
}

// NotFoundError is generic over the identifier type of the missing entity.
type NotFoundError[ID comparable] struct {
	Kind string
	ID   ID
}

func (e NotFoundError[ID]) Error() string { return fmt.Sprintf("%s %v not found", e.Kind, e.ID) }

// Is makes errors.Is(err, ErrNotFound) true for every instantiation.
func (e NotFoundError[ID]) Is(target error) bool { return target == ErrNotFound }
