// Package api is the transport-facing layer: it decodes requests, calls the
// service and translates every failure into an *Error.
package api

import (
	"context"
	"errors"
	"fmt"
	"log"

	"example.com/bank/domain"
	"example.com/bank/middleware"
	"example.com/bank/parse"
	"example.com/bank/service"
)

// Error is the only error type clients ever see.
type Error struct {
	Code    int
	Message string
	Cause   error
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s", e.Code, e.Message) }
func (e *Error) Unwrap() error { return e.Cause }

// ErrUnavailable is sent when the request was cancelled or timed out.
var ErrUnavailable = &Error{Code: 503, Message: "try again later"}

// Translate maps any error to an *Error. Only a duplicate transfer keeps its
// cause; every other domain or infrastructure error is replaced.
func /*@translate*/Translate(err error) error {
	if err == nil {
		return nil
	}
	var ve *domain.ValidationError
	var short domain.InsufficientFundsError
	switch {
	case errors.As(err, &ve):
		return &Error{Code: 400, Message: ve.Error()}
	case errors.Is(err, domain.ErrDuplicate):
		return &Error{Code: 409, Message: "already executed", Cause: err}
	case errors.Is(err, domain.ErrNotFound):
		return &Error{Code: 404, Message: "no such account"}
	case errors.As(err, &short):
		return &Error{Code: 402, Message: "insufficient funds"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ErrUnavailable
	}
	log.Printf("api: internal error: %v", err)
	return &Error{Code: 500, Message: "internal error"}
}

// Server routes requests to handlers.
type Server struct {
	svc    *service.Service
	routes map[string]middleware.Handler
}

// NewServer builds the routing table. Business routes run behind the full
// middleware chain; the health route is public.
func NewServer(svc *service.Service, token string) *Server {
	s := &Server{svc: svc}
	protect := func(h middleware.Handler) middleware.Handler {
		return middleware.Chain(h,
			middleware.Recover(), middleware.MapErrors(Translate), middleware.Deadline(), middleware.Auth(token))
	}
	s.routes = map[string]middleware.Handler{
		"/transfer": protect(s.handleTransfer),
		"/balance":  protect(s.handleBalance),
		"/health":   s.handleHealth,
	}
	return s
}

// Serve dispatches one request.
func (s *Server) /*@serve*/Serve(ctx context.Context, req middleware.Request) error {
	h, ok := s.routes[req.Path]
	if !ok {
		return &Error{Code: 404, Message: "no route " + req.Path}
	}
	return /*@route_call*/h(ctx, req)
}

func (s *Server) /*@handle_transfer*/handleTransfer(ctx context.Context, req middleware.Request) error {
	t, err := parse.Request(req.Body)
	if err != nil {
		return err
	}
	return s.svc.Transfer(ctx, t)
}

func (s *Server) /*@handle_balance*/handleBalance(ctx context.Context, req middleware.Request) error {
	_, err := s.svc.Balance(ctx, domain.AccountID(req.Body))
	return err
}

// handleHealth never fails: a broken store is logged, not reported.
func (s *Server) /*@handle_health*/handleHealth(ctx context.Context, _ middleware.Request) error {
	if _, err := s.svc.Exists(ctx, "health-probe"); err != nil {
		log.Printf("api: health: %v", err)
	}
	return nil
}

// Transfer is the in-process form of POST /transfer: no middleware, but the
// same translation.
func (s *Server) /*@transfer*/Transfer(ctx context.Context, body []byte) error {
	return /*@translate_call*/Translate(s.handleTransfer(ctx, middleware.Request{Path: "/transfer", Body: body}))
}

// Balance is the in-process form of GET /balance.
func (s *Server) /*@balance*/Balance(ctx context.Context, id string) error {
	return Translate(s.handleBalance(ctx, middleware.Request{Path: "/balance", Body: []byte(id)}))
}
