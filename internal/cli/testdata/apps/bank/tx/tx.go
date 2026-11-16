// Package tx runs callbacks inside a storage transaction.
package tx

import (
	"context"
	"errors"
	"fmt"

	"example.com/bank/repo"
)

// Manager begins transactions on a store.
type Manager struct{ store repo.Store }

// NewManager returns a Manager for store.
func NewManager(store repo.Store) *Manager { return &Manager{store: store} }

// WithTx runs fn in a transaction. When fn fails the transaction is rolled
// back and a rollback failure is joined to fn's error; otherwise the
// transaction is committed.
func (m *Manager) /*@withtx*/WithTx(ctx context.Context, fn func(repo.Tx) error) (err error) {
	t, err := m.store./*@begin_call*/Begin()
	if err != nil {
		return fmt.Errorf("tx: begin: %w", err)
	}
	defer func() {
		if err != nil {
			if rerr := t.Rollback(); rerr != nil {
				err = errors.Join(err, fmt.Errorf("tx: rollback: %w", rerr))
			}
			return
		}
		if cerr := t.Commit(); cerr != nil {
			err = fmt.Errorf("tx: commit: %w", cerr)
		}
	}()
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return fn(t)
}

// ReadOnly runs fn and always rolls back; fn's error wins over the rollback's.
func (m *Manager) /*@readonly*/ReadOnly(fn func() error) error {
	t, err := m.store.Begin()
	if err != nil {
		return fmt.Errorf("tx: begin: %w", err)
	}
	ferr := fn()
	rerr := t./*@rollback_call*/Rollback()
	if ferr != nil {
		return ferr
	}
	return rerr
}
