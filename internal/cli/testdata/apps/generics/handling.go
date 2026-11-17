package main

import (
	"errors"

	"example.com/generics/errs"
	"example.com/generics/model"
	"example.com/generics/repo"
	"example.com/generics/validate"
)

// onlyInts drops the string instantiation, naming it through the generic
// alias Failure.
func /*@onlyints*/onlyInts(n int, s string) error {
	err := classify(n, s)
	var fe *errs.Failure[string]
	if errors.As(err, &fe) {
		return nil
	}
	return err
}

// notFloats guards with an instantiation that classify never produces.
func /*@notfloats*/notFloats(n int, s string) error {
	err := classify(n, s)
	var fe *errs.TypedErr[float64]
	if errors.As(err, &fe) {
		return nil
	}
	return err
}

// liftRange passes a sentinel through the E-to-error conversion of Lift.
func liftRange() error {
	return errs./*@liftrange*/Lift(errs.ErrIntRange)
}

// rejected returns the concrete pointer result of a generic constructor.
func /*@rejected*/rejected(n int) error {
	if n < 0 {
		return errs.NewErr("rejected", n)
	}
	return nil
}

// quietFind treats a missing user as success: errors.Is has to see
// ErrNotFound through the %w wraps added inside the generic instances.
func /*@quietfind*/quietFind(r *repo.Repo[validate.UserID, model.User], id validate.UserID) error {
	_, err := findUser(r, id)
	if errors.Is(err, errs.ErrNotFound) {
		return nil
	}
	return err
}

// lazyQuiet drops loader failures by matching the generic wrapper type.
func /*@lazyquiet*/lazyQuiet(c *Scores, k string) error {
	_, err := lazy(c, k)
	var w *errs.Wrapped[string]
	if errors.As(err, &w) {
		return nil
	}
	return err
}

// peek reads through a method expression of an instantiated generic type.
func peek(c *Scores, k string) (int, error) {
	return (*Scores)./*@methodexpr*/Get(c, k)
}

// missingOK treats a missing user as success, checking Find directly.
func /*@missingok*/missingOK(r *repo.Repo[validate.UserID, model.User], id validate.UserID) error {
	_, err := r.Find(id)
	if errors.Is(err, errs.ErrNotFound) {
		return nil
	}
	return err
}
