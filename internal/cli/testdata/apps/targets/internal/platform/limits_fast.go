//go:build fast

package platform

import "errors"

// ErrFastDisabled is returned when the fast path is compiled in but off.
var ErrFastDisabled = errors.New("platform: fast path disabled")

// MaxBatch returns the largest batch the fast path accepts.
func /*@maxbatch*/MaxBatch() (int, error) {
	return 0, ErrFastDisabled
}
