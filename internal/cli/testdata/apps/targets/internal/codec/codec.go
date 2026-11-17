// Package codec decodes raw record values.
package codec

import (
	"bytes"
	"errors"
	"fmt"
)

// Sentinels of the codec.
var (
	ErrEmpty         = errors.New("codec: empty input")
	ErrSyntax        = errors.New("codec: syntax error")
	ErrUnknownFormat = errors.New("codec: unknown format")
)

var formats []string

// Register adds a format name.
func Register(name string) error {
	if name == "" {
		return ErrEmpty
	}
	formats = append(formats, name)
	return nil
}

// Decode parses raw with parse.
func /*@decode*/Decode[/*@decode_tparam*/T any](raw []byte, parse func([]byte) (T, error)) (T, error) {
	var zero T
	if len(raw) == 0 {
		return zero, ErrEmpty
	}
	v, err := /*@parse_call*/parse(raw)
	if err != nil {
		return zero, fmt.Errorf("decode: %w", err)
	}
	return v, nil
}

// Trim strips surrounding blanks; it is a function literal held in a var.
var /*@trim_var*/Trim = /*@trim_lit*/func(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, ErrEmpty
	}
	return bytes.TrimSpace(raw), nil
}

// ParseInt parses a non-negative decimal number.
func ParseInt(raw []byte) (int, error) {
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, ErrSyntax
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
