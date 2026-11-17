package main

import (
	"errors"
	"strconv"

	"example.com/wrapchain/doc"
	"example.com/wrapchain/export"
	"example.com/wrapchain/parse"
	"example.com/wrapchain/transform"
	"example.com/wrapchain/validate"
	"example.com/wrapchain/xerr"
)

// checkLenient drops reports that contain ErrEmpty, looking through the
// fmt.Errorf and errors.Join wraps of validate.Report.
func /*@lenient*/checkLenient(d *doc.Document) error {
	err := validate.Report(d)
	if errors.Is(err, doc.ErrEmpty) {
		return nil
	}
	return err
}

// titleStrict compares a wrapped error with ==, which never matches.
func /*@strict*/titleStrict(d *doc.Document) error {
	err := transform.Title(d)
	if err == doc.ErrNoTitle {
		return nil
	}
	return err
}

// titleQuiet swallows *OpError, the only type Title returns.
func /*@quiet*/titleQuiet(d *doc.Document) error {
	err := transform.Title(d)
	switch err.(type) {
	case *xerr.OpError:
		return nil
	}
	return err
}

// normalizeQuiet swallows a top-level *OpError only.
func /*@normquiet*/normalizeQuiet(d *doc.Document) error {
	err := transform.Normalize(d, false)
	if _, ok := err.(*xerr.OpError); ok {
		return nil
	}
	return err
}

// titleLenient swallows ErrNoTitle, found through the *OpError of Title.
func /*@titlelenient*/titleLenient(d *doc.Document) error {
	err := transform.Title(d)
	if errors.Is(err, doc.ErrNoTitle) {
		return nil
	}
	return err
}

// fieldOrZero swallows every error that holds a *strconv.NumError; all
// errors of parse.Field do, next to doc.ErrSyntax in the same two-%w wrap.
func /*@fieldzero*/fieldOrZero(raw string) (int, error) {
	n, err := parse.Field("size", raw)
	var ne *strconv.NumError
	if errors.As(err, &ne) {
		return 0, nil
	}
	return n, err
}

// rolloutLenient swallows disk failures, found below the 10 Wraps of the
// delivery ladder.
func /*@rolloutlenient*/rolloutLenient() error {
	err := export.Rollout()
	if errors.Is(err, doc.ErrDisk) {
		return nil
	}
	return err
}
