// Package repo maps namespaced keys onto the storage engine.
package repo

import (
	"errors"
	"fmt"

	"example.com/kvstore/engine"
	"example.com/kvstore/kverr"
)

// ErrNotFound re-exports the shared sentinel.
var ErrNotFound = kverr.ErrNotFound

// ErrTooMany stops a listing that grows too large.
var ErrTooMany = errors.New("repo: too many keys")

// base carries the engine; its methods are promoted to Repo.
type base struct {
	eng *engine.Engine
}

// Ping reports whether the engine is usable.
func (b *base) /*@ping*/Ping() error {
	if b.eng == nil {
		return kverr.Internal()
	}
	return b.eng.Wait()
}

// Repo is a namespace inside an engine.
type Repo struct {
	base
	ns string
}

// New returns the repository for namespace ns.
func New(eng *engine.Engine, ns string) *Repo {
	return &Repo{base: base{eng: eng}, ns: ns}
}

func (r *Repo) path(key string) string { return r.ns + "/" + key }

// Load reads key.
func (r *Repo) /*@load*/Load(key string) ([]byte, error) {
	v, err := r.eng./*@get_call*/Get(r.path(key))
	if err != nil {
		return nil, fmt.Errorf("repo: load %s: %w", key, err)
	}
	return v, nil
}

// Store writes key.
func (r *Repo) /*@store*/Store(key string, val []byte) error {
	if err := r./*@recv*/eng.Put(r.path(key), val); err != nil {
		return &kverr.OpError{Op: "store", Key: key, Err: err}
	}
	return nil
}

// Lookup is an old API: the error is the first result.
func (r *Repo) /*@lookup*/Lookup(key string) (error, []byte) {
	v, ok := r.cached(key)
	if ok {
		return nil, v
	}
	v, err := r.eng.Get(r.path(key))
	if errors.Is(err, kverr.ErrNotFound) {
		return ErrNotFound, nil
	}
	return err, v
}

func (r *Repo) cached(string) ([]byte, bool) { return nil, false }

// Swap exchanges two keys and reports the read and the write error apart.
func (r *Repo) /*@swap*/Swap(a, b string) (readErr, writeErr error) {
	va, readErr := r.eng.Get(r.path(a))
	if readErr != nil {
		return readErr, nil
	}
	vb, readErr := r.eng.Get(r.path(b))
	if readErr != nil {
		return readErr, nil
	}
	if writeErr = r.eng.Put(r.path(a), vb); writeErr != nil {
		return nil, writeErr
	}
	return nil, r.eng.Put(r.path(b), va)
}

// find returns a concrete *OpError, nil when the key is acceptable.
func (r *Repo) /*@find*/find(key string) *kverr.OpError {
	if key == "" {
		return &kverr.OpError{Op: "find", Err: kverr.ErrEmptyKey}
	}
	if _, err := r.eng.Get(r.path(key)); err != nil {
		return &kverr.OpError{Op: "find", Key: key, Err: err}
	}
	return nil
}

// Exists has the classic typed-nil bug: a nil *OpError becomes a non-nil error.
func (r *Repo) /*@exists*/Exists(key string) error {
	return r.find(key)
}

// Check is the correct version of Exists.
func (r *Repo) /*@check*/Check(key string) error {
	if e := r.find(key); e != nil {
		return e
	}
	return nil
}

// Keys lists at most max keys with the given prefix.
func (r *Repo) /*@keys*/Keys(prefix string, max int) ([]string, error) {
	var out []string
	err := r.eng./*@scan_call*/Scan(r.path(prefix), false, func(k string) error {
		if len(out) >= max {
			return ErrTooMany
		}
		out = append(out, k)
		return nil
	})
	return out, err
}

// Remove deletes key.
func (r *Repo) /*@remove*/Remove(key string) error { return r.eng.Delete(r.path(key)) }

// Version checks the snapshot version of key.
func (r *Repo) Version(key string, want int) error { return r.eng.Snapshot(r.path(key), want) }

// Has reports whether key exists; both returns can yield the same
// not-found value under two names.
func (r *Repo) /*@has*/Has(key string) error {
	if key == "" {
		return ErrNotFound
	}
	_, err := r.eng.Get(r.path(key))
	return err
}

// Fsck verifies one key in the engine.
func (r *Repo) Fsck(key string) error { return r.eng.Check(r.path(key)) }

// Flush compacts the engine.
func (r *Repo) Flush() error { return r.eng.Compact() }

// Tail drains the log.
func (r *Repo) /*@tail*/Tail(max int) (n int, err error) {
	n, err = r.eng.Drain(max)
	if err == engine.ErrEOF {
		err = nil
	}
	return
}

// Probe reports why key cannot be used. It has the typed-nil bug in a local
// variable: oe stays a nil pointer for a usable key.
func (r *Repo) /*@probe*/Probe(key string) error {
	var oe *kverr.OpError
	if key == "" {
		oe = &kverr.OpError{Op: "probe", Err: kverr.ErrEmptyKey}
	}
	return oe
}

// Stat is Exists done right, through the nil-safe (*OpError).OrNil.
func (r *Repo) /*@stat*/Stat(key string) error { return r.find(key).OrNil() }

// Alive is Ping for callers that treat a closed engine as healthy.
func (r *Repo) /*@alive*/Alive() error {
	err := r.Ping()
	if err == engine.ErrClosed {
		return nil
	}
	return err
}
