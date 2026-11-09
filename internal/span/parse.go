package span

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Parse reads a Target position in the gopls grammar and returns its Span,
// with a relative file resolved against cwd. The accepted forms are
//
//	file  file:L  file:L:C  file:#O  file:L#O  file:L:C#O
//
// optionally followed by a range end: -L:C, -#O, -L:C#O, or a single number N
// (with optional #O), which is the end column on the start line when the
// start had a column and the end line otherwise. Lines and columns are
// 1-based and count bytes; offsets are 0-based. Only file paths are accepted,
// not URIs.
//
// Parsing starts from the right, so Windows drive letters (C:\x.go:3:4) work.
// Unlike gopls, malformed positions are rejected rather than folded into the
// file name: a.go::3, a.go:x:3, a.go:0:1, a.go:1:0, a.go:-1 and empty
// components are errors.
//
// Missing parts are left unset (see Point); Resolve fills them in. When no
// range end is given, End == Start. A line without a column gets column 1.
func Parse(arg, cwd string) (Span, error) {
	file, pos := splitPosition(arg)
	if file == "" {
		return Span{}, fmt.Errorf("invalid position %q: missing file name", arg)
	}
	if bad, ok := badComponent(file); ok {
		return Span{}, fmt.Errorf("invalid position %q: %q is not a number", arg, bad)
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(cwd, file)
	}
	s := Span{File: filepath.Clean(file), Start: NoPoint, End: NoPoint}
	if pos == "" {
		return s, nil
	}
	if err := parseRange(&s, pos[1:]); err != nil {
		return Span{}, fmt.Errorf("invalid position %q: %w", arg, err)
	}
	return s, nil
}

// splitPosition splits arg into the file name and the position suffix, which
// is empty or starts with ':'. The suffix is the longest tail made only of
// digits and the separators ':', '#' and '-', cut at its first ':'.
func splitPosition(arg string) (file, pos string) {
	i := len(arg)
	for i > 0 && strings.IndexByte("0123456789:#-", arg[i-1]) >= 0 {
		i--
	}
	colon := strings.IndexByte(arg[i:], ':')
	if colon < 0 {
		return arg, ""
	}
	return arg[:i+colon], arg[i+colon:]
}

// badComponent reports a non-numeric position component left in the file
// name, as in a.go:x:3. A drive letter prefix (C:) is not a component.
func badComponent(file string) (string, bool) {
	base := file
	if i := strings.LastIndexAny(file, `/\`); i >= 0 {
		base = file[i+1:]
	}
	colon := strings.LastIndexByte(base, ':')
	if colon < 0 {
		return "", false
	}
	if len(base) == len(file) && colon == 1 && isLetter(file[0]) && strings.Count(base, ":") == 1 {
		return "", false // C:x.go
	}
	return base[colon+1:], true
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }

func parseRange(s *Span, pos string) error {
	startText, endText, hasEnd := strings.Cut(pos, "-")
	if startText == "" {
		return errors.New("missing start position")
	}
	start, hadCol, err := parsePoint(startText)
	if err != nil {
		return err
	}
	s.Start, s.End = start, start
	if !hasEnd {
		return nil
	}
	if endText == "" {
		return errors.New("missing end position after '-'")
	}
	end, endHadCol, err := parsePoint(endText)
	if err != nil {
		return fmt.Errorf("end: %w", err)
	}
	if end.HasPosition() && !endHadCol && hadCol {
		// -N after L:C is the end column on the start line.
		end.Line, end.Col = start.Line, end.Line
	}
	if before(end, start) {
		return errors.New("span end is before its start")
	}
	s.End = end
	return nil
}

// before reports whether a is known to come before b.
func before(a, b Point) bool {
	if a.HasPosition() && b.HasPosition() {
		return a.Line < b.Line || a.Line == b.Line && a.Col < b.Col
	}
	if a.HasOffset() && b.HasOffset() {
		return a.Offset < b.Offset
	}
	return false
}

// parsePoint parses L, L:C, #O, L#O or L:C#O. hadCol reports an explicit column.
func parsePoint(text string) (p Point, hadCol bool, err error) {
	p = NoPoint
	lc, off, hasOff := strings.Cut(text, "#")
	if hasOff {
		if p.Offset, err = number("offset", off, 0); err != nil {
			return Point{}, false, err
		}
		if lc == "" {
			return p, false, nil
		}
	}
	line, col, hadCol := strings.Cut(lc, ":")
	if p.Line, err = number("line", line, 1); err != nil {
		return Point{}, false, err
	}
	p.Col = 1
	if hadCol {
		if p.Col, err = number("column", col, 1); err != nil {
			return Point{}, false, err
		}
	}
	return p, hadCol, nil
}

func number(what, text string, min int) (int, error) {
	if text == "" {
		return 0, fmt.Errorf("missing %s", what)
	}
	n, err := strconv.Atoi(text)
	if err != nil || strings.ContainsAny(text, "+-") {
		return 0, fmt.Errorf("%s %q is not a valid number", what, text)
	}
	if n < min {
		return 0, fmt.Errorf("%s must be at least %d, got %d", what, min, n)
	}
	return n, nil
}
