// Package fixture is the test harness for end-to-end Query fixtures. It is
// imported only from _test.go files.
//
// A fixture is a txtar archive. The "args" section holds the command-line
// arguments; "stdout", "stderr" (default empty) and "exit" (default 0) hold
// the expected result; every other file is written into a fresh temporary
// module directory. In args, file.go:@name is replaced by file.go:L:C of the
// byte right after the comment /*@name*/ in file.go, and file.go:#@name by
// file.go:#OFFSET.
//
// Before comparison the output is normalised: the temporary directory becomes
// $WORK, and spans under $GOROOT become $GOROOT/src/<path>:* (in JSON, their
// "start" and "end" become "*"), because stdlib line numbers change between
// Go versions. Run the tests with -update to rewrite the result sections in
// place; everything else in the archive is kept byte for byte.
package fixture

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/txtar"

	"github.com/killedKenny/errtrace/internal/span"
)

var update = flag.Bool("update", false, "rewrite the stdout, stderr and exit sections of fixtures")

// Invocation is what a fixture asks the code under test to run: the
// arguments (with @markers substituted), the module directory and the
// environment.
type Invocation struct {
	Dir  string
	Env  []string
	Args []string
}

// Result is what one Invocation produced.
type Result struct {
	Stdout string
	Stderr string
	Exit   int
}

// RunFunc runs one Invocation, typically cli.Run in-process.
type RunFunc func(t *testing.T, inv Invocation) Result

// Result sections of an archive; all other sections are module files.
const (
	sectionArgs   = "args"
	sectionStdout = "stdout"
	sectionStderr = "stderr"
	sectionExit   = "exit"
)

func isSpecial(name string) bool {
	switch name {
	case sectionArgs, sectionStdout, sectionStderr, sectionExit:
		return true
	}
	return false
}

// testEnv overrides the environment so that fixtures never use the network
// or a different toolchain.
var testEnv = []string{"GOPROXY=off", "GOFLAGS=-mod=mod", "GOWORK=off", "GOTOOLCHAIN=local"}

// Run runs the fixture at path: it writes its module files into a temporary
// directory, substitutes markers in its args, calls fn and compares the
// normalised result with the archive's result sections, or rewrites them
// when the test binary runs with -update.
func Run(t *testing.T, path string, fn RunFunc) {
	t.Helper()
	runFixture(t, path, fn, *update)
}

func runFixture(t *testing.T, path string, fn RunFunc, update bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ar := txtar.Parse(data)
	dir := t.TempDir()
	if err := writeFiles(dir, ar); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	args, err := substitute(string(section(ar, sectionArgs)), ar)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	res := fn(t, Invocation{Dir: dir, Env: Env(), Args: args})
	res.Stdout = Normalize(res.Stdout, dir)
	res.Stderr = Normalize(res.Stderr, dir)
	if update {
		if err := os.WriteFile(path, rewrite(ar, res), 0o666); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, msg := range compare(ar, res) {
		t.Errorf("%s: %s", path, msg)
	}
}

// Env returns the process environment with the fixture overrides applied.
func Env() []string {
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		overridden := false
		for _, o := range testEnv {
			overridden = overridden || strings.HasPrefix(o, key+"=")
		}
		if !overridden {
			env = append(env, kv)
		}
	}
	return append(env, testEnv...)
}

func section(ar *txtar.Archive, name string) []byte {
	for _, f := range ar.Files {
		if f.Name == name {
			return f.Data
		}
	}
	return nil
}

func writeFiles(dir string, ar *txtar.Archive) error {
	for _, f := range ar.Files {
		if isSpecial(f.Name) {
			continue
		}
		name := filepath.Join(dir, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(name, dir+string(filepath.Separator)) {
			return fmt.Errorf("file %q escapes the module directory", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(name), 0o777); err != nil {
			return err
		}
		if err := os.WriteFile(name, f.Data, 0o666); err != nil {
			return err
		}
	}
	return nil
}

var markerRef = regexp.MustCompile(`(\S+?):(#?)@(\w+)`)

// substitute splits args into fields and replaces file:@name and
// file:#@name with the marker's position in that archive file.
func substitute(args string, ar *txtar.Archive) ([]string, error) {
	var firstErr error
	out := markerRef.ReplaceAllStringFunc(args, func(m string) string {
		sub := markerRef.FindStringSubmatch(m)
		file, wantOffset, name := sub[1], sub[2] == "#", sub[3]
		line, col, off, err := markerPos(ar, file, name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return m
		}
		if wantOffset {
			return fmt.Sprintf("%s:#%d", file, off)
		}
		return fmt.Sprintf("%s:%d:%d", file, line, col)
	})
	if firstErr != nil {
		return nil, firstErr
	}
	return strings.Fields(out), nil
}

// markerPos finds the byte right after /*@name*/ in file.
func markerPos(ar *txtar.Archive, file, name string) (line, col, off int, err error) {
	var data []byte
	found := false
	for _, f := range ar.Files {
		if f.Name == file && !isSpecial(f.Name) {
			data, found = f.Data, true
		}
	}
	if !found {
		return 0, 0, 0, fmt.Errorf("marker @%s: no file %q in the archive", name, file)
	}
	comment := []byte("/*@" + name + "*/")
	i := bytes.Index(data, comment)
	if i < 0 {
		return 0, 0, 0, fmt.Errorf("marker @%s: no %s in %s", name, comment, file)
	}
	if bytes.Contains(data[i+len(comment):], comment) {
		return 0, 0, 0, fmt.Errorf("marker @%s: %s appears more than once in %s", name, comment, file)
	}
	off = i + len(comment)
	line = 1 + bytes.Count(data[:off], []byte("\n"))
	col = off - (bytes.LastIndexByte(data[:off], '\n') + 1) + 1
	return line, col, off, nil
}

var goroot = sync.OnceValue(func() string {
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
})

// Normalize masks the parts of output that vary between machines: work
// becomes $WORK, and spans under $GOROOT become $GOROOT/src/<path>:* (in
// JSON, the span's "start" and "end" become "*").
func Normalize(output, work string) string {
	return normalize(output, work, goroot())
}

func normalize(output, work, goroot string) string {
	if goroot != "" {
		src := filepath.Join(goroot, "src") + string(filepath.Separator)
		uri := span.FileURI(src)
		jsonSpan := regexp.MustCompile(`("uri":\s*")` + regexp.QuoteMeta(uri) +
			`([^"]*)("\s*,\s*"start":\s*)\{[^{}]*\}(\s*,\s*"end":\s*)\{[^{}]*\}`)
		output = jsonSpan.ReplaceAllString(output, `${1}file://$$GOROOT/src/${2}${3}"*"${4}"*"`)
		textSpan := regexp.MustCompile(regexp.QuoteMeta(src) + `(\S+?\.\w+):[0-9:#-]*[0-9]`)
		output = textSpan.ReplaceAllString(output, `$$GOROOT/src/${1}:*`)
	}
	var works []string
	if work != "" {
		works = append(works, work)
		if real, err := filepath.EvalSymlinks(work); err == nil && real != work {
			works = append(works, real)
		}
	}
	for _, w := range works {
		output = strings.ReplaceAll(output, span.FileURI(w), "file://$WORK")
	}
	for _, w := range works {
		output = strings.ReplaceAll(output, w, "$WORK")
	}
	return output
}

// fixNL makes s end in a newline, as txtar stores every section.
func fixNL(s string) string {
	if s != "" && !strings.HasSuffix(s, "\n") {
		return s + "\n"
	}
	return s
}

func wantExit(ar *txtar.Archive) (int, error) {
	text := strings.TrimSpace(string(section(ar, sectionExit)))
	if text == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("bad exit section %q", text)
	}
	return n, nil
}

// compare returns one message per result section that differs from res.
func compare(ar *txtar.Archive, res Result) []string {
	var msgs []string
	exit, err := wantExit(ar)
	if err != nil {
		msgs = append(msgs, err.Error())
	} else if exit != res.Exit {
		msgs = append(msgs, fmt.Sprintf("exit code = %d, want %d", res.Exit, exit))
	}
	for _, sec := range []struct{ name, got string }{
		{sectionStdout, res.Stdout},
		{sectionStderr, res.Stderr},
	} {
		got, want := fixNL(sec.got), string(section(ar, sec.name))
		if got != want {
			msgs = append(msgs, fmt.Sprintf("%s differs (run with -update to accept):\n--- got\n%s--- want\n%s", sec.name, got, want))
		}
	}
	return msgs
}

// rewrite returns the archive with its result sections replaced by res. The
// comment and every other section are kept as they were. A missing stderr or
// exit section is added only when its value is not the default.
func rewrite(ar *txtar.Archive, res Result) []byte {
	values := map[string]string{
		sectionStdout: fixNL(res.Stdout),
		sectionStderr: fixNL(res.Stderr),
		sectionExit:   strconv.Itoa(res.Exit) + "\n",
	}
	isDefault := map[string]bool{
		sectionStdout: false,
		sectionStderr: res.Stderr == "",
		sectionExit:   res.Exit == 0,
	}
	out := &txtar.Archive{Comment: ar.Comment}
	seen := map[string]bool{}
	for _, f := range ar.Files {
		if v, ok := values[f.Name]; ok {
			f.Data = []byte(v)
			seen[f.Name] = true
		}
		out.Files = append(out.Files, f)
	}
	// Insert a missing result section after the last section that precedes
	// it in the order args, stdout, stderr, exit (or first, if none does).
	order := []string{sectionArgs, sectionStdout, sectionStderr, sectionExit}
	for k, name := range order[1:] {
		if seen[name] || isDefault[name] {
			continue
		}
		at := 0
		for i, f := range out.Files {
			for _, prior := range order[:k+1] {
				if f.Name == prior {
					at = i + 1
				}
			}
		}
		file := txtar.File{Name: name, Data: []byte(values[name])}
		out.Files = append(out.Files[:at], append([]txtar.File{file}, out.Files[at:]...)...)
		seen[name] = true
	}
	return txtar.Format(out)
}
