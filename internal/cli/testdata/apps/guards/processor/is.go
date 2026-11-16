package processor

import (
	"errors"
	"fmt"
	"log"

	"example.com/guards/gateway"
	"example.com/guards/payerr"
)

// LookupIs handles not-found (fmt.Errorf wrapped) and timeout (OpError wrapped).
func /*@isWrapped*/LookupIs(n int) error {
	err := gateway.Lookup(n)
	if errors.Is(err, payerr.ErrNotFound) {
		return nil
	}
	if errors.Is(err, payerr.ErrTimeout) {
		return nil
	}
	return err
}

// LookupNotIs re-raises everything that is not a not-found.
func /*@notIs*/LookupNotIs(n int) error {
	err := gateway.Lookup(n)
	if !errors.Is(err, payerr.ErrNotFound) {
		return err
	}
	return nil
}

// LookupAndChain re-raises unless the error is one of two known kinds.
func /*@andChain*/LookupAndChain(n int) error {
	err := gateway.Lookup(n)
	if err != nil && !errors.Is(err, payerr.ErrRateLimited) && !errors.Is(err, payerr.ErrNotFound) {
		return err
	}
	return nil
}

// LookupOr drops two kinds with ||; force also drops everything else.
func /*@orChain*/LookupOr(n int, force bool) error {
	err := gateway.Lookup(n)
	if errors.Is(err, payerr.ErrRateLimited) || errors.Is(err, payerr.ErrTimeout) || force {
		return nil
	}
	return err
}

// LookupAndBudget only swallows a rate limit while there is retry budget.
func /*@andBudget*/LookupAndBudget(n, budget int) error {
	err := gateway.Lookup(n)
	if errors.Is(err, payerr.ErrRateLimited) && budget > 0 {
		return nil
	}
	return err
}

// LookupNested swallows a not-found only in lenient mode.
func /*@nested*/LookupNested(n int, lenient bool) error {
	err := gateway.Lookup(n)
	if err != nil {
		if errors.Is(err, payerr.ErrNotFound) {
			if lenient {
				return nil
			}
			return err
		}
		if errors.Is(err, payerr.ErrRateLimited) {
			return nil
		}
		return err
	}
	return nil
}

// RefundIsJoined handles a duplicate, which only arrives joined with a decline.
func /*@isJoined*/RefundIsJoined(n int) error {
	err := gateway.Refund(n)
	if errors.Is(err, payerr.ErrDuplicate) {
		return nil
	}
	return err
}

// RefundJoinMaybe joins a refund and a void error; the void part may be nil.
func /*@joinMaybe*/RefundJoinMaybe(n int) error {
	err := errors.Join(gateway.Refund(n), gateway.Void(n))
	if errors.Is(err, payerr.ErrInternal) {
		return nil
	}
	return err
}

// LookupTranslate turns a not-found into a duplicate and re-raises the rest.
func /*@translate*/LookupTranslate(n int) error {
	err := gateway.Lookup(n)
	if errors.Is(err, payerr.ErrNotFound) {
		return payerr.ErrDuplicate
	}
	return err
}

// LookupRewrap re-wraps only timeouts and swallows everything else.
func /*@rewrap*/LookupRewrap(n int) error {
	err := gateway.Lookup(n)
	if errors.Is(err, payerr.ErrTimeout) {
		return fmt.Errorf("fatal: %w", err)
	}
	return nil
}

// LookupLog logs a rate limit and still returns it.
func /*@logOnly*/LookupLog(n int) error {
	err := gateway.Lookup(n)
	if errors.Is(err, payerr.ErrRateLimited) {
		log.Printf("rate limited: %v", err)
	}
	return err
}

// LookupSwapOnIs replaces a handled timeout by the result of a second call.
func /*@swapOnIs*/LookupSwapOnIs(n int) error {
	err := gateway.Lookup(n)
	if errors.Is(err, payerr.ErrTimeout) {
		err = gateway.Lookup(n + 1)
	}
	return err
}

// LookupSwitchTrue uses a tagless switch of errors.Is cases.
func /*@switchTrue*/LookupSwitchTrue(n int) error {
	err := gateway.Lookup(n)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, payerr.ErrNotFound):
		return nil
	case errors.Is(err, payerr.ErrRateLimited):
		return payerr.ErrInternal
	}
	return err
}

// LookupUnwrapEq checks the unwrapped error, a different value than err.
func /*@unwrapEq*/LookupUnwrapEq(n int) error {
	err := gateway.Lookup(n)
	if errors.Unwrap(err) == payerr.ErrNotFound {
		return nil
	}
	return err
}

// QuirkyIs checks not-found against errors with custom Is / As methods.
func /*@customIs*/QuirkyIs(n int) error {
	err := gateway.Quirky(n)
	if errors.Is(err, payerr.ErrNotFound) {
		return nil
	}
	return err
}

// LookupOrRewrap returns only the two retryable kinds, re-wrapped.
func /*@orRewrap*/LookupOrRewrap(n int) error {
	err := gateway.Lookup(n)
	if errors.Is(err, payerr.ErrTimeout) || errors.Is(err, payerr.ErrRateLimited) {
		return fmt.Errorf("retryable: %w", err)
	}
	return nil
}
