// Package transform rewrites documents; failures travel in the custom
// wrapper types of package xerr.
package transform

import (
	"errors"
	"fmt"

	"example.com/wrapchain/doc"
	"example.com/wrapchain/xerr"
)

func upper(d *doc.Document) error {
	if d.Title == "" {
		return doc.ErrNoTitle
	}
	return nil
}

// Title wraps through WithOp: an *OpError built from its fields.
func /*@title*/Title(d *doc.Document) error {
	return xerr.WithOp("title", upper(d))
}

// Noop hands WithOp a nil error, so nothing comes back.
func /*@noop*/Noop() error {
	return /*@nilqual*/xerr./*@nilcall*/WithOp("noop", nil)
}

// Stage wraps through the two-level helper WithStage.
func /*@stage*/Stage(d *doc.Document) error {
	return xerr.WithStage("transform", upper(d))
}

// Field wraps in a value-receiver wrapper.
func /*@field*/Field(d *doc.Document, name string) error {
	if _, ok := d.Fields[name]; !ok {
		return xerr.FieldError{Field: name, Cause: doc.ErrEmpty}
	}
	return nil
}

// Lazy returns a wrapper whose Unwrap computes its result.
func /*@lazy*/Lazy(d *doc.Document) error {
	if d.Version > 9 {
		return &xerr.LazyError{Hidden: d.Version > 99, Cause: doc.ErrSyntax}
	}
	return nil
}

// Late sets the wrapped field after construction.
func /*@late*/Late(d *doc.Document) error {
	if d.Version < 0 {
		e := &xerr.OpError{Op: "late"}
		e.Err = doc.ErrSyntax
		return e
	}
	return nil
}

// Maybe sets the wrapped field only on one branch.
func /*@maybe*/Maybe(d *doc.Document, verbose bool) error {
	if len(d.Lines) > doc.MaxLine {
		e := &xerr.OpError{Op: "maybe", Err: doc.ErrEmpty}
		if verbose {
			e.Err = doc.ErrTooLong
		}
		return e
	}
	return nil
}

// Setter sets the wrapped error through a method.
func /*@setter*/Setter(d *doc.Document) error {
	if len(d.Lines) > doc.MaxLine {
		e := &xerr.StageError{Stage: "setter"}
		e.SetCause(doc.ErrTooLong)
		return e
	}
	return nil
}

// Bare returns a wrapper that wraps nothing: Err stays nil.
func /*@bare*/Bare(d *doc.Document) error {
	if d.Path == "" {
		return &xerr.OpError{Op: "bare"}
	}
	return nil
}

// Always wraps unconditionally: the *OpError is non-nil even when upper
// succeeds.
func /*@always*/Always(d *doc.Document) error {
	return &xerr.OpError{Op: "always", Err: upper(d)}
}

// Multi returns an Unwrap() []error wrapper.
func /*@multi*/Multi(d *doc.Document) error {
	if d.Title == "" && len(d.Lines) == 0 {
		return &xerr.MultiError{Errs: []error{doc.ErrNoTitle, doc.ErrEmpty}}
	}
	return nil
}

func fetch(d *doc.Document) error {
	if d.Version > 2 {
		return &xerr.CodeError{Code: 503}
	}
	if d.Path == "" {
		return doc.ErrDisk
	}
	return nil
}

// Retryable keeps only what matches ErrRetryable; CodeError.Is does.
func /*@retryable*/Retryable(d *doc.Document) error {
	err := fetch(d)
	if errors.Is(err, xerr.ErrRetryable) {
		return err
	}
	return nil
}

func quota(d *doc.Document) error {
	if len(d.Lines) > 500 {
		return &xerr.QuotaError{Used: len(d.Lines), Max: 500}
	}
	if d.Path == "" {
		return doc.ErrDisk
	}
	return nil
}

// OverLimit keeps only what converts to *LimitError; QuotaError.As does.
func /*@overlimit*/OverLimit(d *doc.Document) error {
	err := quota(d)
	var le *xerr.LimitError
	if errors.As(err, &le) {
		return fmt.Errorf("limit %d: %w", le.Max, err)
	}
	return nil
}

// Include resolves nested includes, wrapping once per level.
func /*@include*/Include(d *doc.Document, depth int) error {
	if depth > len(d.Lines) {
		return doc.ErrCycle
	}
	if err := Include(d, depth+1); err != nil {
		return fmt.Errorf("include %d: %w", depth, err)
	}
	return nil
}

// Normalize returns the same origin bare and through two different wraps.
func /*@normalize*/Normalize(d *doc.Document, fast bool) error {
	err := upper(d)
	if err == nil {
		return nil
	}
	if fast {
		return fmt.Errorf("fast: %w", err)
	}
	if d.Version > 1 {
		return xerr.WithOp("normalize", err)
	}
	return err
}

// annotate wraps its parameter.
func /*@annotate*/annotate(step string, err error) error {
	return xerr.Wrapf(err, "step %s", step)
}

// Trim annotates the failure of upper.
func Trim(d *doc.Document) error {
	return /*@anncall*/annotate("trim", upper(d))
}

// Trace reports through the forwarding helpers, two levels deep.
func /*@trace*/Trace(d *doc.Document) error {
	if err := upper(d); err != nil {
		return xerr.Tracef("trace %s: %w", d.Path, err)
	}
	return nil
}

// Peel unwraps the *OpError of Title again.
func /*@peel*/Peel(d *doc.Document) error {
	return errors.Unwrap(Title(d))
}

// PeelJoin unwraps a join: errors.Unwrap never looks into Unwrap() []error.
func /*@peeljoin*/PeelJoin() error {
	return errors.Unwrap(errors.Join(doc.ErrEmpty, doc.ErrNoTitle))
}

// PeelPlain unwraps a sentinel that wraps nothing.
func /*@peelplain*/PeelPlain() error {
	return errors.Unwrap(doc.ErrEmpty)
}

// PeelTwice unwraps one of two fmt.Errorf levels.
func /*@peeltwice*/PeelTwice(d *doc.Document) error {
	err := upper(d)
	if err == nil {
		return nil
	}
	inner := fmt.Errorf("inner: %w", err)
	outer := fmt.Errorf("outer: %w", inner)
	return errors.Unwrap(outer)
}

// Settle drops what matches ErrRetryable; only CodeError.Is can.
func /*@settle*/Settle(d *doc.Document) error {
	err := fetch(d)
	if errors.Is(err, xerr.ErrRetryable) {
		return nil
	}
	return err
}

func deadline(d *doc.Document) error {
	if d.Version > 3 {
		return &xerr.TimeoutError{Op: "deadline", Err: doc.ErrDisk}
	}
	return nil
}

// Patient drops timeouts: TimeoutError.Is always matches ErrRetryable.
func /*@patient*/Patient(d *doc.Document) error {
	err := deadline(d)
	if errors.Is(err, xerr.ErrRetryable) {
		return nil
	}
	return err
}

// Durable drops disk failures, found through TimeoutError.Unwrap.
func /*@durable*/Durable(d *doc.Document) error {
	err := deadline(d)
	if errors.Is(err, doc.ErrDisk) {
		return nil
	}
	return err
}

// FieldRef returns a pointer to the value-receiver wrapper.
func /*@fieldref*/FieldRef(d *doc.Document, name string) error {
	if v := d.Fields[name]; len(v) > doc.MaxLine {
		return &xerr.FieldError{Field: name, Cause: doc.ErrTooLong}
	}
	return nil
}
