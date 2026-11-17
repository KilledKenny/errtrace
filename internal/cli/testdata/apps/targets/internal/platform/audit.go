//go:build fast && audit

package platform

import (
	"errors"
	"fmt"
)

// ErrAuditOff is returned when auditing is compiled in but not enabled.
var ErrAuditOff = errors.New("platform: audit off")

// Audit verifies the fast path under audit.
func /*@audit*/Audit() error {
	if _, err := MaxBatch(); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return ErrAuditOff
}
