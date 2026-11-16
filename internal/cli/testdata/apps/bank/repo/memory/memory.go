// Package memory is the in-memory repo.Store used by the CLI and tests.
package memory

import (
	"context"
	"fmt"
	"sync"

	"example.com/bank/domain"
	"example.com/bank/repo"
	"example.com/bank/retry"
)

// Store keeps everything in maps.
type Store struct {
	mu       sync.Mutex
	accounts map[domain.AccountID]*domain.Account
	seen     map[domain.TransferID]bool
	// Contention makes the next Begin calls time out, to exercise retries.
	Contention int
}

// New returns a store holding the given accounts.
func New(accounts ...*domain.Account) *Store {
	s := &Store{accounts: map[domain.AccountID]*domain.Account{}, seen: map[domain.TransferID]bool{}}
	for _, a := range accounts {
		s.accounts[a.ID] = a
	}
	return s
}

func (s *Store) /*@get*/Get(ctx context.Context, id domain.AccountID) (*domain.Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	if !ok {
		return nil, domain.NotFoundError[domain.AccountID]{Kind: "account", ID: id}
	}
	cp := *a
	return &cp, nil
}

func (s *Store) /*@put*/Put(ctx context.Context, a *domain.Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *a
	s.accounts[a.ID] = &cp
	return nil
}

func (s *Store) /*@append*/Append(ctx context.Context, t domain.Transfer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen[t.ID] {
		return fmt.Errorf("transfer %d: %w", t.ID, domain.ErrDuplicate)
	}
	s.seen[t.ID] = true
	return nil
}

// FindTransfer looks a transfer up in the ledger.
func (s *Store) /*@find*/FindTransfer(id domain.TransferID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.seen[id] {
		return domain.NotFoundError[domain.TransferID]{Kind: "transfer", ID: id}
	}
	return nil
}

func (s *Store) /*@begin*/Begin() (repo.Tx, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Contention > 0 {
		s.Contention--
		return nil, &retry.TimeoutError{Op: "memory: begin"}
	}
	return &memTx{}, nil
}

type memTx struct{ done bool }

func (t *memTx) Commit() error   { return t.finish() }
func (t *memTx) Rollback() error { return t.finish() }

func (t *memTx) finish() error {
	if t.done {
		return repo.ErrTxDone
	}
	t.done = true
	return nil
}
