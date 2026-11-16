// Package syntaxerr deliberately fails to parse.
package syntaxerr

import "errors"

// Half is missing the rest of its body.
func /*@half*/Half() error {
	return errors.New("half"
}
