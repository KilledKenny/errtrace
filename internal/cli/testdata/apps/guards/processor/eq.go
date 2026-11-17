// Package processor holds the payment handlers. Each handler inspects the
// gateway's error and handles, translates or re-raises it.
package processor

import (
	"fmt"

	"example.com/guards/gateway"
	"example.com/guards/payerr"
)

// ChargeIgnoreDeclined treats a declined card as a normal outcome.
func /*@ignoreDeclined*/ChargeIgnoreDeclined(n int) error {
	err := gateway.Charge(n)
	if err == payerr.ErrDeclined {
		return nil
	}
	return err
}

// ChargeNeqElse handles the timeout in the else branch of a != check.
func /*@neqElse*/ChargeNeqElse(n int) error {
	err := gateway.Charge(n)
	if err != payerr.ErrTimeout {
		return err
	} else {
		return nil
	}
}

// LookupEqWrapped compares a wrapped error by identity, which never matches.
func /*@eqWrapped*/LookupEqWrapped(n int) error {
	err := gateway.Lookup(n)
	if err == payerr.ErrNotFound || err == payerr.ErrTimeout || err == payerr.ErrRateLimited {
		return nil
	}
	return err
}

// ChargeOnlyDeclined returns the error itself in the guarded branch.
func /*@onlyDeclined*/ChargeOnlyDeclined(n int) error {
	err := gateway.Charge(n)
	if err == payerr.ErrDeclined {
		return err
	}
	return nil
}

// ChargeElseChain translates, drops and re-raises in an if/else-if chain.
func /*@elseChain*/ChargeElseChain(n int) error {
	err := gateway.Charge(n)
	if err == nil {
		return nil
	} else if err == payerr.ErrDeclined {
		return payerr.ErrDuplicate
	} else if err == payerr.ErrTimeout {
		return nil
	} else {
		return err
	}
}

// ChargeSwitch drops two sentinels in one case and re-raises the rest.
func /*@valueSwitch*/ChargeSwitch(n int) error {
	err := gateway.Charge(n)
	switch err {
	case nil:
		return nil
	case payerr.ErrDeclined, payerr.ErrTimeout:
		return nil
	default:
		return err
	}
}

// ChargeSwitchKeep re-raises one case, translates another and drops the default.
func /*@valueSwitchKeep*/ChargeSwitchKeep(n int) error {
	err := gateway.Charge(n)
	switch err {
	case payerr.ErrDeclined:
		return err
	case payerr.ErrTimeout:
		return payerr.ErrInternal
	default:
		return nil
	}
}

// ChargeAliasCopy checks a renamed copy of the error.
func /*@aliasCopy*/ChargeAliasCopy(n int) error {
	err := gateway.Charge(n)
	cause := err
	if cause == payerr.ErrDeclined {
		return nil
	}
	return err
}

// ChargeVoidOther checks one variable and returns another.
func /*@otherVar*/ChargeVoidOther(n int) error {
	cerr := gateway.Charge(n)
	verr := gateway.Void(n)
	if cerr == payerr.ErrDeclined {
		return verr
	}
	return cerr
}

// ChargeThenVoid checks the error and then overwrites it.
func /*@reassign*/ChargeThenVoid(n int) error {
	err := gateway.Charge(n)
	if err == payerr.ErrDeclined || err == payerr.ErrTimeout {
		return nil
	}
	if err != nil {
		return err
	}
	err = gateway.Void(n)
	return err
}

// ChargeSameAsFirst compares against a local error value, not a sentinel.
func /*@localCompare*/ChargeSameAsFirst(n int) error {
	first := gateway.Void(1)
	err := gateway.Void(n)
	if err == first {
		return nil
	}
	return err
}

// ChargeWrapThenEq wraps first, so the identity check can never match.
func /*@wrapThenEq*/ChargeWrapThenEq(n int) error {
	err := gateway.Void(n)
	if err != nil {
		err = fmt.Errorf("void %d: %w", n, err)
	}
	if err == payerr.ErrDeclined {
		return nil
	}
	return err
}

// ValidateSentinels compares against value-type and zero-size sentinels.
func /*@valueSentinels*/ValidateSentinels(n int) error {
	err := gateway.Validate(n)
	if err == payerr.ErrBadAmount || err == payerr.ErrEmpty {
		return nil
	}
	return err
}

// PrecheckTypedNil drops only the typed nil *CodeError.
func /*@typedNil*/PrecheckTypedNil(n int) error {
	err := gateway.Precheck(n)
	if err == (*payerr.CodeError)(nil) {
		return nil
	}
	return err
}

// PrecheckNilCheck forwards whatever is non-nil, including a typed nil.
func /*@nilCheck*/PrecheckNilCheck(n int) error {
	if err := gateway.Precheck(n); err != nil {
		return err
	}
	return nil
}

// PrecheckInlineNilCheck is PrecheckNilCheck on the helper-free gateway call.
func /*@nilCheckInline*/PrecheckInlineNilCheck(n int) error {
	if err := gateway.PrecheckInline(n); err != nil {
		return err
	}
	return nil
}
