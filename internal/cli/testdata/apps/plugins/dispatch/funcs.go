package dispatch

import (
	"errors"
	"fmt"

	"example.com/plugins/core"
	"example.com/plugins/drivers"
)

var ErrRateLimited = errors.New("dispatch: rate limited")

// Lazy captures err, then changes it before the closure runs.
func /*@lazy*/Lazy(e core.Event) error {
	err := core.ErrDropped
	report := func() error { return err }
	if e.Body == "" {
		err = core.ErrEmptyBody
	}
	return report()
}

func each(es []core.Event, fn func(core.Event)) {
	for _, e := range es {
		fn(e)
	}
}

// FirstError lets a callback without results write a captured variable.
func /*@firsterr*/FirstError(d core.Driver, es []core.Event) error {
	var first error
	each(es, func(e core.Event) {
		if err := d.Send(e); err != nil && first == nil {
			first = fmt.Errorf("batch: %w", err)
		}
	})
	return first
}

func collect(dst *error, d core.Driver, e core.Event) {
	if err := d.Send(e); err != nil {
		*dst = err
	}
}

// ByAddr hands the address of its result variable to a helper.
func /*@byaddr*/ByAddr(d core.Driver, e core.Event) error {
	var last error
	collect(&last, d, e)
	return last
}

// Limit returns a stateful check: a function returned from a function.
func Limit(max int) func(core.Event) error {
	n := 0
	return func(core.Event) error {
		n++
		if n > max {
			return ErrRateLimited
		}
		return nil
	}
}

// Limited calls the function Limit returned.
func /*@limited*/Limited(e core.Event) error {
	check := Limit(3)
	return check(e)
}

func /*@retry*/retry(n int, e core.Event, fn func(core.Event) error) (err error) {
	for i := 0; i < n; i++ {
		if err = fn(e); err == nil {
			return nil
		}
	}
	return err
}

// RetrySMS passes a method value of a concrete type.
func RetrySMS(sms *drivers.SMS, e core.Event) error {
	return /*@retrysms_call*/retry(2, e, sms.Send)
}

// RetryValidate passes a plain function.
func RetryValidate(e core.Event) error {
	return retry(1, e, Validate)
}

// RetryDriver passes a method value bound to an interface.
func /*@retrydriver*/RetryDriver(d core.Driver, e core.Event) error {
	send := d./*@mv_noncall*/Send
	return retry(3, e, send)
}

// ViaMethodExpr calls through method expressions.
func /*@mexpr*/ViaMethodExpr(e core.Event) error {
	send := (*drivers.SMS).Send
	if err := send(&drivers.SMS{}, e); err != nil {
		return err
	}
	mail := drivers.Email.Send
	return mail(drivers.Email{}, e)
}

// ViaIfaceMethodExpr calls through an interface method expression.
func /*@imexpr*/ViaIfaceMethodExpr(h core.Handler, e core.Event) error {
	handle := core.Handler.Handle
	return handle(h, e)
}
