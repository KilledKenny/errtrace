package domain

import (
	"fmt"
	"time"
)

type (
	AccountID  string
	TransferID int64
	Money      int64
)

// Status is the lifecycle state of an account.
type Status int

const (
	Active Status = iota
	Frozen
	Closed
)

// Account is a customer account.
type Account struct {
	ID         AccountID
	Owner      string
	Balance    Money
	Status     Status
	SpentToday Money
}

// Transfer moves Amount from one account to another.
type Transfer struct {
	ID       TransferID
	From, To AccountID
	Amount   Money
	Currency string
	At       time.Time
}

// Usable reports whether money may leave the account.
func (a *Account) /*@usable*/Usable() error {
	switch a.Status {
	case Closed:
		return ErrAccountClosed
	case Frozen:
		return ErrFrozen
	}
	return nil
}

// Debit takes m out of the account.
func (a *Account) /*@debit*/Debit(m Money) error {
	if err := a.Usable(); err != nil {
		return fmt.Errorf("debit %s: %w", a.ID, err)
	}
	if a.Balance < m {
		return InsufficientFundsError{Account: a.ID, Need: m, Have: a.Balance}
	}
	a.Balance -= m
	a.SpentToday += m
	return nil
}

// Credit adds m to the account; frozen accounts still receive money.
func (a *Account) /*@credit*/Credit(m Money) error {
	if a.Status == Closed {
		return fmt.Errorf("credit %s: %w", a.ID, ErrAccountClosed)
	}
	a.Balance += m
	return nil
}

// Validate checks the transfer's own fields, without looking at accounts.
func (t Transfer) /*@validate*/Validate() error {
	if t.From == "" {
		return &ValidationError{Field: "from", Reason: "required"}
	}
	if t.To == "" {
		return &ValidationError{Field: "to", Reason: "required"}
	}
	if t.From == t.To {
		return ErrSameAccount
	}
	if t.Amount <= 0 {
		return &ValidationError{Field: "amount", Reason: "must be positive"}
	}
	return nil
}
