// Package validate checks keys and values before they reach the service.
package validate

import (
	"strings"

	"example.com/kvstore/kverr"
)

// Key validates a key.
func /*@key*/Key(k string) error {
	if k == "" {
		return kverr.ErrEmptyKey
	}
	if strings.ContainsAny(k, " \t\n") {
		return kverr.FieldError{Field: "key", Msg: "contains whitespace"}
	}
	if strings.HasPrefix(k, "_") {
		return kverr.Reason("kv: reserved key")
	}
	if len(k) > 64 {
		return kverr.CodeTooLarge
	}
	return nil
}

// Value validates a value; the error is the first result.
func /*@value*/Value(v string) (error, int) {
	if len(v) > 1024 {
		return kverr./*@conv*/Code(len(v)), 0
	}
	return nil, len(v)
}

// Pair validates a key and a value and reports both problems separately.
func /*@pair*/Pair(k, v string) (keyErr, valErr error) {
	keyErr = Key(k)
	valErr, _ = Value(v)
	return keyErr, valErr
}

// Clean normalises a key; it has no error result.
func /*@clean*/Clean(k string) string { return strings.TrimSpace(k) }
