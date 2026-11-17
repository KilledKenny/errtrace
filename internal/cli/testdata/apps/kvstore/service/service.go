// Package service implements the business rules on top of a repository.
package service

import (
	"errors"
	"fmt"
	"strings"

	"example.com/kvstore/kverr"
	"example.com/kvstore/repo"
	"example.com/kvstore/validate"
)

// ErrLoop is returned when alias resolution does not terminate.
var ErrLoop = errors.New("service: alias loop")

var errGaveUp = errors.New("service: gave up")

// Service is the inventory service.
type Service struct {
	repo     *repo.Repo
	readOnly bool
}

// New returns a service over r.
func New(r *repo.Repo, readOnly bool) *Service {
	return &Service{repo: r, readOnly: readOnly}
}

// Get returns the value of key, following aliases.
func (s *Service) /*@get*/Get(key string) (string, error) {
	if err := validate./*@key_call*/Key(key); err != nil {
		return "", fmt.Errorf("get: %w", err)
	}
	return s./*@resolve_call*/resolve(key, 0)
}

// resolve and follow are mutually recursive: a value "@other" is an alias.
func (s *Service) /*@resolve*/resolve(key string, depth int) (string, error) {
	if depth > 8 {
		return "", ErrLoop
	}
	v, err := s.repo.Load(key)
	if err != nil {
		return "", err
	}
	return s./*@follow_call*/follow(string(v), depth)
}

func (s *Service) /*@follow*/follow(val string, depth int) (string, error) {
	if !strings.HasPrefix(val, "@") {
		return val, nil
	}
	out, err := s.resolve(val[1:], depth+1)
	if err != nil {
		return "", fmt.Errorf("alias %s: %w", val, err)
	}
	return out, nil
}

// Put stores a value; the deferred closure wraps every failure.
func (s *Service) /*@put*/Put(key, val string) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("put %s: %w", key, err)
		}
	}()
	if s.readOnly {
		return kverr.ErrReadOnly
	}
	if err = validate.Key(key); err != nil {
		return err
	}
	if verr, _ := validate.Value(val); verr != nil {
		return verr
	}
	return s.repo./*@store_call*/Store(key, []byte(val))
}

// Close flushes the repository; the deferred closure always overwrites the
// provisional result.
func (s *Service) /*@close*/Close() (err error) {
	defer func() {
		err = s.repo.Flush()
	}()
	return errGaveUp
}

// Delete removes a key, retrying up to n times.
func (s *Service) /*@delete*/Delete(key string, n int) error {
	if s.readOnly {
		return kverr.ErrReadOnly
	}
	return s.retry(key, n)
}

// retry is directly recursive.
func (s *Service) /*@retry*/retry(key string, n int) error {
	err := s.repo.Remove(key)
	if err == nil {
		return nil
	}
	if n <= 0 {
		return errGaveUp
	}
	return s./*@retry_call*/retry(key, n-1)
}

// Move copies src to dst. The inner err shadows the outer one, so the
// function always returns nil.
func (s *Service) /*@move*/Move(src, dst string) error {
	var err error
	if v, err := s.repo.Load(src); err == nil {
		err = s.repo.Store(dst, v)
		_ = err
	}
	return err
}

// Copy is the fixed version of Move.
func (s *Service) /*@copy*/Copy(src, dst string) error {
	var err error
	var v []byte
	if v, err = s.repo.Load(src); err == nil {
		err = s.repo.Store(dst, v)
	}
	return err
}

// annotate returns its argument, tagged with the operation.
func /*@annotate*/annotate(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", op, err)
}

// firstErr returns the first non-nil error.
func /*@firsterr*/firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// pick returns fallback when err is nil.
func pick(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// Health merges several checks into one error.
func (s *Service) /*@health*/Health(key string) error {
	var err error
	switch {
	case s.readOnly:
		err = kverr.ErrReadOnly
	case key == "":
		err = s.repo./*@ping_call*/Ping()
	case key == "quota":
		err = kverr.ErrThrottled
	default:
		err = s.repo.Check(key)
	}
	return /*@annotate_call*/annotate("health", err)
}

// Audit runs independent checks and reports the first failure.
func (s *Service) /*@audit*/Audit(key string) error {
	return /*@firsterr_call*/firstErr(s.repo.Version(key, 1), s.repo.Exists(key), kverr.ErrConflict)
}

// Verify prefers the repository's verdict and falls back to a sentinel.
func (s *Service) /*@verify*/Verify(key string) error {
	return /*@pick_call*/pick(s.repo.Check(key), kverr.ErrCorrupt)
}

// List returns the keys with a prefix.
func (s *Service) /*@list*/List(prefix string) ([]string, error) {
	keys, err := s.repo.Keys(prefix, 100)
	if err != nil {
		return nil, annotate("list", err)
	}
	return keys, nil
}

// Fsck verifies the stored bookkeeping of a key.
func (s *Service) /*@fsck*/Fsck(key string) error {
	return annotate("fsck", s.repo.Fsck(key))
}

// Has reports whether key exists.
func (s *Service) Has(key string) error { return s.repo.Has(key) }

// Stats never fails; the error result exists for interface compatibility.
func (s *Service) /*@stats*/Stats() (int, error) {
	n, err := s.repo.Tail(10)
	if err != nil {
		return n, nil
	}
	return n, nil
}

// Exchange swaps two keys.
func (s *Service) /*@exchange*/Exchange(a, b string) error {
	rerr, werr := s.repo./*@swap_call*/Swap(a, b)
	if rerr != nil {
		return rerr
	}
	return werr
}

// Find looks a key up through the legacy API.
func (s *Service) /*@find*/Find(key string) (string, error) {
	err, v := s.repo./*@lookup_call*/Lookup(key)
	return string(v), err
}

// inflight counts running maintenance operations.
var inflight int

func begin() { inflight++ }
func end()   { inflight-- }

// Sync flushes the repository. The deferred call does not touch the named
// result.
func (s *Service) /*@sync*/Sync() (err error) {
	begin()
	defer end()
	if s.readOnly {
		return kverr.ErrReadOnly
	}
	err = s.repo.Flush()
	return err
}

// tag marks err as coming from the service layer.
func tag(err error) error {
	return /*@tag_call*/annotate("service", err)
}

// Probe checks that key could be stored, without touching its value.
func (s *Service) /*@probe*/Probe(key string) error {
	if err := s.repo.Alive(); err != nil {
		return err
	}
	if err := s.repo.Stat(key); err != nil {
		return err
	}
	return tag(s.repo.Probe(key))
}
