// Package sink is a third-party style driver living outside drivers.
package sink

import (
	"errors"
	"fmt"

	"example.com/plugins/core"
)

// ErrRemote is returned when the remote end is not reachable.
var ErrRemote = errors.New("remote unreachable")

// Remote implements core.Driver.
type Remote struct{ Addr string }

func (r Remote) Send(e core.Event) error {
	if r.Addr == "" {
		return fmt.Errorf("remote %s: %w", e.Topic, ErrRemote)
	}
	return nil
}
