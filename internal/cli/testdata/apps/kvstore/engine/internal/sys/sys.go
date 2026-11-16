// Package sys provides the body of engine.fsync through a linkname push.
package sys

import (
	"errors"
	_ "unsafe" // for go:linkname
)

var errShortWrite = errors.New("sys: short write")

//go:linkname fsync example.com/kvstore/engine.fsync
func /*@fsync*/fsync(n int) error {
	if n < 0 {
		return errShortWrite
	}
	return nil
}
