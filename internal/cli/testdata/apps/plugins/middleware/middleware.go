// Package middleware decorates handlers.
package middleware

import (
	"errors"
	"fmt"

	"example.com/plugins/core"
)

// ErrGaveUp is joined to the last error once Retry stops trying.
var ErrGaveUp = errors.New("gave up")

// Middleware wraps a Handler in another one.
type Middleware func(core.Handler) core.Handler

// Chain applies mws so that the first one is outermost.
func /*@chain*/Chain(h core.Handler, mws ...Middleware) core.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// Tag prefixes errors with a label.
func Tag(label string) Middleware {
	return func(next core.Handler) core.Handler {
		return core.HandlerFunc(func(e core.Event) error {
			if err := next.Handle(e); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
			return nil
		})
	}
}

// Filter drops events of other topics.
func Filter(topic string) Middleware {
	return func(next core.Handler) core.Handler {
		return core.HandlerFunc(func(e core.Event) error {
			if e.Topic != topic {
				return core.ErrDropped
			}
			return next.Handle(e)
		})
	}
}

// Retry calls next up to n times.
func Retry(n int) Middleware {
	return func(next core.Handler) core.Handler {
		return core.HandlerFunc(func(e core.Event) error {
			var err error
			for i := 0; i < n; i++ {
				if err = next.Handle(e); err == nil {
					return nil
				}
			}
			return fmt.Errorf("%w: %w", ErrGaveUp, err)
		})
	}
}

// Recover turns a panic of next into an error.
func Recover() Middleware {
	return func(next core.Handler) core.Handler {
		return core.HandlerFunc(/*@recover_lit*/func(e core.Event) (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			return next.Handle(e)
		})
	}
}
