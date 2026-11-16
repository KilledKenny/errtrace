package rules

import (
	"context"
	"errors"
	"fmt"

	"example.com/bank/domain"
	"example.com/bank/limits"
)

var (
	// ErrCurrency is wrapped by Currency for a currency that is not allowed.
	ErrCurrency = errors.New("rules: unsupported currency")
	// ErrBlocked is returned by Blocklist.
	ErrBlocked = errors.New("rules: account blocked")
)

// Shape checks the transfer's own fields.
type Shape struct{}

func (Shape) Name() string { return "shape" }
func (Shape) Check(_ context.Context, in Input) error {
	return in.Transfer.Validate()
}

// SourceUsable requires an active source account.
type SourceUsable struct{}

func (SourceUsable) Name() string { return "source-usable" }
func (SourceUsable) Check(_ context.Context, in Input) error {
	return in.From.Usable()
}

// DestOpen refuses to pay into a closed account.
type DestOpen struct{}

func (DestOpen) Name() string { return "dest-open" }
func (DestOpen) Check(_ context.Context, in Input) error {
	if in.To.Status == domain.Closed {
		return fmt.Errorf("destination %s: %w", in.To.ID, domain.ErrAccountClosed)
	}
	return nil
}

// Funds requires the source balance to cover the amount.
type Funds struct{}

func (Funds) Name() string { return "funds" }
func (Funds) /*@funds_check*/Check(_ context.Context, in Input) error {
	if in.From.Balance < in.Transfer.Amount {
		return domain.InsufficientFundsError{Account: in.From.ID, Need: in.Transfer.Amount, Have: in.From.Balance}
	}
	return nil
}

// Limit applies a spending policy.
type Limit struct{ Policy limits.Policy }

func (Limit) Name() string { return "limit" }
func (l Limit) /*@limit_check*/Check(_ context.Context, in Input) error {
	return l.Policy.Check(in.Transfer, in.From.SpentToday)
}

// Currency allows only the listed currencies.
type Currency struct{ Allowed []string }

func (Currency) Name() string { return "currency" }
func (c Currency) Check(_ context.Context, in Input) error {
	for _, cur := range c.Allowed {
		if cur == in.Transfer.Currency {
			return nil
		}
	}
	return fmt.Errorf("%w %q", ErrCurrency, in.Transfer.Currency)
}

// Blocklist refuses transfers touching a listed account.
type Blocklist map[domain.AccountID]bool

func (Blocklist) Name() string { return "blocklist" }
func (b Blocklist) Check(_ context.Context, in Input) error {
	if b[in.Transfer.From] || b[in.Transfer.To] {
		return ErrBlocked
	}
	return nil
}

// Hours allows transfers only between Open and Close (UTC hours).
type Hours struct{ Open, Close int }

func (Hours) Name() string { return "hours" }
func (h Hours) Check(_ context.Context, in Input) error {
	if hr := in.Transfer.At.UTC().Hour(); hr < h.Open || hr >= h.Close {
		return errors.New("rules: outside business hours")
	}
	return nil
}

// Alive fails once the request's context is done.
type Alive struct{}

func (Alive) Name() string { return "alive" }
func (Alive) /*@alive_check*/Check(ctx context.Context, _ Input) error {
	return ctx.Err()
}

// Func adapts a function to a Rule.
type Func struct {
	Label string
	Fn    func(context.Context, Input) error
}

func (f Func) Name() string { return f.Label }
func (f Func) /*@func_check*/Check(ctx context.Context, in Input) error {
	return f.Fn(ctx, in)
}

// Default is the rule set every deployment starts from.
func Default(p limits.Policy) *Engine {
	return NewEngine(
		Alive{}, Shape{}, SourceUsable{}, DestOpen{}, Funds{}, Limit{Policy: p},
		Currency{Allowed: []string{"EUR", "USD"}}, Blocklist{"sanctioned": true}, Hours{Open: 0, Close: 24},
	)
}
