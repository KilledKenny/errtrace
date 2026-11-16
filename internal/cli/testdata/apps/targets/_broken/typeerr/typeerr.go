// Package typeerr deliberately fails to type-check. Its directory starts
// with an underscore, so ./... never matches it.
package typeerr

import "errors"

// ErrBroken is returned by Broken.
var ErrBroken = errors.New("typeerr: broken")

// Broken has a type error in its body.
func /*@broken*/Broken() error {
	var n int = "not a number"
	_ = n
	return ErrBroken
}
