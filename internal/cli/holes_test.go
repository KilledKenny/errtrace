package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KilledKenny/errtrace/internal/fixture"
)

// runWithin runs the CLI in dir with args and fails the test if it has
// not finished within limit. It returns stdout.
func runWithin(t *testing.T, dir string, args []string, limit time.Duration) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run(context.Background(), Options{Args: args, Dir: dir, Env: fixture.Env(), Stdout: &stdout, Stderr: &stderr})
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr.String())
		}
	case <-time.After(limit):
		t.Fatalf("still running after %s", limit)
	}
	return stdout.String()
}

// entryCount returns the number of entries of an Error Set printed as
// text: the lines after the first that are not indented.
func entryCount(out string) int {
	n := 0
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n")[1:] {
		if !strings.HasPrefix(line, "\t") {
			n++
		}
	}
	return n
}

// TestSelfFeedingStoresFinish runs the fixtures whose stores wrap what
// they load from their own variable or field. They once did not finish
// (a Join of a variable with itself squared the values on every pass) or
// grew with every combination of the stores; each must finish quickly
// with a small Error Set.
func TestSelfFeedingStoresFinish(t *testing.T) {
	for _, name := range []string{
		"self_join_defer", "self_join_field", "self_cycle_two_fields",
		"defer_join_three", "defer_errorf_two", "field_join_three", "var_closure_self_wrap", "field_recursive",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir, ar := fixture.Extract(t, filepath.Join("testdata", "e2e", "holes", name+".txtar"))
			out := runWithin(t, dir, fixture.Args(t, ar), 30*time.Second)
			if n := entryCount(out); n > 40 {
				t.Errorf("%d entries, want a set that does not grow with the chain depth:\n%s", n, out)
			}
		})
	}
}

// TestCutKeepsUnresolved checks that cutting a set of values at the size
// cap never drops an Unresolved value. A field is stored more Sentinels
// than one result can carry, and may also be written through an escaped
// pointer, which is the last value found. F wraps what it loads, G
// returns it: both Error Sets are cut, both say so with a fixpoint-limit
// entry, and both keep the entry for the escaped pointer.
func TestCutKeepsUnresolved(t *testing.T) {
	t.Parallel()
	const n = 2200 // more than summary.maxValues
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n\t\"errors\"\n\t\"fmt\"\n)\n\n")
	for i := range n {
		fmt.Fprintf(&b, "var E%d = errors.New(\"e%d\")\n", i, i)
	}
	b.WriteString("\ntype S struct{ err error }\n\nfunc fill(s *S, k int) {\n\tswitch k {\n")
	for i := range n {
		fmt.Fprintf(&b, "\tcase %d:\n\t\ts.err = E%d\n", i, i)
	}
	b.WriteString("\t}\n}\n\nvar Hidden = errors.New(\"hidden\")\n\nfunc poke(p *error) { *p = Hidden }\n\n")
	b.WriteString("func F(s *S) error {\n\tif s.err != nil {\n\t\treturn fmt.Errorf(\"f: %w\", s.err)\n\t}\n\treturn nil\n}\n\n")
	b.WriteString("func G(s *S) error { return s.err }\n\n")
	b.WriteString("func main() { s := &S{}; fill(s, 3); poke(&s.err); fmt.Println(F(s), G(s)) }\n")
	src := b.String()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.24\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o666); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"F", "G"} {
		line := 1 + strings.Count(src[:strings.Index(src, "func "+fn+"(")], "\n")
		out := runWithin(t, dir, []string{fmt.Sprintf("a.go:%d:6", line)}, 2*time.Minute)
		if !strings.Contains(out, "unresolved(fixpoint-limit)") {
			t.Errorf("%s: the cut set has no fixpoint-limit entry:\n%s", fn, firstLines(out, 3))
		}
		if !strings.Contains(out, "may be written through an escaped pointer") {
			t.Errorf("%s: the cut dropped the Unresolved entry of the escaped pointer:\n%s", fn, firstLines(out, 3))
		}
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
