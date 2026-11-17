package processor

import (
	"errors"
	"fmt"

	"example.com/guards/gateway"
	"example.com/guards/payerr"
)

// Processor carries the handling policy.
type Processor struct {
	Lenient bool
	Seen    int
}

func /*@isNotFoundDecl*/isNotFound(err error) bool { return errors.Is(err, payerr.ErrNotFound) }

func (p *Processor) ignorable(err error) bool {
	return p.Lenient && err == payerr.ErrDeclined
}

// LookupHelper guards through a helper function.
func /*@helperFunc*/LookupHelper(n int) error {
	err := gateway.Lookup(n)
	if isNotFound(err) {
		return nil
	}
	return err
}

// Charge guards through a method on the processor.
func (p *Processor) /*@helperMethod*/Charge(n int) error {
	err := gateway.Charge(n)
	if p.ignorable(err) {
		p.Seen++
		return nil
	}
	return err
}

// Classify is a filter applied by callers: it drops not-found and declined
// and wraps the rest.
func /*@classify*/Classify(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, payerr.ErrNotFound) || err == payerr.ErrDeclined {
		return nil
	}
	return fmt.Errorf("classified: %w", err)
}

// LookupClassified passes the lookup error through Classify.
func /*@classifyLookup*/LookupClassified(n int) error {
	return /*@classifyLookupCall*/Classify(gateway.Lookup(n))
}

// CaptureClassified passes the capture error through Classify; its declined
// error is wrapped, so the identity check in Classify does not match.
func /*@classifyCapture*/CaptureClassified(n int) error {
	err := gateway.Capture(n)
	return /*@classifyCaptureCall*/Classify(err)
}

// ChargeClassifiedTwice filters in the callee and then again in the caller.
func /*@classifyTwice*/ChargeClassifiedTwice(n int) error {
	err := Classify(gateway.Charge(n))
	if errors.Is(err, payerr.ErrTimeout) {
		return nil
	}
	return err
}

// passThrough returns its parameter after an unrelated check.
func passThrough(err error, strict bool) error {
	if strict && err == payerr.ErrInternal {
		return payerr.ErrDuplicate
	}
	return err
}

// VoidPassThrough only translates ErrInternal in strict mode.
func /*@paramPartial*/VoidPassThrough(n int, strict bool) error {
	return passThrough(gateway.Void(n), strict)
}
