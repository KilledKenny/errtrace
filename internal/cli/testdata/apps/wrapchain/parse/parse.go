// Package parse turns raw text into documents; every failure is wrapped
// with fmt.Errorf in one of its many spellings.
package parse

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"example.com/wrapchain/doc"
)

const fmtRow = "row %d: %w"

const (
	prefixCell = "cell %s"
	fmtCell    = prefixCell + ": %w"
)

// Version parses the header version.
func /*@version*/Version(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("version %q: %w", s, err)
	}
	return n, nil
}

// Open opens the document file.
func /*@open*/Open(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open document: %w", err)
	}
	return f, nil
}

// digits parses an unsigned decimal of at most four digits, reporting
// failures the way package strconv does.
func digits(s string) (int, error) {
	if len(s) > 4 {
		return 0, &strconv.NumError{Func: "digits", Num: s, Err: strconv.ErrRange}
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, &strconv.NumError{Func: "digits", Num: s, Err: strconv.ErrSyntax}
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// Field parses a numeric field: two %w in one format.
func /*@field*/Field(name, raw string) (int, error) {
	n, err := digits(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: field %s: %w", doc.ErrSyntax, name, err)
	}
	return n, nil
}

// Row checks one row: constant format, and %w mixed with %d, %s and %v.
func /*@row*/Row(n int, line string) error {
	if line == "" {
		return fmt.Errorf(fmtRow, n, doc.ErrEmpty)
	}
	if len(line) > doc.MaxLine {
		return fmt.Errorf("line %d (%s...): %v: %w", n, line[:8], doc.ErrSyntax, doc.ErrTooLong)
	}
	return nil
}

// Cell checks one cell: the format is a concatenation of constants.
func /*@cell*/Cell(name, v string) error {
	if v == "" {
		return fmt.Errorf(fmtCell, name, doc.ErrEmpty)
	}
	return nil
}

// Column checks a column: explicit argument indexes and * widths.
func /*@column*/Column(col, width int, raw string) error {
	if raw == "" {
		return fmt.Errorf("col %[2]*[1]d: %[3]w (not %[4]v)", col, width, doc.ErrEmpty, doc.ErrSyntax)
	}
	if len(raw) > width {
		return fmt.Errorf("%*d|%-12w|%+v", width, col, doc.ErrTooLong, doc.ErrSyntax)
	}
	return nil
}

// Reorder reports a misplaced value: %[2]w then an implicit %w (argument 3).
func /*@reorder*/Reorder(raw string) error {
	if raw == " " {
		return fmt.Errorf("%[2]w then %w, after %[1]v", doc.ErrSyntax, doc.ErrEmpty, doc.ErrTooLong)
	}
	return nil
}

// Tag checks a tag: the %w operand is a nil error unless strict is set.
func /*@tag*/Tag(name string, strict bool) error {
	var cause error
	if strict {
		cause = doc.ErrSyntax
	}
	if name == "" {
		return fmt.Errorf("tag: %w", cause)
	}
	return nil
}

// Footer checks the last line: the %w operand is always a nil error.
func /*@footer*/Footer(line string) error {
	var cause error
	if line != "EOF" {
		return fmt.Errorf("footer %q: %w", line, cause)
	}
	return nil
}

// Label rejects a label: the %w operand is not an error at all.
func /*@label*/Label(name string) error {
	layout := "label %w" // a variable, so vet does not reject the string operand
	if len(name) > 8 {
		return fmt.Errorf(layout, name)
	}
	return nil
}

// Percent parses a percentage: none of its errors wraps anything.
func /*@percent*/Percent(raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("want 100%%w, got %q: %v", raw, err)
	}
	if n > 100 {
		return 0, fmt.Errorf("%s", doc.ErrTooLong.Error())
	}
	if n < 0 {
		return 0, errors.New("percent: " + doc.ErrSyntax.Error())
	}
	return n, nil
}

// Section checks a section: its format string is built at run time.
func /*@section*/Section(name, body string) error {
	if body == "" {
		return fmt.Errorf("section "+name+": %w", doc.ErrEmpty)
	}
	return nil
}

// Anchor rejects an empty anchor: %[3]w points past the operands, so
// nothing is wrapped.
func /*@anchor*/Anchor(name string) error {
	layout := "anchor %[3]w: %v" // a variable, so vet does not reject the index
	if name == "" {
		return fmt.Errorf(layout, doc.ErrSyntax, doc.ErrEmpty)
	}
	return nil
}

// Echo wraps the same operand with two %w.
func /*@echo*/Echo(raw string) error {
	if _, err := digits(raw); err != nil {
		return fmt.Errorf("echo %w (again: %w)", err, err)
	}
	return nil
}
