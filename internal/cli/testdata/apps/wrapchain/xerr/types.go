package xerr

import (
	"errors"
	"strings"
)

// ErrRetryable is matched by CodeError.Is for server-side codes.
var ErrRetryable = errors.New("retryable")

// OpError wraps Err with the failed operation (pointer receiver).
type OpError struct {
	Op  string
	Err error
}

func (e *OpError) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *OpError) Unwrap() error { return e.Err }

// FieldError wraps Cause with a field name (value receiver).
type FieldError struct {
	Field string
	Cause error
}

func (e FieldError) Error() string { return "field " + e.Field + ": " + e.Cause.Error() }
func (e FieldError) Unwrap() error { return e.Cause }

// LazyError decides in Unwrap whether its cause is visible.
type LazyError struct {
	Hidden bool
	Cause  error
}

func (e *LazyError) Error() string { return "lazy" }
func (e *LazyError) Unwrap() error {
	if e.Hidden {
		return nil
	}
	return e.Cause
}

// StageError gets its cause through a setter.
type StageError struct {
	Stage string
	cause error
}

func (e *StageError) Error() string      { return "stage " + e.Stage }
func (e *StageError) Unwrap() error      { return e.cause }
func (e *StageError) SetCause(err error) { e.cause = err }

// MultiError wraps several errors at once.
type MultiError struct{ Errs []error }

func (e *MultiError) Error() string {
	parts := make([]string, len(e.Errs))
	for i, err := range e.Errs {
		parts[i] = err.Error()
	}
	return strings.Join(parts, "; ")
}
func (e *MultiError) Unwrap() []error { return e.Errs }

// CodeError is a remote status code; 5xx codes match ErrRetryable.
type CodeError struct{ Code int }

func (e *CodeError) Error() string { return "remote code" }
func (e *CodeError) Is(target error) bool {
	return target == ErrRetryable && e.Code >= 500
}

// LimitError reports an exceeded limit.
type LimitError struct{ Max, Got int }

func (e *LimitError) Error() string { return "limit exceeded" }

// QuotaError converts itself to a *LimitError through As.
type QuotaError struct{ Used, Max int }

func (e *QuotaError) Error() string { return "quota" }
func (e *QuotaError) As(target any) bool {
	if t, ok := target.(**LimitError); ok {
		*t = &LimitError{Max: e.Max, Got: e.Used}
		return true
	}
	return false
}
