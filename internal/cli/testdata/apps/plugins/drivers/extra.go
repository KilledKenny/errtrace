package drivers

import (
	"errors"
	"fmt"
	"io"
	"strconv"

	"example.com/plugins/core"
)

var (
	ErrCarrier    = errors.New("carrier: pigeon lost")
	ErrFax        = errors.New("fax: paper jam")
	ErrBase       = errors.New("base: not configured")
	ErrConnReset  = errors.New("conn: reset")
	ErrConnClosed = errors.New("conn: already closed")
)

// Carrier is a Driver that no code ever instantiates.
type Carrier struct{}

func (Carrier) Send(e core.Event) error { return ErrCarrier }

// Fax is instantiated, but only ever called directly: it never becomes a
// core.Driver value.
type Fax struct{}

func (Fax) Send(e core.Event) error { return ErrFax }

// Logged embeds the Driver interface: Send is promoted from whatever
// Driver the field holds.
type Logged struct {
	core.Driver
	Prefix string
}

type base struct{}

func (base) Send(e core.Event) error { return ErrBase }

// Fancy gets Send promoted from an embedded struct.
type Fancy struct {
	base
	Color string
}

// Strict gets Send promoted through an embedded pointer.
type Strict struct{ *SMS }

// Channel is a Driver that must be closed (embedded interfaces, one of
// them from the standard library).
type Channel interface {
	core.Driver
	io.Closer
}

// Conn is the only Channel.
type Conn struct{ open bool }

func (c *Conn) Send(e core.Event) error {
	if !c.open {
		return ErrConnReset
	}
	return nil
}

func (c *Conn) Close() error {
	if !c.open {
		return ErrConnClosed
	}
	c.open = false
	return nil
}

// Status is both a fmt.Stringer and an error.
type Status int

func (s Status) String() string { return "status " + strconv.Itoa(int(s)) }
func (s Status) Error() string  { return s.String() }

// ErrBusy is a sentinel of the concrete type Status.
var ErrBusy error = Status(429)

// Gateway reports failures as Status values.
type Gateway struct{ Up bool }

func (g Gateway) /*@gw_send*/Send(e core.Event) error {
	if !g.Up {
		return /*@status_conv*/Status(503)
	}
	if e.Priority > 9 {
		return ErrBusy
	}
	return nil
}

// Audited decorates a Driver. It embeds the interface but declares its own
// Send, which shadows the promoted one.
type Audited struct{ core.Driver }

func (a Audited) Send(e core.Event) error {
	if err := a.Driver.Send(e); err != nil {
		return fmt.Errorf("audited: %w", err)
	}
	return nil
}
