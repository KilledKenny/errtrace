package dispatch

import (
	"errors"
	"fmt"
	"io"

	"example.com/plugins/core"
	"example.com/plugins/drivers"
	"example.com/plugins/sink"
)

var (
	ErrBadLimit  = errors.New("dispatch: bad limit")
	ErrNoCert    = errors.New("dispatch: no certificate")
	ErrSpoolBusy = errors.New("dispatch: spool busy")
)

// Hooks are optional callbacks around a delivery.
type Hooks struct {
	Before func(core.Event) error
	After  func(core.Event) error
}

// Dispatcher sends events to a primary driver with an optional fallback.
type Dispatcher struct {
	name     string
	limit    int
	pending  int
	primary  core.Driver
	fallback core.Driver
	auditor  core.Auditor           // never set
	onDrop   func(core.Event) error // never set
	hooks    Hooks
	closer   io.Closer
}

// Option configures a Dispatcher.
type Option func(*Dispatcher) error

func WithLimit(n int) Option {
	return func(d *Dispatcher) error {
		if n < 0 {
			return ErrBadLimit
		}
		d.limit = n
		return nil
	}
}

func WithName(name string) Option {
	return /*@withname_lit*/func(d *Dispatcher) error {
		if name == "" {
			return errors.New("dispatch: empty name")
		}
		d.name = name
		return nil
	}
}

func WithFallback(drv core.Driver) Option {
	return func(d *Dispatcher) error {
		d.fallback = drv
		return nil
	}
}

// WithTLS is not used by any caller.
func WithTLS(cert string) Option {
	return func(d *Dispatcher) error {
		if cert == "" {
			return ErrNoCert
		}
		return nil
	}
}

// New builds a Dispatcher and applies the options in order.
func /*@new*/New(primary core.Driver, opts ...Option) (*Dispatcher, error) {
	d := &Dispatcher{primary: primary, limit: 10, closer: &spool{}}
	for _, opt := range opts {
		if err := /*@opt_call*/opt(d); err != nil {
			return nil, fmt.Errorf("dispatch.New: %w", err)
		}
	}
	return d, nil
}

func (d *Dispatcher) SetHooks(h Hooks) { d.hooks = h }

// Validate is the usual Before hook.
func Validate(e core.Event) error {
	if e.Body == "" {
		return core.ErrEmptyBody
	}
	return nil
}

// Send runs the hooks around primary, falling back when it fails.
func (d *Dispatcher) /*@disp_send*/Send(e core.Event) error {
	if d.hooks.Before != nil {
		if err := d.hooks./*@before*/Before(e); err != nil {
			return fmt.Errorf("before: %w", err)
		}
	}
	err := d.primary.Send(e)
	if err != nil && d.fallback != nil {
		err = d.fallback./*@fallback*/Send(e)
	}
	if err != nil {
		return err
	}
	if d.hooks.After != nil {
		return d.hooks.After(e)
	}
	return nil
}

// Audit forwards to the auditor, which nobody ever sets.
func (d *Dispatcher) /*@audit*/Audit(e core.Event) error {
	if d.auditor == nil {
		return nil
	}
	return d.auditor./*@audit_call*/Audit(e)
}

// Drop calls the onDrop callback, which nobody ever sets.
func (d *Dispatcher) /*@drop*/Drop(e core.Event) error {
	if d.onDrop == nil {
		return core.ErrDropped
	}
	return d.onDrop(e)
}

// Probe uses a driver that is still to be wired: the interface is nil.
func /*@probe*/Probe(e core.Event) error {
	var d core.Driver
	return d.Send(e)
}

type spool struct{ busy bool }

func (s *spool) Close() error {
	if s.busy {
		return ErrSpoolBusy
	}
	return nil
}

func (d *Dispatcher) flush() error {
	if d.pending > d.limit {
		return core.ErrDropped
	}
	return nil
}

// Close flushes; a deferred closure reports the closer's error when the
// flush itself succeeded.
func (d *Dispatcher) /*@close*/Close() (err error) {
	defer func() {
		if cerr := d.closer.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	return d.flush()
}

// Shutdown flushes and discards the closer's error.
func (d *Dispatcher) /*@shutdown*/Shutdown() error {
	defer /*@defer_recv*/d.closer./*@defer_close*/Close()
	return d.flush()
}

// Async delivers on another goroutine and waits for the result.
func (d *Dispatcher) /*@async*/Async(h core.Handler, e core.Event) error {
	errc := make(chan error, 1)
	go func() { errc <- h./*@go_handle*/Handle(e) }()
	return <-errc
}

// Fire delivers without waiting.
func Fire(h core.Handler, e core.Event) {
	go h./*@go_stmt*/Handle(e)
}

// deliver is the one dynamic call site shared by the Alert* functions.
func /*@deliver*/deliver(d core.Driver, e core.Event) error { return d.Send(e) }

// AlertOps pages the on-call engineer.
func AlertOps(e core.Event) error {
	return /*@ops_call*/deliver(drivers.Pager{}, e)
}

// AlertUser mails the user and then texts them.
func /*@alertuser*/AlertUser(e core.Event, sms *drivers.SMS) error {
	if err := deliver(drivers.Email{}, e); err != nil {
		return err
	}
	return deliver(sms, e)
}

// AlertVia forwards a driver chosen by its own caller.
func /*@alertvia*/AlertVia(d core.Driver, e core.Event) error {
	return deliver(d, e)
}

func pick(kind string) core.Driver {
	switch kind {
	case "email":
		return drivers.Email{}
	case "sms":
		return &drivers.SMS{}
	case "remote":
		return sink.Remote{Addr: "10.0.0.1"}
	}
	return nil
}

// Route selects a driver by name.
func /*@route*/Route(kind string, e core.Event) error {
	d := pick(kind)
	if d == nil {
		return core.ErrNoRoute
	}
	return d./*@route_send*/Send(e)
}

// Cycle sends on a Channel and closes it.
func /*@cycle*/Cycle(c drivers.Channel, e core.Event) error {
	if err := c.Send(e); err != nil {
		c.Close()
		return err
	}
	return c.Close()
}

// Release closes the drivers that can be closed.
func /*@release*/Release(d core.Driver) error {
	if c, ok := d.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// SendQuietly ignores an over-long text message.
func /*@quietly*/SendQuietly(e core.Event) error {
	err := SendPair(&drivers.SMS{}, e)
	if errors.Is(err, drivers.ErrSMS) {
		return nil
	}
	return err
}

// SendNoHTTP treats an HTTP failure of the webhook as delivered.
func /*@nohttp*/SendNoHTTP(e core.Event) error {
	err := SendSeven(&drivers.Webhook{}, e)
	var he *drivers.HTTPError
	if errors.As(err, &he) {
		return nil
	}
	return err
}
