//go:build durable

package source

import (
	"fmt"
	"os"
)

// flush forces the output file to stable storage.
func /*@flush*/flush(f *os.File) error {
	if err := f.Sync(); err != nil {
		return fmt.Errorf("source: sync: %w", err)
	}
	return nil
}
