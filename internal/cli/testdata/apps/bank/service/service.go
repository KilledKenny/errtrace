// Package service implements the bank's use cases on top of storage, the
// rules engine, the event bus and the audit log.
package service

import (
	"context"
	"errors"
	"fmt"

	"example.com/bank/audit"
	"example.com/bank/domain"
	"example.com/bank/events"
	"example.com/bank/repo"
	"example.com/bank/retry"
	"example.com/bank/rules"
	"example.com/bank/tx"
)

// ErrUnsupported is returned when the configured store lacks a feature.
var ErrUnsupported = errors.New("service: not supported by this store")

// Service is the application layer.
type Service struct {
	store   repo.Store
	txm     *tx.Manager
	rules   *rules.Engine
	bus     *events.Bus
	audit   *audit.Logger
	retries int
}

// New wires a Service.
func New(store repo.Store, eng *rules.Engine, bus *events.Bus, log *audit.Logger, retries int) *Service {
	return &Service{store: store, txm: tx.NewManager(store), rules: eng, bus: bus, audit: log, retries: retries}
}

// Transfer executes t in a transaction, retrying while the store is busy.
// The outcome is always audited; a failing audit is joined to the result.
func (s *Service) /*@transfer*/Transfer(ctx context.Context, t domain.Transfer) error {
	err := retry./*@retry_call*/Do(ctx, s.retries, func() error {
		return s.txm./*@withtx_call*/WithTx(ctx, func(repo.Tx) error { return s./*@execute_call*/execute(ctx, t) })
	})
	if err != nil {
		err = fmt.Errorf("transfer %d: %w", t.ID, err)
	}
	if aerr := s.audit./*@record_call*/Record("transfer", t, err); aerr != nil {
		return errors.Join(err, aerr)
	}
	if err != nil {
		return err
	}
	return s.bus./*@notify_call*/Notify(events.Event{Kind: "transfer.done", Transfer: t})
}

// execute is the body of the transaction.
func (s *Service) /*@execute*/execute(ctx context.Context, t domain.Transfer) error {
	in, err := s./*@input_call*/input(ctx, t)
	if err != nil {
		return err
	}
	from, to := in.From, in.To
	if err := s.rules./*@evaluate_call*/Evaluate(ctx, in); err != nil {
		return err
	}
	if err := from./*@debit_call*/Debit(t.Amount); err != nil {
		return err
	}
	if err := to.Credit(t.Amount); err != nil {
		return err
	}
	if err := s.store./*@append_call*/Append(ctx, t); err != nil {
		return err
	}
	return s./*@save_call*/save(ctx, from, to)
}

func (s *Service) /*@save*/save(ctx context.Context, accounts ...*domain.Account) error {
	for _, a := range accounts {
		if err := s.store.Put(ctx, a); err != nil {
			return fmt.Errorf("save %s: %w", a.ID, err)
		}
	}
	return nil
}

// Batch runs the transfers in order. Duplicates are skipped, cancellation
// stops the batch, every other failure is collected.
func (s *Service) /*@batch*/Batch(ctx context.Context, ts []domain.Transfer) error {
	var failed error
	for _, t := range ts {
		err := s.Transfer(ctx, t)
		switch {
		case err == nil, errors.Is(err, domain.ErrDuplicate):
			continue
		case errors.Is(err, context.Canceled):
			return err
		}
		failed = errors.Join(failed, err)
	}
	return failed
}

// Balance reads an account's balance, retrying transient store errors.
func (s *Service) /*@balance*/Balance(ctx context.Context, id domain.AccountID) (domain.Money, error) {
	a, err := retry./*@value_call*/Value(ctx, s.retries, func() (*domain.Account, error) {
		return s.store.Get(ctx, id)
	})
	if err != nil {
		return 0, err
	}
	return a.Balance, nil
}

// Preview says whether t would pass the rules, treating limits as advice.
func (s *Service) /*@preview*/Preview(ctx context.Context, t domain.Transfer) error {
	in, err := s.input(ctx, t)
	if err != nil {
		return err
	}
	return s.rules./*@advisory_call*/Advisory(ctx, in)
}

// Explain lists every rule t would break.
func (s *Service) /*@explain*/Explain(ctx context.Context, t domain.Transfer) error {
	in, err := s.input(ctx, t)
	if err != nil {
		return err
	}
	return s.rules.EvaluateAll(ctx, in)
}

func (s *Service) input(ctx context.Context, t domain.Transfer) (rules.Input, error) {
	from, err := s.store.Get(ctx, t.From)
	if err != nil {
		return rules.Input{}, fmt.Errorf("source: %w", err)
	}
	to, err := s.store.Get(ctx, t.To)
	if err != nil {
		return rules.Input{}, fmt.Errorf("destination: %w", err)
	}
	return rules.Input{Transfer: t, From: from, To: to}, nil
}

// Owner reads the account holder's name in a read-only transaction.
func (s *Service) /*@owner*/Owner(ctx context.Context, id domain.AccountID) (string, error) {
	var owner string
	err := s.txm./*@readonly_call*/ReadOnly(func() error {
		a, err := s.store.Get(ctx, id)
		if err != nil {
			return err
		}
		owner = a.Owner
		return nil
	})
	return owner, err
}

// Exists reports whether the account is known. "Not found" is an answer,
// not an error.
func (s *Service) /*@exists*/Exists(ctx context.Context, id domain.AccountID) (bool, error) {
	_, err := s.store.Get(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// CanPay reports only a shortfall: unknown, frozen and closed accounts are
// somebody else's problem.
func (s *Service) /*@canpay*/CanPay(ctx context.Context, id domain.AccountID, amount domain.Money) error {
	a, err := s.store.Get(ctx, id)
	if err != nil {
		return nil
	}
	err = a.Debit(amount)
	var short domain.InsufficientFundsError
	if errors.As(err, &short) {
		return err
	}
	return nil
}

// SetStatus changes an account's status and announces it.
func (s *Service) /*@setstatus*/SetStatus(ctx context.Context, id domain.AccountID, st domain.Status) error {
	a, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	a.Status = st
	if err := s.store.Put(ctx, a); err != nil {
		return err
	}
	return s.bus./*@publish_call*/Publish(events.Event{Kind: "account.status", Transfer: domain.Transfer{From: id}})
}

// Settled reports whether a transfer is in the ledger; only stores that can
// search their ledger support it.
func (s *Service) /*@settled*/Settled(id domain.TransferID) error {
	f, ok := s.store.(interface {
		FindTransfer(domain.TransferID) error
	})
	if !ok {
		return ErrUnsupported
	}
	return f./*@find_call*/FindTransfer(id)
}

// Ready reports whether the store accepts transactions.
func (s *Service) /*@ready*/Ready() error { return s.txm.Ping() }

// Import records a transfer that was executed elsewhere: it only enters the
// ledger, no money moves and no rule is consulted.
func (s *Service) /*@import*/Import(ctx context.Context, t domain.Transfer) error {
	if err := t.Validate(); err != nil {
		return err
	}
	return s.store./*@import_append_call*/Append(ctx, t)
}
