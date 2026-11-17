package main

import (
	"example.com/generics/cache"
	"example.com/generics/errs"
	"example.com/generics/model"
	"example.com/generics/repo"
	"example.com/generics/result"
	"example.com/generics/seq"
	"example.com/generics/validate"
)

// Scores is an alias of one cache instantiation.
type Scores = cache.Cache[string, int]

// Pair is a generic alias of cache.Entry.
type Pair[K comparable, V any] = cache.Entry[K, V]

// fill calls a generic method through the alias types.
func fill(c *Scores, ps []Pair[string, int]) error {
	for _, p := range ps {
		if err := c./*@aliasput*/Put(p.Key, p.Val); err != nil {
			return err
		}
	}
	return nil
}

// score never fails on a missing key: GetOr handles ErrNotFound with ==.
func score(c *Scores, k string) (int, error) {
	return cache./*@getor*/GetOr(c, k, 0)
}

// lazy loads through a callback that fails with a generic-alias error type.
func lazy(c *Scores, k string) (int, error) {
	return c./*@lazy*/GetOrLoad(k, func(k string) (int, error) {
		return 0, &errs.Failure[string]{Op: "load", Val: k}
	})
}

// session uses the promoted Put and the overriding Get of the embedding type.
func session(t *cache.TTL[string, int], k string) (int, error) {
	if err := t./*@promoted*/Put(k, 1); err != nil {
		return 0, err
	}
	return t./*@ttlget*/Get(k)
}

// nameOf calls a generic method through a method value.
func nameOf(c *cache.Cache[validate.UserID, validate.Name], id validate.UserID) (validate.Name, error) {
	get := c.Get
	return /*@methodval*/get(id)
}

// viaEqual guards with a generic comparable helper instead of a plain ==.
func /*@viaequal*/viaEqual(c *Scores, k string) (int, error) {
	v, err := c.Get(k)
	if errs.Equal(err, errs.ErrNotFound) {
		return 0, nil
	}
	return v, err
}

// tag hides the cache error inside a generic struct with no Unwrap.
func /*@tag*/tag(c *Scores, k string) error {
	if _, err := c.Get(k); err != nil {
		return errs.Tag(err, k)
	}
	return nil
}

// viaResult routes an error through the fields of a generic Result.
func viaResult(s string) (int, error) {
	r := result.Try(func() (int, error) { return parsePort(s) })
	return r./*@resget*/Get()
}

// firstName unwraps an Option.
func firstName(o result.Option[validate.Name]) (validate.Name, error) {
	return o./*@optunwrap*/Unwrap()
}

// flushAll calls generic methods through the non-generic Flusher interface.
func flushAll(fs ...cache.Flusher) error {
	for _, f := range fs {
		if err := f./*@flush*/Flush(); err != nil {
			return err
		}
	}
	return nil
}

// saveUser saves into the user repository.
func saveUser(r *repo.Repo[validate.UserID, model.User], u model.User) error {
	return r./*@saveuser*/Save(u)
}

// saveTags saves into the tag repository through the generic SaveAll.
func saveTags(r *repo.Repo[string, model.Tag], ts []model.Tag) error {
	return repo./*@savetags*/SaveAll(r, ts)
}

// findUser retries a lookup.
func /*@finduser*/findUser(r *repo.Repo[validate.UserID, model.User], id validate.UserID) (model.User, error) {
	return seq.Retry(3, func() (model.User, error) { return r.Find(id) })
}
