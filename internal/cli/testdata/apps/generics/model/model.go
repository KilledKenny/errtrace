// Package model holds the entities stored by the demo.
package model

import (
	"fmt"

	"example.com/generics/errs"
	"example.com/generics/validate"
)

// User is keyed by its numeric ID.
type User struct {
	ID   validate.UserID
	Name validate.Name
}

func (u User) Key() validate.UserID { return u.ID }

func (u User) Validate() error {
	if err := validate.Check(u.ID); err != nil {
		return fmt.Errorf("user id: %w", err)
	}
	return validate.Check(u.Name)
}

// Tag is keyed by its own text.
type Tag string

func (t Tag) Key() string { return string(t) }

func (t Tag) Validate() error {
	if len(t) > 8 {
		return errs.ErrTooLong
	}
	return nil
}
