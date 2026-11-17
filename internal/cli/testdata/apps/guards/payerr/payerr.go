// Package payerr holds the error vocabulary of the payment processor.
package payerr

import "errors"

// Sentinels returned by identity.
var (
	ErrDeclined    = errors.New("card declined")
	ErrTimeout     = errors.New("gateway timeout")
	ErrNotFound    = errors.New("payment not found")
	ErrRateLimited = errors.New("rate limited")
	ErrInternal    = errors.New("internal error")
	ErrDuplicate   = errors.New("duplicate payment")
)

// ErrBadAmount is a sentinel of a comparable value type.
var ErrBadAmount error = ValidationError{Field: "amount"}

// ErrEmpty is a sentinel of a zero-size type.
var ErrEmpty error = Empty{}

// CodeError is a gateway response code (pointer receiver).
type CodeError struct {
	Code int
	Msg  string
}

func (e *CodeError) Error() string { return e.Msg }

// ValidationError is a comparable value-type error.
type ValidationError struct{ Field string }

func (e ValidationError) Error() string { return "invalid " + e.Field }

// OpError wraps the error of one gateway operation.
type OpError struct {
	Op  string
	Err error
}

func (e *OpError) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *OpError) Unwrap() error { return e.Err }

// Temporary is implemented by errors that are worth retrying.
type Temporary interface {
	error
	Temporary() bool
}

// NetError is a transport failure; it implements Temporary.
type NetError struct{ Host string }

func (e *NetError) Error() string   { return "net: " + e.Host }
func (e *NetError) Temporary() bool { return true }

// FlexError decides itself what it matches (custom Is).
type FlexError struct{ Kind string }

func (e *FlexError) Error() string { return "flex " + e.Kind }
func (e *FlexError) Is(target error) bool {
	return e.Kind == "notfound" && target == ErrNotFound
}

// ShapeError can present itself as a *CodeError (custom As).
type ShapeError struct{ Code int }

func (e *ShapeError) Error() string { return "shape" }
func (e *ShapeError) As(target any) bool {
	if p, ok := target.(**CodeError); ok {
		*p = &CodeError{Code: e.Code, Msg: "shape"}
		return true
	}
	return false
}

// Empty is a zero-size error type.
type Empty struct{}

func (Empty) Error() string { return "empty" }
