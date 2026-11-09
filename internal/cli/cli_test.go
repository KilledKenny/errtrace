package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/killedKenny/errtrace/internal/fixture"
)

func runInProcess(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = Run(context.Background(), Options{Args: args, Dir: t.TempDir(), Stdout: &out, Stderr: &errb})
	return out.String(), errb.String(), code
}

func TestHelpGoesToStdout(t *testing.T) {
	stdout, stderr, code := runInProcess(t, "-h")
	if code != ExitOK || stderr != "" {
		t.Errorf("-h: code %d, stderr %q; want 0 and empty stderr", code, stderr)
	}
	if !strings.HasPrefix(stdout, "usage: "+BinaryName+" [flags] <target>\n") {
		t.Errorf("-h stdout = %q", stdout)
	}
	if strings.Contains(stdout, "-debug") {
		t.Errorf("usage mentions the hidden -debug flag:\n%s", stdout)
	}
}

func TestErrorsUseBinaryNamePrefix(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"a.go:1:1", "b.go:1:1"},
		{"a.go:0:1"},
		{"-nosuchflag"},
		{"-debug=nope", "a.go:1:1"},
		{"a.go:1:1"},
	} {
		stdout, stderr, code := runInProcess(t, args...)
		if code != ExitError {
			t.Errorf("%q: code = %d, want %d", args, code, ExitError)
		}
		if stdout != "" {
			t.Errorf("%q: stdout = %q, want empty", args, stdout)
		}
		if !strings.HasPrefix(stderr, BinaryName+": ") {
			t.Errorf("%q: stderr = %q, want prefix %q", args, stderr, BinaryName+": ")
		}
	}
}

func TestUsageOnlyForUsageErrors(t *testing.T) {
	_, stderr, _ := runInProcess(t, "a.go:0:1")
	if strings.Contains(stderr, "usage:") {
		t.Errorf("a bad position is not a usage error, but usage was printed:\n%s", stderr)
	}
	_, stderr, _ = runInProcess(t)
	if !strings.Contains(stderr, "usage:") {
		t.Errorf("a missing Target is a usage error, but usage was not printed:\n%s", stderr)
	}
}

func TestDebugModes(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fx\n\ngo 1.24\n",
		"a.go":   "package fx\n\nfunc F() error { return nil }\n",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	var out, errb bytes.Buffer
	code := Run(context.Background(), Options{
		Args:   []string{"-debug=target,timing,summary", "a.go:3:6"},
		Dir:    dir,
		Env:    fixture.Env(),
		Stdout: &out,
		Stderr: &errb,
	})
	wantOut := filepath.Join(dir, "a.go") + ":3:6-7: Error Set of example.com/fx.F: 0 entries\n"
	if code != ExitOK || out.String() != wantOut {
		t.Errorf("valid -debug list: code %d, stdout %q; want 0 and stdout %q (debug output goes to stderr only)", code, out.String(), wantOut)
	}
	lines := strings.Split(strings.TrimSuffix(errb.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "timing: load=") || !strings.Contains(lines[0], " ssa=") ||
		!strings.HasPrefix(lines[1], "target: declaration example.com/fx.F funcs=[example.com/fx.F] span=") {
		t.Errorf("valid -debug list: stderr = %q, want a timing line and a target line", errb.String())
	}
}
