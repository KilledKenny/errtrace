// Package validate checks documents and reports problems with errors.Join.
package validate

import (
	"errors"
	"fmt"

	"example.com/wrapchain/doc"
	"example.com/wrapchain/xerr"
)

func checkTitle(d *doc.Document) error {
	if d.Title == "" {
		return doc.ErrNoTitle
	}
	return nil
}

func checkBody(d *doc.Document) error {
	if len(d.Lines) == 0 {
		return doc.ErrEmpty
	}
	return nil
}

func checkLength(d *doc.Document) error {
	if n := len(d.Lines); n > 1000 {
		return &xerr.LimitError{Max: 1000, Got: n}
	}
	return nil
}

// All joins two checks between literal nils.
func /*@all*/All(d *doc.Document) error {
	return errors./*@joincall*/Join(nil, checkTitle(d), nil, checkBody(d))
}

// Single joins exactly one operand.
func /*@single*/Single(d *doc.Document) error {
	return errors.Join(checkLength(d))
}

// None joins only nil operands, so it is always nil.
func /*@none*/None() error {
	var a, b error
	return errors.Join(a, nil, b)
}

// Collect gathers the failures in a slice and spreads it into Join.
func /*@collect*/Collect(d *doc.Document) error {
	var errs []error
	if err := checkTitle(d); err != nil {
		errs = append(errs, err)
	}
	if err := checkBody(d); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Report joins first and wraps the join.
func /*@report*/Report(d *doc.Document) error {
	if err := errors.Join(checkTitle(d), checkBody(d)); err != nil {
		return fmt.Errorf("validate %s: %w", d.Path, err)
	}
	return nil
}

// Labelled wraps each failure first and joins the wraps.
func /*@labelled*/Labelled(d *doc.Document) error {
	var titleErr, bodyErr error
	if d.Title == "" {
		titleErr = fmt.Errorf("title: %w", doc.ErrNoTitle)
	}
	if len(d.Lines) == 0 {
		bodyErr = fmt.Errorf("body: %w", doc.ErrEmpty)
	}
	return errors.Join(titleErr, bodyErr)
}

// Lines accumulates one wrapped error per bad line.
func /*@lines*/Lines(d *doc.Document) error {
	var err error
	for i, l := range d.Lines {
		if len(l) > doc.MaxLine {
			err = errors.Join(err, fmt.Errorf("line %d: %w", i, doc.ErrTooLong))
		}
	}
	return err
}
