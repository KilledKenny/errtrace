// Package repo is a generic repository built on cache, validate and seq.
package repo

import (
	"fmt"

	"example.com/generics/cache"
	"example.com/generics/errs"
	"example.com/generics/seq"
	"example.com/generics/validate"
)

// Entity is something that validates and has a comparable key.
type Entity[ID comparable] interface {
	validate.Validator
	Key() ID
}

// Repo stores entities of type T under keys of type ID.
type Repo[ID comparable, T Entity[ID]] struct {
	store *cache.Cache[ID, T]
}

// New returns an empty repository.
func New[ID comparable, T Entity[ID]](limit int) *Repo[ID, T] {
	return &Repo[ID, T]{store: cache.New[ID, T](limit)}
}

// Save validates e and stores it.
func (r *Repo[ID, T]) /*@save*/Save(e T) error {
	if err := validate.Check(e); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	if err := r.store.Put(e.Key(), e); err != nil {
		return errs.Wrap(e.Key(), err)
	}
	return nil
}

// Find loads the entity stored under id.
func (r *Repo[ID, T]) Find(id ID) (T, error) {
	e, err := r.store.Get(id)
	if err != nil {
		return e, fmt.Errorf("find %v: %w", id, err)
	}
	return e, nil
}

// SaveAll saves every entity, passing the bound method r.Save to seq.Each.
func /*@saveall*/SaveAll[ID comparable, T Entity[ID]](r *Repo[ID, T], es []T) error {
	return seq.Each(es, r.Save)
}
