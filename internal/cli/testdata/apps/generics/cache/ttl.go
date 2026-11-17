package cache

import (
	"errors"
	"fmt"

	"example.com/generics/errs"
)

// TTL is a Cache whose entries can expire. It embeds the generic Cache, so
// Put is promoted while Get and Flush are overridden.
type TTL[K comparable, V any] struct {
	*Cache[K, V]
	expired map[K]bool
}

// NewTTL returns an empty expiring cache.
func NewTTL[K comparable, V any](limit int) *TTL[K, V] {
	return &TTL[K, V]{Cache: New[K, V](limit), expired: make(map[K]bool)}
}

// Expire marks k as expired.
func (t *TTL[K, V]) Expire(k K) { t.expired[k] = true }

// Get fails for expired keys and otherwise defers to the embedded cache.
func (t *TTL[K, V]) /*@ttlget*/Get(k K) (V, error) {
	if t.expired[k] {
		var zero V
		return zero, fmt.Errorf("ttl: %w", errs.ErrExpired)
	}
	return t.Cache.Get(k)
}

// Flush tolerates an already empty cache.
func (t *TTL[K, V]) /*@ttlflush*/Flush() error {
	if err := t.Cache.Flush(); err != nil && !errors.Is(err, errs.ErrEmpty) {
		return err
	}
	clear(t.expired)
	return nil
}
