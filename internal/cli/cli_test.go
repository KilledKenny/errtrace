package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
	_, stderr, _ := runInProcess(t, "-debug=target,timing,summary", "a.go:1:1")
	if stderr != BinaryName+": not implemented\n" {
		t.Errorf("valid -debug list: stderr = %q", stderr)
	}
}
