// Package filerepo is a repo.Store keeping one small text file per account.
package filerepo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"example.com/bank/domain"
	"example.com/bank/repo"
	"example.com/bank/retry"
)

// CorruptError reports an account file that cannot be decoded.
type CorruptError struct {
	ID  domain.AccountID
	Err error
}

func (e *CorruptError) Error() string { return fmt.Sprintf("account %s is corrupt: %v", e.ID, e.Err) }
func (e *CorruptError) Unwrap() error { return e.Err }

var errShortRecord = errors.New("short record")

// Store is rooted at a directory.
type Store struct{ dir string }

// Open creates the directory if needed.
func /*@open*/Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("filerepo: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(id domain.AccountID) string { return filepath.Join(s.dir, string(id)+".acct") }

func (s *Store) /*@get*/Get(ctx context.Context, id domain.AccountID) (*domain.Account, error) {
	data, err := os.ReadFile(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, domain.NotFoundError[domain.AccountID]{Kind: "account", ID: id}
	}
	if err != nil {
		return nil, fmt.Errorf("filerepo: read %s: %w", id, err)
	}
	a, err := /*@decode_call*/decode(id, string(data))
	if err != nil {
		return nil, &CorruptError{ID: id, Err: err}
	}
	return a, nil
}

// decode parses "owner|balance|status|spent".
func /*@decode*/decode(id domain.AccountID, rec string) (*domain.Account, error) {
	parts := strings.Split(strings.TrimSpace(rec), "|")
	if len(parts) != 4 {
		return nil, errShortRecord
	}
	bal, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("balance: %w", err)
	}
	st, err := strconv.Atoi(parts[2])
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	spent, _ := strconv.ParseInt(parts[3], 10, 64)
	return &domain.Account{ID: id, Owner: parts[0], Balance: domain.Money(bal),
		Status: domain.Status(st), SpentToday: domain.Money(spent)}, nil
}

func (s *Store) /*@put*/Put(ctx context.Context, a *domain.Account) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rec := fmt.Sprintf("%s|%d|%d|%d\n", a.Owner, a.Balance, a.Status, a.SpentToday)
	if err := os.WriteFile(s.path(a.ID), []byte(rec), 0o644); err != nil {
		return fmt.Errorf("filerepo: write %s: %w", a.ID, err)
	}
	return nil
}

func (s *Store) /*@append*/Append(ctx context.Context, t domain.Transfer) (err error) {
	f, err := os.OpenFile(filepath.Join(s.dir, "ledger"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("filerepo: ledger: %w", err)
	}
	_, werr := fmt.Fprintf(f, "%d %s %s %d\n", t.ID, t.From, t.To, t.Amount)
	return errors.Join(werr, f.Close())
}

func (s *Store) lock() string { return filepath.Join(s.dir, "LOCK") }

// Begin takes the directory lock; a held lock is a transient failure.
func (s *Store) /*@begin*/Begin() (repo.Tx, error) {
	f, err := os.OpenFile(s.lock(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, retry.ErrBusy
		}
		return nil, fmt.Errorf("filerepo: lock: %w", err)
	}
	f.Close()
	return &fileTx{lock: s.lock()}, nil
}

type fileTx struct {
	lock string
	done bool
}

func (t *fileTx) Commit() error { return t.release() }

// Rollback tolerates a lock that is already gone.
func (t *fileTx) /*@rollback*/Rollback() error {
	err := t.release()
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, repo.ErrTxDone) {
		return nil
	}
	return err
}

func (t *fileTx) /*@release*/release() error {
	if t.done {
		return repo.ErrTxDone
	}
	t.done = true
	return os.Remove(t.lock)
}
