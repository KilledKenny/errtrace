// Command errtrace answers a Query: given a Target (a function declaration or
// a call site), it reports the Error Set, every Error Origin that can reach
// the Target's error result together with its Propagation Path.
package main

import (
	"os"

	"github.com/killedKenny/errtrace/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
