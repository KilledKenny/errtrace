// Package repo declares the storage interfaces of the bank.
package repo

import (
	"context"
	"errors"

	"example.com/bank/domain"
)

// ErrTxDone is returned by Commit and Rollback on a finished transaction.
var ErrTxDone = errors.New("repo: transaction already finished")

// Accounts stores accounts.
type Accounts interface {
	Get(ctx context.Context, id domain.AccountID) (*domain.Account, error)
	Put(ctx context.Context, a *domain.Account) error
}

// Ledger records executed transfers.
type Ledger interface {
	Append(ctx context.Context, t domain.Transfer) error
}

// Tx is a unit of work.
type Tx interface {
	Commit() error
	Rollback() error
}

// Store is everything the service needs from storage.
type Store interface {
	Accounts
	Ledger
	Begin() (Tx, error)
}
