// Package jobs runs import steps under a context.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrShutdown is the cancellation cause used when the importer shuts down.
var ErrShutdown = errors.New("jobs: shutting down")

// ErrNoHook is returned when the context carries no retry hook.
var ErrNoHook = errors.New("jobs: no hook in context")

type hookKey struct{}

// Hook is a callback stored in a context value.
type Hook func(step string) error

// WithHook stores hook in the context.
func WithHook(ctx context.Context, hook Hook) context.Context {
	return context.WithValue(ctx, hookKey{}, hook)
}

// Check returns the context's error, if it is done.
func /*@check*/Check(ctx context.Context) error {
	return ctx./*@ctxerr*/Err()
}

// Why returns the cancellation cause.
func /*@why*/Why(ctx context.Context) error {
	return context./*@cause*/Cause(ctx)
}

// Wait blocks until d elapsed or ctx is done. A deadline is not a failure
// for the caller, so DeadlineExceeded is filtered out.
func /*@wait*/Wait(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		err := ctx.Err()
		if errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return fmt.Errorf("jobs: wait: %w", err)
	}
}

// Stop classifies a finished step: only the two context sentinels pass.
func /*@stop*/Stop(done bool, timedOut bool) error {
	switch {
	case !done:
		return nil
	case timedOut:
		return context.DeadlineExceeded
	}
	return context.Canceled
}

// Notify calls the hook stored in the context.
func /*@notify*/Notify(ctx context.Context, step string) error {
	hook, ok := ctx.Value(hookKey{}).(Hook)
	if !ok {
		return ErrNoHook
	}
	return /*@hookcall*/hook(step)
}

// Run executes step under a cancellable context whose cause is ErrShutdown.
func /*@run*/Run(parent context.Context, step func(context.Context) error) error {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(ErrShutdown)
	if err := /*@stepcall*/step(ctx); err != nil {
		return fmt.Errorf("jobs: step: %w", err)
	}
	return nil
}
