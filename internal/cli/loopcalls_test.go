package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KilledKenny/errtrace/internal/fixture"
)

// TestLoopCallsMatchInline checks that loops wrapping through helper calls
// give, at the default chain depth, the same entries (origin and Wrap
// Chain) as the same loops with the Wraps written inline, and in about the
// same time: the call sites around one loop are solved together, and the
// Propagation Paths of their nested argument sources are computed once per
// source list, so the work does not multiply with their number.
//
// A loop through three helpers has more than maxValues Wrap Chains at the
// default depth: both forms are cut to the same number of entries with an
// Unresolved fixpoint-limit value, though not to the same entries (which
// ones are kept is the order the values are found in).
func TestLoopCallsMatchInline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		capped bool
	}{{"alternate", false}, {"nested", false}, {"three", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := loopCallEntries(t, tc.name+".txtar")
			inline := loopCallEntries(t, tc.name+"_inline.txtar")
			if len(calls) < 100 {
				t.Errorf("%s: %d entries, want the loop unrolled to the chain depth", tc.name, len(calls))
			}
			if tc.capped {
				limit := func(entries []string) bool {
					return slices.ContainsFunc(entries, func(e string) bool {
						return strings.HasPrefix(e, "unresolved(fixpoint-limit)")
					})
				}
				if len(calls) != len(inline) || !limit(calls) || !limit(inline) {
					t.Errorf("%s: %d entries (fixpoint limit %v), inline %d (fixpoint limit %v); want the same number, both at the limit",
						tc.name, len(calls), limit(calls), len(inline), limit(inline))
				}
				return
			}
			if !slices.Equal(calls, inline) {
				t.Errorf("%s: entries differ from the inline loop:\n--- calls\n%s\n--- inline\n%s",
					tc.name, strings.Join(calls, "\n"), strings.Join(inline, "\n"))
			}
		})
	}
}

var (
	spanPrefix = regexp.MustCompile(`^\t*\S+: `)
	morePaths  = regexp.MustCompile(` \[\+\d+ more paths\]`)
)

// loopCallEntries runs the fixture under testdata/loopcalls and returns
// its entries, sorted, each as its origin line and Wrap Chain lines
// without spans or paths.
func loopCallEntries(t *testing.T, file string) []string {
	t.Helper()
	dir, ar := fixture.Extract(t, filepath.Join("testdata", "loopcalls", file))
	args := fixture.Args(t, ar)
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run(context.Background(), Options{Args: args, Dir: dir, Env: fixture.Env(), Stdout: &stdout, Stderr: &stderr})
	}()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", file, code, stderr.String())
		}
	case <-time.After(2 * time.Minute):
		t.Fatalf("%s: still running after 2m", file)
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	var entries []string
	var cur []string
	inChain := false
	for _, line := range lines[1:] {
		switch {
		case !strings.HasPrefix(line, "\t"):
			if cur != nil {
				entries = append(entries, strings.Join(cur, " | "))
			}
			cur = []string{morePaths.ReplaceAllString(spanPrefix.ReplaceAllString(line, ""), "")}
			inChain = false
		case line == "\twrap chain (origin → target):":
			inChain = true
		case !strings.HasPrefix(line, "\t\t"):
			inChain = false
		case inChain:
			cur = append(cur, spanPrefix.ReplaceAllString(line, ""))
		}
	}
	if cur != nil {
		entries = append(entries, strings.Join(cur, " | "))
	}
	slices.Sort(entries)
	return entries
}
