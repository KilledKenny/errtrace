// Package store is a fake key-value store.
package store

import "errors"

// ErrMissing is returned for unknown keys.
var ErrMissing = errors.New("missing")

// Get looks up key.
func Get(key string) error {
	if key == "" {
		return ErrMissing
	}
	return nil
}
