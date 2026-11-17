// Package retry re-runs operations that fail with transient errors.
package retry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrBusy marks a resource that is temporarily locked.
	ErrBusy = errors.New("resource busy")
	// ErrGaveUp wraps the last transient error once the attempts are used up.
	ErrGaveUp = errors.New("retry: gave up")
)

// TimeoutError is a transient error of a single operation.
type TimeoutError struct{ Op string }

func (e *TimeoutError) Error() string { return e.Op + ": timed out" }

// Backoff is the pause between attempts.
var Backoff = time.Millisecond

// Do runs op up to attempts times. Only ErrBusy and *TimeoutError are
// retried; any other error is returned as is.
func /*@do*/Do(ctx context.Context, attempts int, op func() error) error {
	var last error
	for i := 0; i < attempts; i++ {
		err := op()
		if err == nil {
			return nil
		}
		var te *TimeoutError
		if !errors.Is(err, ErrBusy) && !errors.As(err, &te) {
			return err
		}
		last = err
		if err := /*@sleep_call*/sleep(ctx, Backoff); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w after %d attempts: %w", ErrGaveUp, attempts, last)
}

// Value is Do for operations that also produce a value.
func /*@value*/Value[T any](ctx context.Context, attempts int, op func() (T, error)) (T, error) {
	var out T
	err := Do(ctx, attempts, func() (err error) {
		out, err = op()
		return err
	})
	return out, err
}

// sleep waits for d or until ctx is done.
func /*@sleep*/sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("retry: interrupted: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}
