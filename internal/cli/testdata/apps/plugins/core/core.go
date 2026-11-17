// Package core holds the types every plugin shares.
package core

import "errors"

// Event is one notification travelling through the system.
type Event struct {
	Topic    string
	Body     string
	Priority int
}

// Errors shared by all packages.
var (
	ErrNoRoute   = errors.New("no route")
	ErrDropped   = errors.New("event dropped")
	ErrEmptyBody = errors.New("empty body")
)

// Handler consumes an Event.
type Handler interface {
	/*@handle_iface*/Handle(e Event) error
}

// HandlerFunc adapts a function to a Handler.
type HandlerFunc func(Event) error

// Handle calls f.
func (f HandlerFunc) /*@hf_handle*/Handle(e Event) error { return f(e) }

// Named is implemented by plugins that have a display name.
type Named interface{ Name() string }

// Plugin is a named Handler (two embedded interfaces).
type Plugin interface {
	Named
	Handler
}

// Driver delivers an Event to the outside world.
type Driver interface{ Send(e Event) error }

// Auditor records delivered events. Nothing implements it yet.
type Auditor interface{ Audit(e Event) error }
