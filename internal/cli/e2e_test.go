package cli

import (
	"bytes"
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killedKenny/errtrace/internal/fixture"
)

// TestE2E runs every fixture under testdata/e2e through Run in-process.
// Use -update to rewrite their stdout, stderr and exit sections.
func TestE2E(t *testing.T) {
	root := filepath.Join("testdata", "e2e")
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".txtar") {
			paths = append(paths, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures found")
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator))), ".txtar")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture.Run(t, path, runCLI)
		})
	}
}

func runCLI(t *testing.T, inv fixture.Invocation) fixture.Result {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), Options{
		Args:   inv.Args,
		Dir:    inv.Dir,
		Env:    inv.Env,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	return fixture.Result{Stdout: stdout.String(), Stderr: stripTiming(stderr.String()), Exit: code}
}

// stripTiming drops the -debug=timing lines, which vary from run to run.
func stripTiming(s string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if !strings.HasPrefix(line, "timing:") {
			b.WriteString(line)
		}
	}
	return b.String()
}
