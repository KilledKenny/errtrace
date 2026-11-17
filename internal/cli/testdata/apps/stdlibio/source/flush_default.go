//go:build !durable

package source

import "os"

// flush is a no-op unless the importer is built with the durable tag.
func /*@flush*/flush(f *os.File) error {
	return nil
}
