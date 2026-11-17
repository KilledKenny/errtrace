package processor

import (
	"errors"
	"fmt"

	"example.com/guards/gateway"
	"example.com/guards/payerr"
)

// errGone is a package-level alias of the not-found sentinel.
var errGone = payerr.ErrNotFound

// ChargeAsWrongTarget asks for a *ValidationError, but the gateway returns
// ValidationError values, so errors.As never matches.
func /*@asWrongTarget*/ChargeAsWrongTarget(n int) error {
	err := gateway.Charge(n)
	var ve *payerr.ValidationError
	if errors.As(err, &ve) {
		return nil
	}
	return err
}

// LookupIsTarget swallows whatever kind the caller names.
func /*@isParamTarget*/LookupIsTarget(n int, target error) error {
	err := gateway.Lookup(n)
	if errors.Is(err, target) {
		return nil
	}
	return err
}

// LookupIsFlag stores the result of the check in a variable first.
func /*@isBoolVar*/LookupIsFlag(n int) error {
	err := gateway.Lookup(n)
	notFound := errors.Is(err, payerr.ErrNotFound)
	if notFound {
		return nil
	}
	return err
}

// LookupIsAlias checks against the package-level alias errGone.
func /*@isAlias*/LookupIsAlias(n int) error {
	err := gateway.Lookup(n)
	if errors.Is(err, errGone) {
		return nil
	}
	return err
}

// PrecheckOnlyTypedNil returns nothing but the typed nil *CodeError.
func /*@onlyTypedNil*/PrecheckOnlyTypedNil(n int) error {
	err := gateway.Precheck(n)
	if err == (*payerr.CodeError)(nil) {
		return err
	}
	return nil
}

// PrecheckInlineOnlyTypedNil is PrecheckOnlyTypedNil on the helper-free call.
func /*@onlyTypedNilInline*/PrecheckInlineOnlyTypedNil(n int) error {
	err := gateway.PrecheckInline(n)
	if err == (*payerr.CodeError)(nil) {
		return err
	}
	return nil
}

// VoidDescribe wraps unconditionally (even a nil error) and then drops declines.
func /*@wrapNil*/VoidDescribe(n int) error {
	err := fmt.Errorf("void %d: %w", n, gateway.Void(n))
	if errors.Is(err, payerr.ErrDeclined) {
		return nil
	}
	return err
}

// ChargeClosureGuard guards through a local function literal.
func /*@closureGuard*/ChargeClosureGuard(n int) error {
	isDeclined := func(e error) bool { return e == payerr.ErrDeclined }
	err := gateway.Charge(n)
	if isDeclined(err) {
		return nil
	}
	return err
}

// ChargeIfVoidDeclined applies errors.Is to one error and returns another.
func /*@isOtherVar*/ChargeIfVoidDeclined(n int) error {
	cerr := gateway.Charge(n)
	verr := gateway.Void(n)
	if errors.Is(verr, payerr.ErrDeclined) {
		return cerr
	}
	return nil
}
