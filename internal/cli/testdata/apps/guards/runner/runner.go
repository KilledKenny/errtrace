// Package runner retries payment jobs.
package runner

import (
	"errors"
	"fmt"

	"example.com/guards/gateway"
	"example.com/guards/payerr"
)

// Runner retries a job up to Max times.
type Runner struct {
	Max int
}

// Run retries on timeouts and rate limits and fails fast on everything else.
func (r *Runner) /*@run*/Run(id string) error {
	var err error
	for attempt := 0; attempt < r.Max; attempt++ {
		err = gateway.Lookup(attempt)
		if err == nil {
			return nil
		}
		if errors.Is(err, payerr.ErrTimeout) || errors.Is(err, payerr.ErrRateLimited) {
			continue
		}
		return fmt.Errorf("job %s: %w", id, err)
	}
	return fmt.Errorf("job %s: retries exhausted: %w", id, err)
}

// RunSkipTimeouts skips timeouts with continue and stops on any other error.
func (r *Runner) /*@loopContinue*/RunSkipTimeouts() error {
	for attempt := 0; attempt < r.Max; attempt++ {
		err := gateway.Charge(attempt)
		if err == payerr.ErrTimeout {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// RunUntilSettled breaks out as soon as the error is not a timeout; a
// timeout on the last attempt is still returned.
func (r *Runner) /*@loopBreak*/RunUntilSettled() error {
	var err error
	for attempt := 0; attempt < r.Max; attempt++ {
		err = gateway.Charge(attempt)
		if err != payerr.ErrTimeout {
			break
		}
	}
	return err
}

// RunLast remembers the last non-timeout error of all attempts.
func (r *Runner) /*@loopLast*/RunLast() error {
	var last error
	for attempt := 0; attempt < r.Max; attempt++ {
		if err := gateway.Charge(attempt); err != nil && err != payerr.ErrTimeout {
			last = err
		}
	}
	return last
}

// RunTimeoutsOnly is Run with a single retryable kind.
func (r *Runner) /*@runSingle*/RunTimeoutsOnly(id string) error {
	var err error
	for attempt := 0; attempt < r.Max; attempt++ {
		err = gateway.Lookup(attempt)
		if err == nil {
			return nil
		}
		if errors.Is(err, payerr.ErrTimeout) {
			continue
		}
		return fmt.Errorf("job %s: %w", id, err)
	}
	return fmt.Errorf("job %s: retries exhausted: %w", id, err)
}
