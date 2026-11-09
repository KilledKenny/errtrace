package span

import (
	"encoding/json"
	"go/token"
	"strings"
	"testing"
)

func pt(line, col, off int) Point { return Point{Line: line, Col: col, Offset: off} }

func TestParseAccepted(t *testing.T) {
	const cwd = "/work"
	tests := []struct {
		arg        string
		file       string
		start, end Point
	}{
		{"a.go", "/work/a.go", NoPoint, NoPoint},
		{"a.go:3", "/work/a.go", pt(3, 1, -1), pt(3, 1, -1)},
		{"a.go:3:4", "/work/a.go", pt(3, 4, -1), pt(3, 4, -1)},
		{"a.go:#12", "/work/a.go", pt(0, 0, 12), pt(0, 0, 12)},
		{"a.go:#0", "/work/a.go", pt(0, 0, 0), pt(0, 0, 0)},
		{"a.go:3#12", "/work/a.go", pt(3, 1, 12), pt(3, 1, 12)},
		{"a.go:3:4#12", "/work/a.go", pt(3, 4, 12), pt(3, 4, 12)},
		{"a.go:3:4-5:6", "/work/a.go", pt(3, 4, -1), pt(5, 6, -1)},
		{"a.go:3:4-9", "/work/a.go", pt(3, 4, -1), pt(3, 9, -1)},
		{"a.go:3-5", "/work/a.go", pt(3, 1, -1), pt(5, 1, -1)},
		{"a.go:#3-#9", "/work/a.go", pt(0, 0, 3), pt(0, 0, 9)},
		{"a.go:3:4#20-3:9#25", "/work/a.go", pt(3, 4, 20), pt(3, 9, 25)},
		{"a.go:3:4#20-9#25", "/work/a.go", pt(3, 4, 20), pt(3, 9, 25)},
		{"sub/../b/a.go:1:1", "/work/b/a.go", pt(1, 1, -1), pt(1, 1, -1)},
		{"/abs/dir/a.go:7:2", "/abs/dir/a.go", pt(7, 2, -1), pt(7, 2, -1)},
		{"file-1.go:2:3", "/work/file-1.go", pt(2, 3, -1), pt(2, 3, -1)},
		{"v2/x123:2", "/work/v2/x123", pt(2, 1, -1), pt(2, 1, -1)},
		{"/tmp/x:y/a.go:2:3", "/tmp/x:y/a.go", pt(2, 3, -1), pt(2, 3, -1)},
	}
	for _, tt := range tests {
		got, err := Parse(tt.arg, cwd)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.arg, err)
			continue
		}
		want := Span{File: tt.file, Start: tt.start, End: tt.end}
		if got != want {
			t.Errorf("Parse(%q) = %+v, want %+v", tt.arg, got, want)
		}
	}
}

func TestParseDriveLetter(t *testing.T) {
	// Parsing from the right leaves the drive letter in the file name. On
	// Unix the path is not absolute, so it is joined to cwd.
	for _, arg := range []string{`C:\x.go:3:4`, `C:x.go:3:4`} {
		got, err := Parse(arg, "/work")
		if err != nil {
			t.Errorf("Parse(%q): %v", arg, err)
			continue
		}
		if !strings.HasSuffix(got.File, strings.TrimSuffix(arg, ":3:4")) || got.Start != pt(3, 4, -1) {
			t.Errorf("Parse(%q) = %+v", arg, got)
		}
	}
}

func TestParseRejected(t *testing.T) {
	tests := []struct{ arg, wantErr string }{
		{"a.go::3", "missing line"},
		{"a.go:x:3", `"x" is not a number`},
		{"a.go:0:1", "line must be at least 1"},
		{"a.go:1:0", "column must be at least 1"},
		{"a.go:-1", "missing start position"},
		{"a.go:", "missing start position"},
		{"a.go:3:", "missing column"},
		{"a.go:#", "missing offset"},
		{"a.go:3#", "missing offset"},
		{"a.go:3:4-", "missing end position"},
		{"a.go:3:4-:5", "end: missing line"},
		{":3:4", "missing file name"},
		{"", "missing file name"},
		{"a.go:3:4:5", `column "4:5" is not a valid number`},
		{"a.go:5:4-3:1", "span end is before its start"},
		{"a.go:3:4-2", "span end is before its start"},
		{"a.go:#9-#3", "span end is before its start"},
		{"a.go:99999999999999999999", "is not a valid number"},
	}
	for _, tt := range tests {
		got, err := Parse(tt.arg, "/work")
		if err == nil {
			t.Errorf("Parse(%q) = %+v, want error containing %q", tt.arg, got, tt.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("Parse(%q) error = %q, want it to contain %q", tt.arg, err, tt.wantErr)
		}
		if !strings.Contains(err.Error(), tt.arg) {
			t.Errorf("Parse(%q) error = %q, want it to quote the argument", tt.arg, err)
		}
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		s    Span
		want string
	}{
		{Span{"/a.go", pt(3, 4, -1), pt(3, 4, -1)}, "/a.go:3:4-4"},
		{Span{"/a.go", pt(3, 4, 20), pt(3, 9, 25)}, "/a.go:3:4-9"},
		{Span{"/a.go", pt(3, 4, -1), pt(5, 2, -1)}, "/a.go:3:4-5:2"},
		{Span{"/a.go", pt(0, 0, 3), pt(0, 0, 9)}, "/a.go:#3-#9"},
		{Span{"/a.go", NoPoint, NoPoint}, "/a.go"},
	}
	for _, tt := range tests {
		if got := Format(tt.s); got != tt.want {
			t.Errorf("Format(%+v) = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	for _, s := range []Span{
		{"/dir/a.go", pt(3, 4, -1), pt(3, 9, -1)},   // single line
		{"/dir/a.go", pt(3, 4, -1), pt(7, 2, -1)},   // multi-line
		{"/dir/a.go", pt(3, 4, -1), pt(3, 4, -1)},   // point
		{"/dir/a.go", pt(0, 0, 10), pt(0, 0, 12)},   // offsets only
		{"/dir/é.go", pt(1, 10, -1), pt(1, 12, -1)}, // non-ASCII path
	} {
		got, err := Parse(Format(s), "/elsewhere")
		if err != nil {
			t.Errorf("Parse(Format(%+v)): %v", s, err)
			continue
		}
		if got != s {
			t.Errorf("Parse(Format(s)) = %+v, want %+v", got, s)
		}
	}
}

// file builds a token.File for src named /dir/a.go.
func file(src string) (*token.FileSet, *token.File) {
	fset := token.NewFileSet()
	f := fset.AddFile("/dir/a.go", -1, len(src))
	f.SetLinesForContent([]byte(src))
	return fset, f
}

// The second line holds a non-ASCII identifier: é is 2 bytes, so the
// column after "var é" is 7, not 6.
const src = "package p\nvar é = 1\n\nfunc F() {}"

func TestResolve(t *testing.T) {
	_, f := file(src)
	tests := []struct {
		arg        string
		start, end Point
	}{
		{"/dir/a.go", pt(1, 1, 0), pt(1, 1, 0)},
		{"/dir/a.go:2", pt(2, 1, 10), pt(2, 1, 10)},
		{"/dir/a.go:2:5", pt(2, 5, 14), pt(2, 5, 14)},    // é
		{"/dir/a.go:2:7", pt(2, 7, 16), pt(2, 7, 16)},    // the space after é
		{"/dir/a.go:#16", pt(2, 7, 16), pt(2, 7, 16)},    // same, by offset
		{"/dir/a.go:2:11", pt(2, 11, 20), pt(2, 11, 20)}, // one past end of line
		{"/dir/a.go:3:1", pt(3, 1, 21), pt(3, 1, 21)},    // empty line
		{"/dir/a.go:4:12", pt(4, 12, 33), pt(4, 12, 33)}, // EOF, no trailing newline
		{"/dir/a.go:#33", pt(4, 12, 33), pt(4, 12, 33)},
		{"/dir/a.go:2:5-7", pt(2, 5, 14), pt(2, 7, 16)},
		{"/dir/a.go:2:5-4:5", pt(2, 5, 14), pt(4, 5, 26)},
		{"/dir/a.go:2:5#14", pt(2, 5, 14), pt(2, 5, 14)},
	}
	for _, tt := range tests {
		s, err := Parse(tt.arg, "/")
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.arg, err)
		}
		got, err := Resolve(s, f)
		if err != nil {
			t.Errorf("Resolve(%q): %v", tt.arg, err)
			continue
		}
		want := Span{File: "/dir/a.go", Start: tt.start, End: tt.end}
		if got != want {
			t.Errorf("Resolve(%q) = %+v, want %+v", tt.arg, got, want)
		}
	}
}

func TestResolveOutOfRange(t *testing.T) {
	_, f := file(src)
	tests := []struct{ arg, wantErr string }{
		{"/dir/a.go:5:1", "line 5 is out of range (file has 4 lines)"},
		{"/dir/a.go:2:12", "column 12 is out of range (line 2 has 10 bytes)"},
		{"/dir/a.go:4:13", "column 13 is out of range"},
		{"/dir/a.go:#34", "offset #34 is out of range (file has 33 bytes)"},
		{"/dir/a.go:2:5#15", "offset #15 does not match 2:5"},
		{"/dir/a.go:2:5-9:1", "span end: line 9 is out of range"},
		{"/dir/a.go:2:5-#3", "span end 1:4 is before its start 2:5"},
	}
	for _, tt := range tests {
		s, err := Parse(tt.arg, "/")
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.arg, err)
		}
		got, err := Resolve(s, f)
		if err == nil {
			t.Errorf("Resolve(%q) = %+v, want error containing %q", tt.arg, got, tt.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("Resolve(%q) error = %q, want it to contain %q", tt.arg, err, tt.wantErr)
		}
	}
}

func TestOf(t *testing.T) {
	fset, f := file(src)
	got := Of(fset, f.Pos(14), f.Pos(16))
	want := Span{File: "/dir/a.go", Start: pt(2, 5, 14), End: pt(2, 7, 16)}
	if got != want {
		t.Errorf("Of = %+v, want %+v", got, want)
	}
	if s := Format(got); s != "/dir/a.go:2:5-7" {
		t.Errorf("Format(Of) = %q", s)
	}
	point := Of(fset, f.Pos(14), token.NoPos)
	if !point.IsPoint() || point.Start != pt(2, 5, 14) {
		t.Errorf("Of(pos, NoPos) = %+v, want point span", point)
	}
}

func TestJSON(t *testing.T) {
	s := Span{File: "/dir/my file.go", Start: pt(2, 5, 14), End: pt(2, 7, 16)}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"uri":"file:///dir/my%20file.go","start":{"line":2,"column":5,"offset":14},"end":{"line":2,"column":7,"offset":16}}`
	if string(data) != want {
		t.Errorf("Marshal = %s\nwant      %s", data, want)
	}
	var back Span
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back != s {
		t.Errorf("Unmarshal = %+v, want %+v", back, s)
	}
}
