// Package gateway is a fake payment gateway; every call fails in a way
// selected by n.
package gateway

import (
	"errors"
	"fmt"

	"example.com/guards/payerr"
)

// Charge fails with two sentinels, two concrete types and an opaque error.
func Charge(n int) error {
	switch n {
	case 0:
		return nil
	case 1:
		return payerr.ErrDeclined
	case 2:
		return payerr.ErrTimeout
	case 3:
		return &payerr.CodeError{Code: 402, Msg: "payment required"}
	case 4:
		return payerr.ValidationError{Field: "currency"}
	}
	return errors.New("gateway down")
}

// Lookup fails with wrapped errors: fmt.Errorf %w, a custom Unwrap type, a
// bare sentinel and a Temporary transport error.
func Lookup(n int) error {
	switch n {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("lookup %d: %w", n, payerr.ErrNotFound)
	case 2:
		return &payerr.OpError{Op: "lookup", Err: payerr.ErrTimeout}
	case 3:
		return payerr.ErrRateLimited
	}
	return &payerr.NetError{Host: "gw-1"}
}

// Refund fails with a join of two sentinels or a bare sentinel.
func Refund(n int) error {
	if n == 1 {
		return errors.Join(payerr.ErrDeclined, payerr.ErrDuplicate)
	}
	if n == 2 {
		return payerr.ErrInternal
	}
	return nil
}

// Void fails with a subset of what Charge returns.
func Void(n int) error {
	if n == 1 {
		return payerr.ErrDeclined
	}
	if n == 2 {
		return payerr.ErrInternal
	}
	return nil
}

// Capture fails with a CodeError hidden behind an OpError and a bare one.
func Capture(n int) error {
	if n == 1 {
		return &payerr.OpError{Op: "capture", Err: &payerr.CodeError{Code: 409, Msg: "conflict"}}
	}
	if n == 2 {
		return &payerr.CodeError{Code: 500, Msg: "boom"}
	}
	if n == 3 {
		return fmt.Errorf("capture: %w", payerr.ErrDeclined)
	}
	return nil
}

// Quirky fails with errors that have custom Is / As methods.
func Quirky(n int) error {
	if n == 1 {
		return &payerr.FlexError{Kind: "notfound"}
	}
	if n == 2 {
		return &payerr.ShapeError{Code: 7}
	}
	if n == 3 {
		return payerr.ErrNotFound
	}
	return nil
}

// Validate fails with value-type and zero-size errors, fresh and sentinel.
func Validate(n int) error {
	switch n {
	case 1:
		return payerr.ErrBadAmount
	case 2:
		return payerr.ValidationError{Field: "amount"}
	case 3:
		return payerr.ErrEmpty
	case 4:
		return payerr.Empty{}
	}
	return nil
}

func check(n int) *payerr.CodeError {
	if n > 10 {
		return &payerr.CodeError{Code: n, Msg: "too large"}
	}
	return nil
}

// Precheck returns a typed nil *CodeError for small n.
func Precheck(n int) error {
	return check(n)
}

// PrecheckInline is Precheck without the helper: the nil pointer is a local.
func PrecheckInline(n int) error {
	var ce *payerr.CodeError
	if n > 10 {
		ce = &payerr.CodeError{Code: n, Msg: "too large"}
	}
	return ce
}
