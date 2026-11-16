//go:build !fast

package platform

import "errors"

// ErrNoLimit is returned on platforms without a tuned batch limit.
var ErrNoLimit = errors.New("platform: no batch limit")

// MaxBatch returns the largest batch the platform accepts.
func /*@maxbatch*/MaxBatch() (int, error) {
	return 0, ErrNoLimit
}
