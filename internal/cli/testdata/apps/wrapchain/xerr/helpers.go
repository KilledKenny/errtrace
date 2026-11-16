// Package xerr holds the project-local wrap helpers and wrapper types.
package xerr

import "fmt"

// Annotatef forwards its format and operands to fmt.Errorf.
func /*@annotatef*/Annotatef(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

// Tracef is a second forwarding level on top of Annotatef.
func Tracef(format string, args ...any) error {
	return Annotatef(format, args...)
}

// Wrapf wraps err with a formatted message; a nil err stays nil.
func /*@wrapf*/Wrapf(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), err)
}

// WithOp wraps err in an *OpError; a nil err stays nil.
func /*@withop*/WithOp(op string, err error) error {
	if err == nil {
		return nil
	}
	return &OpError{Op: op, Err: err}
}

// WithStage is WithOp with a stage prefix: a helper two levels deep.
func WithStage(stage string, err error) error {
	return WithOp("stage "+stage, err)
}
