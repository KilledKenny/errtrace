// Package cache is a typed in-memory cache.
package cache

import "example.com/generics/errs"

// Entry is one key/value pair.
type Entry[K comparable, V any] struct {
	Key K
	Val V
}

// Flusher is a non-generic interface implemented by every cache instance.
type Flusher interface {
	Flush() error
}

// Cache is a bounded map.
type Cache[K comparable, V any] struct {
	items map[K]V
	limit int
}

// New returns an empty cache holding at most limit entries.
func New[K comparable, V any](limit int) *Cache[K, V] {
	return &Cache[K, V]{items: make(map[K]V), limit: limit}
}

// Get returns the value stored under k.
func (c *Cache[K, V]) /*@get*/Get(k K) (V, error) {
	v, ok := c.items[k]
	if !ok {
		var zero V
		return zero, errs.ErrNotFound
	}
	return v, nil
}

// Put stores v under k; keys are write-once.
func (c *Cache[K, V]) /*@put*/Put(k K, v V) error {
	if _, dup := c.items[k]; dup {
		return errs.ErrDuplicate
	}
	if len(c.items) >= c.limit {
		return &errs.TypedErr[K]{Op: "put", Val: k}
	}
	c.items[k] = v
	return nil
}

// GetOrLoad returns the cached value or loads and caches it.
func (c *Cache[K, V]) /*@getorload*/GetOrLoad(k K, load func(K) (V, error)) (V, error) {
	v, err := c.Get(k)
	if err == errs.ErrNotFound {
		v, err = load(k)
		if err != nil {
			return v, errs.Wrap(k, err)
		}
		_ = c.Put(k, v)
		return v, nil
	}
	return v, err
}

// Flush empties the cache; flushing an empty cache is an error.
func (c *Cache[K, V]) Flush() error {
	if len(c.items) == 0 {
		return errs.ErrEmpty
	}
	clear(c.items)
	return nil
}

// Evict removes k. No caller in this program uses it.
func (c *Cache[K, V]) /*@evict*/Evict(k K) error {
	if _, ok := c.items[k]; !ok {
		return errs.ErrNotFound
	}
	delete(c.items, k)
	return nil
}

// GetOr returns def instead of failing on a missing key.
func /*@getor*/GetOr[K comparable, V any](c *Cache[K, V], k K, def V) (V, error) {
	v, err := c.Get(k)
	if err == errs.ErrNotFound {
		return def, nil
	}
	return v, err
}
