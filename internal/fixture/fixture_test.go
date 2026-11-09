package fixture

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/txtar"
)

// echo is a RunFunc that reports its Invocation instead of running a Query.
func echo(t *testing.T, inv Invocation) Result {
	for _, want := range testEnv {
		if !slices.Contains(inv.Env, want) {
			t.Errorf("Env lacks %s", want)
		}
	}
	file := filepath.Join(inv.Dir, "a.go")
	if _, err := os.Stat(file); err != nil {
		t.Errorf("module file not written: %v", err)
	}
	return Result{
		Stdout: fmt.Sprintf("args: %s\nfile: %s\n", strings.Join(inv.Args, " "), file),
		Stderr: fmt.Sprintf("uri: file://%s\n", file),
		Exit:   3,
	}
}

func TestRunEcho(t *testing.T) {
	Run(t, filepath.Join("testdata", "echo.txtar"), echo)
}

func TestSubstitute(t *testing.T) {
	ar := txtar.Parse([]byte(`-- a.go --
package p

var é /*@after_e*/ = 1
func /*@f*/F() {}
-- dup.go --
/*@d*/ /*@d*/
`))
	tests := []struct{ args, want string }{
		{"a.go:@f", "a.go:4:12"},
		{"a.go:#@f", "a.go:#46"},
		{"-x a.go:@after_e", "-x a.go:3:20"}, // é is 2 bytes, so 2 columns
		{"a.go:@f\n  a.go:#@after_e", "a.go:4:12 a.go:#30"},
		{"no markers here", "no markers here"},
	}
	for _, tt := range tests {
		got, err := substitute(tt.args, ar)
		if err != nil {
			t.Errorf("substitute(%q): %v", tt.args, err)
			continue
		}
		if s := strings.Join(got, " "); s != tt.want {
			t.Errorf("substitute(%q) = %q, want %q", tt.args, s, tt.want)
		}
	}
	for _, tt := range []struct{ args, wantErr string }{
		{"a.go:@missing", "no /*@missing*/ in a.go"},
		{"b.go:@f", `no file "b.go"`},
		{"dup.go:@d", "more than once"},
	} {
		if _, err := substitute(tt.args, ar); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("substitute(%q) error = %v, want %q", tt.args, err, tt.wantErr)
		}
	}
}

func TestNormalize(t *testing.T) {
	const work = "/tmp/TestX123/001"
	const goroot = "/opt/go"
	tests := []struct{ name, in, want string }{
		{
			"work",
			"/tmp/TestX123/001/a.go:3:4-9: in example.com/fx.Load\n",
			"$WORK/a.go:3:4-9: in example.com/fx.Load\n",
		},
		{
			"goroot text",
			"/opt/go/src/io/io.go:44:5-8: sentinel io.EOF\n\t/opt/go/src/os/file.go:100:2-120:3: in os.Open\n",
			"$GOROOT/src/io/io.go:*: sentinel io.EOF\n\t$GOROOT/src/os/file.go:*: in os.Open\n",
		},
		{
			"goroot json compact",
			`{"uri":"file:///opt/go/src/io/io.go","start":{"line":44,"column":5,"offset":900},"end":{"line":44,"column":8,"offset":903}}`,
			`{"uri":"file://$GOROOT/src/io/io.go","start":"*","end":"*"}`,
		},
		{
			"goroot json indented",
			"{\n  \"uri\": \"file:///opt/go/src/io/io.go\",\n  \"start\": {\n    \"line\": 44,\n    \"column\": 5,\n    \"offset\": 900\n  },\n  \"end\": {\n    \"line\": 44,\n    \"column\": 8,\n    \"offset\": 903\n  }\n}\n",
			"{\n  \"uri\": \"file://$GOROOT/src/io/io.go\",\n  \"start\": \"*\",\n  \"end\": \"*\"\n}\n",
		},
		{
			"work json keeps positions",
			`{"uri":"file:///tmp/TestX123/001/a.go","start":{"line":1,"column":1,"offset":0}}`,
			`{"uri":"file://$WORK/a.go","start":{"line":1,"column":1,"offset":0}}`,
		},
		{
			"goroot outside src untouched",
			"/opt/go/bin/go\n",
			"/opt/go/bin/go\n",
		},
	}
	for _, tt := range tests {
		if got := normalize(tt.in, work, goroot); got != tt.want {
			t.Errorf("%s: normalize =\n%s\nwant\n%s", tt.name, got, tt.want)
		}
	}
}

func TestCompare(t *testing.T) {
	ar := txtar.Parse([]byte("-- stdout --\nok\n-- exit --\n2\n"))
	if msgs := compare(ar, Result{Stdout: "ok\n", Exit: 2}); len(msgs) != 0 {
		t.Errorf("compare(equal) = %q, want nothing", msgs)
	}
	msgs := compare(ar, Result{Stdout: "other\n", Stderr: "boom\n", Exit: 0})
	if len(msgs) != 3 {
		t.Fatalf("compare(different) = %q, want 3 messages", msgs)
	}
	for i, want := range []string{"exit code = 0, want 2", "stdout differs", "stderr differs"} {
		if !strings.Contains(msgs[i], want) {
			t.Errorf("message %d = %q, want it to contain %q", i, msgs[i], want)
		}
	}
}

// withoutResults returns the archive's bytes with the result sections
// dropped, to compare everything -update must keep.
func withoutResults(t *testing.T, data []byte) []byte {
	t.Helper()
	ar := txtar.Parse(data)
	var files []txtar.File
	for _, f := range ar.Files {
		if f.Name != sectionStdout && f.Name != sectionStderr && f.Name != sectionExit {
			files = append(files, f)
		}
	}
	ar.Files = files
	return txtar.Format(ar)
}

func TestUpdate(t *testing.T) {
	orig, err := os.ReadFile(filepath.Join("testdata", "update.txtar"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(txtar.Format(txtar.Parse(orig)), orig) {
		t.Fatal("testdata/update.txtar does not round-trip through txtar; fix the test data")
	}
	copyPath := filepath.Join(t.TempDir(), "update.txtar")
	if err := os.WriteFile(copyPath, orig, 0o666); err != nil {
		t.Fatal(err)
	}

	// First update: new stdout, plus stderr and exit sections that were absent.
	failing := func(t *testing.T, inv Invocation) Result {
		return Result{
			Stdout: "query " + strings.Join(inv.Args, " ") + "\n",
			Stderr: "in " + inv.Dir + "\n",
			Exit:   2,
		}
	}
	runFixture(t, copyPath, failing, true)
	updated, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := withoutResults(t, updated), withoutResults(t, orig); !bytes.Equal(got, want) {
		t.Errorf("-update changed more than the result sections:\n--- got\n%s--- want\n%s", got, want)
	}
	const wantHead = "This comment must survive -update.\n" +
		"-- args --\na.go:@f\n" +
		"-- stdout --\nquery a.go:3:12\n" +
		"-- stderr --\nin $WORK\n" +
		"-- exit --\n2\n" +
		"-- go.mod --\n"
	if !bytes.HasPrefix(updated, []byte(wantHead)) {
		t.Errorf("updated archive starts:\n%s\nwant:\n%s", updated[:min(len(updated), len(wantHead))], wantHead)
	}
	// The updated fixture now passes without -update.
	runFixture(t, copyPath, failing, false)

	// Second update: default stderr and exit keep their (now present) sections.
	passing := func(*testing.T, Invocation) Result { return Result{Stdout: "fine\n"} }
	runFixture(t, copyPath, passing, true)
	updated, err = os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(updated, []byte("-- stdout --\nfine\n-- stderr --\n-- exit --\n0\n-- go.mod --\n")) {
		t.Errorf("second update produced:\n%s", updated)
	}
	if got, want := withoutResults(t, updated), withoutResults(t, orig); !bytes.Equal(got, want) {
		t.Errorf("second -update changed more than the result sections:\n--- got\n%s--- want\n%s", got, want)
	}
}

func TestUpdateKeepsDefaultsAbsent(t *testing.T) {
	ar := txtar.Parse([]byte("-- args --\nx\n-- stdout --\nold\n-- a.go --\npackage a\n"))
	got := string(rewrite(ar, Result{Stdout: "new\n"}))
	const want = "-- args --\nx\n-- stdout --\nnew\n-- a.go --\npackage a\n"
	if got != want {
		t.Errorf("rewrite = %q, want %q", got, want)
	}
}
