// Package store is the record store shared by both commands.
package store

import (
	"errors"
	"fmt"
)

// Sentinels of the store.
var (
	ErrNotFound   = errors.New("store: not found")
	ErrClosed     = errors.New("store: closed")
	ErrEmptyBatch = errors.New("store: empty batch")
)

// Backend is what a Store persists to.
type /*@backend_type*/Backend interface {
	/*@iface_read*/Read(key string) ([]byte, error)
	Write(key string, val []byte) error
}

// base is the state every store shares.
type base struct{ closed bool }

// Check reports whether the store is still usable.
func (b *base) /*@check*/Check() error {
	if b.closed {
		return ErrClosed
	}
	return nil
}

// Store is a counted view on a Backend.
type Store struct {
	base
	be   /*@field_type*/Backend
	hits int
}

// New returns a Store on be.
func /*@new*/New(be Backend) *Store { return &Store{be: be} }

// Get returns the value of key.
/*@get_kw*/func (/*@get_recv*/s *Store) /*@get*/Get(/*@get_param*/key string) ([]byte, /*@get_result*/error) {
	if err := s./*@embedded*/Check(); err != nil {
		return nil, err
	}
	/*@get_stmt*/s.hits++
	val, err := s.be./*@iface_call*/Read(key)
	if err != nil {
		/*@get_return*/return nil, fmt.Errorf(/*@get_string*/"get %q: %w", key, err)
	}
	return val, nil
/*@get_close*/}

// Hits returns how many values were read.
func (s Store) /*@hits*/Hits() int { return s.hits }

// Ping reports whether the store is open.
func (s Store) /*@ping*/Ping() error {
	if s.closed {
		return ErrClosed
	}
	return nil
}

// Close marks the store as closed.
func (s *Store) Close() error {
	if s.closed {
		return ErrClosed
	}
	s.closed = true
	return nil
}

// Batch collects writes.
type Batch struct {
	s   *Store
	n   int
	err error
}

// NewBatch starts a batch on s.
func NewBatch(s *Store) *Batch { return &Batch{s: s} }

// Add queues one write.
func (b *Batch) Add(key string, val []byte) *Batch {
	if b.err == nil {
		b.err = b.s.be.Write(key, val)
	}
	b.n++
	return b
}

// Commit finishes the batch.
func (b *Batch) Commit() error {
	if b.n == 0 {
		return ErrEmptyBatch
	}
	return b.err
}

type mem struct{ data map[string][]byte }

// NewMem returns an in-memory Backend holding data.
func NewMem(data map[string][]byte) Backend { return &mem{data: data} }

func (m *mem) Read(key string) ([]byte, error) {
	val, ok := m.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	return val, nil
}

func (m *mem) Write(key string, val []byte) error {
	if m.data == nil {
		return ErrClosed
	}
	m.data[key] = val
	return nil
}
