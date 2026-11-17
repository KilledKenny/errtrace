// Command smoke is the smallest test application: it checks the harness.
package main

import (
	"fmt"

	"example.com/smoke/store"
)

func /*@load*/load(key string) error {
	if err := store.Get(key); err != nil {
		return fmt.Errorf("load %q: %w", key, err)
	}
	return nil
}

func main() { _ = load("k") }
