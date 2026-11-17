// Package engine is the in-memory storage engine at the bottom of the stack.
package engine

import (
	"errors"
	"fmt"
	"io"

	_ "example.com/kvstore/engine/internal/sys" // body of fsync
	"example.com/kvstore/kverr"
)

// ErrClosed is initialised here and replaced in init.
var /*@errclosed*/ErrClosed = errors.New("closed")

var (
	// ErrEOF re-exports io.EOF.
	ErrEOF = io.EOF
	// ErrStale is replaced at run time by SetStaleError (unstable).
	ErrStale = errors.New("engine: stale snapshot")
	// ErrIgnored is never assigned: it is always nil.
	ErrIgnored error

	errTorn = errors.New("engine: torn write")
)

func init() {
	ErrClosed = errors.New("engine: closed")
}

// SetStaleError replaces ErrStale (used by embedders to localise messages).
func SetStaleError(err error) { ErrStale = err }

// paranoid enables extra verification; it is off in this build.
const paranoid = false

// fsync has no body here: see engine/internal/sys.
func /*@fsync_decl*/fsync(n int) error

// Engine stores values in memory.
type Engine struct {
	data    map[string][]byte
	version map[string]int
	limit   int
	closed  bool
	last    error
	errc    chan error
	done    chan struct{}
}

// Open creates an engine holding at most limit keys.
func /*@open*/Open(limit int) (*Engine, error) {
	if limit < 0 {
		return nil, fmt.Errorf("engine: bad limit %d", limit)
	}
	return &Engine{
		data:    make(map[string][]byte),
		version: make(map[string]int),
		limit:   limit,
		errc:    make(chan error, 1),
		done:    make(chan struct{}),
	}, nil
}

// Get returns the value stored under key.
func (e *Engine) /*@get*/Get(key string) ([]byte, error) {
	if e.closed {
		return nil, ErrClosed
	}
	v, ok := e.data[key]
	if !ok {
		return nil, kverr.ErrNotFound
	}
	return v, nil
}

func (e *Engine) /*@verify*/verify(key string) error {
	if paranoid {
		return e./*@dead_call*/deepVerify(key)
	}
	if false {
		return errTorn
	}
	if e.version[key] < 0 {
		return kverr.CodeTooLarge
	}
	return nil
}

// deepVerify is only called in paranoid builds.
func (e *Engine) /*@deepverify*/deepVerify(key string) error {
	if _, ok := e.data[key]; ok && e.version[key] == 0 {
		return kverr.ErrCorrupt
	}
	return nil
}

// Put stores val under key.
func (e *Engine) /*@put*/Put(key string, val []byte) error {
	if e.closed {
		return ErrClosed
	}
	if len(key) > 64 {
		return kverr.CodeTooLarge
	}
	if _, ok := e.data[key]; !ok && len(e.data) >= e.limit {
		return kverr.ErrQuota
	}
	e.data[key] = val
	e.version[key]++
	return /*@fsync*/fsync(len(val))
}

// Delete removes key. Deleting a missing key yields ErrIgnored (nil).
func (e *Engine) /*@delete*/Delete(key string) error {
	switch _, ok := e.data[key]; {
	case e.closed:
		return ErrClosed
	case key == "":
		return kverr.ErrEmptyKey
	case !ok:
		return ErrIgnored
	default:
		delete(e.data, key)
	}
	return nil
}

// Check verifies the bookkeeping of key.
func (e *Engine) /*@check*/Check(key string) error {
	if err := e.verify(key); err != nil {
		return err
	}
	return nil
}

// Snapshot checks that key is still at version want.
func (e *Engine) /*@snapshot*/Snapshot(key string, want int) error {
	if e.version[key] != want {
		return ErrStale
	}
	return nil
}

// Scan calls visit for every key with the prefix, twice if verify is set.
func (e *Engine) /*@scan*/Scan(prefix string, verify bool, visit func(key string) error) error {
	var err error
outer:
	for k := range e.data {
		if len(k) < len(prefix) || k[:len(prefix)] != prefix {
			continue
		}
		for pass := 0; pass < 2; pass++ {
			if err = visit(k); err != nil {
				break outer
			}
			if !verify {
				continue outer
			}
		}
	}
	if err != nil {
		return fmt.Errorf("scan %q: %w", prefix, err)
	}
	return nil
}

func (e *Engine) compactOnce(try int) error {
	if e.closed {
		return ErrClosed
	}
	if try < 2 && len(e.data)%2 == 1 {
		return errTorn
	}
	return nil
}

// Compact rewrites the store, retrying torn writes.
func (e *Engine) /*@compact*/Compact() error {
	var err error
	try := 0
retry:
	err = e.compactOnce(try)
	if err == errTorn && try < 3 {
		try++
		goto retry
	}
	return err
}

// Wait blocks until a background error, shutdown, or nothing is pending.
func (e *Engine) /*@wait*/Wait() error {
	select {
	case <-e.done:
		return ErrClosed
	case err := <-e.errc:
		return err
	default:
		return nil
	}
}

// Drain reads records until the end of the log.
func (e *Engine) /*@drain*/Drain(max int) (int, error) {
	n := 0
	for ; n < max; n++ {
		if n >= len(e.data) {
			return n, ErrEOF
		}
	}
	return n, nil
}

// LastErr returns the last recorded background error.
func (e *Engine) /*@lasterr*/LastErr() error { return e.last }

// Close shuts the engine down.
func (e *Engine) Close() error {
	if e.closed {
		return ErrClosed
	}
	e.closed = true
	close(e.done)
	return nil
}
