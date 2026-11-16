// Package limits implements spending limits.
package limits

import (
	"fmt"

	"example.com/bank/domain"
)

// Policy holds the caps; zero means unlimited.
type Policy struct {
	PerTransfer domain.Money
	Daily       domain.Money
}

// Exceeded says which cap was hit. It unwraps to domain.ErrLimitExceeded.
type Exceeded struct {
	Kind    string
	Limit   domain.Money
	Attempt domain.Money
}

func (e *Exceeded) Error() string {
	return fmt.Sprintf("%s limit %d exceeded by %d", e.Kind, e.Limit, e.Attempt-e.Limit)
}

func (e *Exceeded) Unwrap() error { return domain.ErrLimitExceeded }

// Check applies the policy to a transfer, given what was already spent today.
func (p Policy) /*@check*/Check(t domain.Transfer, spent domain.Money) error {
	if p.PerTransfer > 0 && t.Amount > p.PerTransfer {
		return &Exceeded{Kind: "per-transfer", Limit: p.PerTransfer, Attempt: t.Amount}
	}
	if p.Daily > 0 && spent+t.Amount > p.Daily {
		return fmt.Errorf("daily budget of %s: %w", t.From,
			&Exceeded{Kind: "daily", Limit: p.Daily, Attempt: spent + t.Amount})
	}
	return nil
}
