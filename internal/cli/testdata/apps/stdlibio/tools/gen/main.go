// Command gen writes a sample data file for manual testing. It lives in
// its own module, outside example.com/stdlibio.
package main

import (
	"errors"
	"fmt"
	"os"
)

var errNoPath = errors.New("gen: no output path")

func /*@generate*/generate(args []string) error {
	if len(args) == 0 {
		return errNoPath
	}
	return os.WriteFile(args[0], []byte("name,count\nwidget,3\n"), 0o644)
}

func main() {
	if err := generate(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
