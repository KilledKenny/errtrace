// Package drivers contains the built-in delivery drivers.
package drivers

import (
	"errors"
	"fmt"

	"example.com/plugins/core"
)

// One sentinel per driver, so that every Candidate is recognisable.
var (
	ErrEmail = errors.New("email: no body")
	ErrSMS   = errors.New("sms: message too long")
	ErrSlack = errors.New("slack: no channel")
	ErrPager = errors.New("pager: nobody on call")
	ErrFile  = errors.New("file: no path")
	ErrQueue = errors.New("queue: full")
)

// Email has a value receiver.
type Email struct{ From string }

func (Email) Send(e core.Event) error {
	if e.Body == "" {
		return ErrEmail
	}
	return nil
}

// SMS has a pointer receiver.
type SMS struct{ sent int }

func (s *SMS) Send(e core.Event) error {
	s.sent++
	if len(e.Body) > 160 {
		return ErrSMS
	}
	return nil
}

// Slack wraps its sentinel.
type Slack struct{ Channel string }

func (s Slack) Send(e core.Event) error {
	if s.Channel == "" {
		return fmt.Errorf("slack %q: %w", e.Topic, ErrSlack)
	}
	return nil
}

// HTTPError is the concrete error of Webhook.
type HTTPError struct{ Code int }

func (e *HTTPError) Error() string { return fmt.Sprintf("http %d", e.Code) }

// Webhook returns a concrete error type.
type Webhook struct{ URL string }

func (w *Webhook) Send(e core.Event) error {
	if w.URL == "" {
		return &HTTPError{Code: 404}
	}
	return nil
}

// Push returns an opaque error.
type Push struct{}

func (Push) Send(e core.Event) error {
	if e.Priority < 0 {
		return errors.New("push: negative priority")
	}
	return nil
}

// Pager wakes somebody up.
type Pager struct{}

func (Pager) Send(e core.Event) error {
	if e.Priority < 5 {
		return ErrPager
	}
	return nil
}

// File appends to a file.
type File struct{ Path string }

func (f File) Send(e core.Event) error {
	if f.Path == "" {
		return ErrFile
	}
	return nil
}

// Queue buffers events.
type Queue struct{ Cap int }

func (q Queue) Send(e core.Event) error {
	if q.Cap == 0 {
		return ErrQueue
	}
	return nil
}

// Log never fails.
type Log struct{}

func (Log) Send(e core.Event) error {
	fmt.Println("event:", e.Topic)
	return nil
}
