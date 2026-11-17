// Package kverr holds the error vocabulary shared by every layer.
package kverr

import (
	"errors"
	"strconv"
)

// ErrNotFound is returned when a key does not exist.
var ErrNotFound = errors.New("kv: not found")

var (
	// ErrConflict is returned when a compare-and-swap loses.
	ErrConflict = errors.New("kv: conflict")
	// ErrReadOnly is returned for writes to a read-only store.
	ErrReadOnly = errors.New("kv: read-only")

	errInternal = errors.New("kv: internal error")
)

// Internal returns the unexported internal-error sentinel.
func /*@internal*/Internal() error { return errInternal }

// Code is a numeric error (int kind, value receiver).
type Code int

func (c Code) Error() string { return "kv: code " + strconv.Itoa(int(c)) }

// CodeTooLarge is a constant of an error type.
const CodeTooLarge Code = 413

// ErrQuota is a sentinel whose static type is the custom type Code.
var ErrQuota = Code(507)

// ErrThrottled is an interface-typed sentinel holding a Code.
var ErrThrottled error = Code(429)

// Reason is a textual error (string kind, value receiver).
type Reason string

func (r Reason) Error() string { return string(r) }

// ErrEmptyKey is a sentinel of the custom string type.
var ErrEmptyKey = Reason("kv: empty key")

// FieldError reports an invalid field (struct kind, value receiver).
type FieldError struct {
	Field string
	Msg   string
}

func (e FieldError) Error() string { return e.Field + ": " + e.Msg }

// OpError records a failed operation (struct kind, pointer receiver).
type OpError struct {
	Op  string
	Key string
	Err error
}

func (e *OpError) Error() string {
	if e.Err == nil {
		return e.Op + " " + e.Key
	}
	return e.Op + " " + e.Key + ": " + e.Err.Error()
}

func (e *OpError) Unwrap() error { return e.Err }

// ErrCorrupt is a pointer-typed sentinel.
var ErrCorrupt = &OpError{Op: "verify"}

// OrNil returns e as an error, and an untyped nil for a nil pointer.
func (e *OpError) /*@ornil*/OrNil() error {
	if e == nil {
		return nil
	}
	return e
}
