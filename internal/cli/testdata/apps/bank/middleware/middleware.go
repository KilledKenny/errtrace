// Package middleware wraps request handlers.
package middleware

import (
	"context"
	"errors"
	"fmt"
)

// ErrUnauthorized is returned by Auth for a wrong token.
var ErrUnauthorized = errors.New("unauthorized")

// Request is a transport-neutral request.
type Request struct {
	Path  string
	Token string
	Body  []byte
}

// Handler serves one request.
type Handler func(ctx context.Context, req Request) error

// Middleware decorates a Handler.
type Middleware func(Handler) Handler

// Chain applies mws to h; the first middleware is the outermost.
func /*@chain*/Chain(h Handler, mws ...Middleware) Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// Auth rejects requests without the expected token.
func Auth(token string) Middleware {
	return func(next Handler) Handler {
		return /*@auth_fn*/func(ctx context.Context, req Request) error {
			if req.Token != token {
				return ErrUnauthorized
			}
			return next(ctx, req)
		}
	}
}

// Deadline refuses to start work on a context that is already done.
func Deadline() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req Request) error {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("middleware: %s: %w", req.Path, err)
			}
			return next(ctx, req)
		}
	}
}

// Recover turns a panic in the handler into an error.
func Recover() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req Request) (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("middleware: panic serving %s: %v", req.Path, r)
				}
			}()
			return next(ctx, req)
		}
	}
}

// MapErrors passes every handler error through f.
func MapErrors(f func(error) error) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req Request) error {
			return f(next(ctx, req))
		}
	}
}

// Guarded runs h behind Auth only: a direct, statically known composition.
func /*@guarded*/Guarded(ctx context.Context, token string, req Request, h Handler) error {
	return /*@guarded_call*/Auth(token)(h)(ctx, req)
}
