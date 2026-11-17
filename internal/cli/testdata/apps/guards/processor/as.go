package processor

import (
	"errors"

	"example.com/guards/gateway"
	"example.com/guards/payerr"
)

// ChargeAs handles the two concrete types with pointer and value targets.
func /*@asTargets*/ChargeAs(n int) error {
	err := gateway.Charge(n)
	var ce *payerr.CodeError
	if errors.As(err, &ce) {
		return nil
	}
	var ve payerr.ValidationError
	if errors.As(err, &ve) {
		return nil
	}
	return err
}

// ChargeAsCode only handles 402 responses; other CodeErrors are re-raised.
func /*@asCode*/ChargeAsCode(n int) error {
	err := gateway.Charge(n)
	var ce *payerr.CodeError
	if errors.As(err, &ce) && ce.Code == 402 {
		return nil
	}
	return err
}

// LookupAsTemporary handles everything that implements Temporary.
func /*@asIface*/LookupAsTemporary(n int) error {
	err := gateway.Lookup(n)
	var t payerr.Temporary
	if errors.As(err, &t) {
		return nil
	}
	return err
}

// CaptureAs finds a CodeError behind an OpError and behind nothing.
func /*@asUnwrap*/CaptureAs(n int) error {
	err := gateway.Capture(n)
	var ce *payerr.CodeError
	if errors.As(err, &ce) {
		return nil
	}
	return err
}

// CaptureAsOp handles the wrapper type itself.
func /*@asWrapper*/CaptureAsOp(n int) error {
	err := gateway.Capture(n)
	var oe *payerr.OpError
	if !errors.As(err, &oe) {
		return err
	}
	return nil
}

// QuirkyAs asks for a *CodeError from errors with custom Is / As methods.
func /*@customAs*/QuirkyAs(n int) error {
	err := gateway.Quirky(n)
	var ce *payerr.CodeError
	if errors.As(err, &ce) {
		return nil
	}
	return err
}

// ChargeCommaOk handles a CodeError found with a comma-ok assertion.
func /*@commaOk*/ChargeCommaOk(n int) error {
	err := gateway.Charge(n)
	if _, ok := err.(*payerr.CodeError); ok {
		return nil
	}
	return err
}

// CaptureCommaOk asserts the top-level type only; wrapped CodeErrors pass.
func /*@commaOkWrapped*/CaptureCommaOk(n int) error {
	err := gateway.Capture(n)
	if ce, ok := err.(*payerr.CodeError); ok && ce != nil {
		return nil
	}
	return err
}

// ChargeMustCode panics unless the error is a *CodeError.
func /*@singleAssert*/ChargeMustCode(n int) error {
	err := gateway.Charge(n)
	if err == nil {
		return nil
	}
	ce := err.(*payerr.CodeError)
	ce.Msg = "charge: " + ce.Msg
	return err
}

// ChargeTypeSwitch drops two types in one case and re-raises the rest.
func /*@typeSwitch*/ChargeTypeSwitch(n int) error {
	err := gateway.Charge(n)
	switch err.(type) {
	case nil:
		return nil
	case *payerr.CodeError, payerr.ValidationError:
		return nil
	default:
		return err
	}
}

// LookupTypeSwitch has an interface case, a wrapper case and a bound variable.
func /*@typeSwitchIface*/LookupTypeSwitch(n int) error {
	switch e := gateway.Lookup(n).(type) {
	case payerr.Temporary:
		return nil
	case *payerr.OpError:
		return e.Err
	case nil:
		return nil
	default:
		return e
	}
}

// CaptureTypeSwitch only re-raises CodeErrors that are not wrapped.
func /*@typeSwitchOnly*/CaptureTypeSwitch(n int) error {
	err := gateway.Capture(n)
	switch err.(type) {
	case *payerr.CodeError:
		return err
	}
	return nil
}
