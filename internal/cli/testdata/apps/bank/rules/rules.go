// Package rules is the engine that decides whether a transfer may run.
package rules

import (
	"context"
	"errors"
	"fmt"

	"example.com/bank/domain"
)

// Input is what a rule looks at.
type Input struct {
	Transfer domain.Transfer
	From, To *domain.Account
}

// Rule vetoes a transfer by returning an error.
type Rule interface {
	Name() string
	Check(ctx context.Context, in Input) error
}

// Violation says which rule refused the transfer; it unwraps to the reason.
type Violation struct {
	Rule string
	Err  error
}

func (v *Violation) Error() string { return "rule " + v.Rule + ": " + v.Err.Error() }
func (v *Violation) Unwrap() error { return v.Err }

// Engine runs its rules in order.
type Engine struct{ rules []Rule }

// NewEngine returns an engine with the given rules.
func NewEngine(rs ...Rule) *Engine { return &Engine{rules: rs} }

// Add appends a rule.
func (e *Engine) Add(r Rule) { e.rules = append(e.rules, r) }

// Evaluate stops at the first rule that fails and reports it as a Violation.
func (e *Engine) /*@evaluate*/Evaluate(ctx context.Context, in Input) error {
	for _, r := range e.rules {
		if err := r./*@check_call*/Check(ctx, in); err != nil {
			return &Violation{Rule: r.Name(), Err: err}
		}
	}
	return nil
}

// EvaluateAll runs every rule and reports all failures together.
func (e *Engine) /*@all*/EvaluateAll(ctx context.Context, in Input) error {
	var errs []error
	for _, r := range e.rules {
		if err := r.Check(ctx, in); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// Advisory treats spending limits as advice: a transfer that only breaks a
// limit is let through.
func (e *Engine) /*@advisory*/Advisory(ctx context.Context, in Input) error {
	err := e.Evaluate(ctx, in)
	if errors.Is(err, domain.ErrLimitExceeded) {
		return nil
	}
	return err
}

// Explain runs a single rule and labels its verdict.
func /*@explain*/Explain(ctx context.Context, r Rule, in Input) error {
	if err := r.Check(ctx, in); err != nil {
		return fmt.Errorf("%s says no: %w", r.Name(), err)
	}
	return nil
}
