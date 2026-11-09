// Package span implements the gopls position grammar that errtrace uses to
// name a Target on the command line, the span form it prints for every Error
// Origin, Wrap and Propagation Path hop, and the conversion between go/token
// positions and byte-based line/column spans.
//
// Lines and columns are 1-based and columns count bytes (not runes or UTF-16
// units). Offsets are 0-based byte offsets.
package span

import (
	"encoding/json"
	"fmt"
	"go/token"
	"net/url"
	"path/filepath"
	"strings"
)

// Point is one position in a file. Line and Col are 1-based and Col counts
// bytes; 0 means unset. Offset is a 0-based byte offset; -1 means unset.
type Point struct {
	Line   int
	Col    int
	Offset int
}

// NoPoint is a Point with no line, column or offset.
var NoPoint = Point{Offset: -1}

// HasPosition reports whether p has a line (and therefore a column).
func (p Point) HasPosition() bool { return p.Line > 0 }

// HasOffset reports whether p has a byte offset.
func (p Point) HasOffset() bool { return p.Offset >= 0 }

// IsValid reports whether p has a line or an offset.
func (p Point) IsValid() bool { return p.HasPosition() || p.HasOffset() }

// Span is a range in one file, such as the position of a Target, an Error
// Origin or a Propagation Path hop. File is an absolute, cleaned path. A
// point span has End == Start.
type Span struct {
	File  string
	Start Point
	End   Point
}

// IsPoint reports whether s covers a single point.
func (s Span) IsPoint() bool { return s.Start == s.End }

// String returns Format(s).
func (s Span) String() string { return Format(s) }

// Of builds the Span covering [start, end) in fset. Positions are taken
// unadjusted, so //line directives do not move them. If end is token.NoPos
// the Span is a point span at start. Of panics if start is not valid.
func Of(fset *token.FileSet, start, end token.Pos) Span {
	if !start.IsValid() {
		panic("span.Of: invalid start position")
	}
	sp := fset.PositionFor(start, false)
	s := Span{
		File:  filepath.Clean(sp.Filename),
		Start: Point{Line: sp.Line, Col: sp.Column, Offset: sp.Offset},
	}
	s.End = s.Start
	if end.IsValid() {
		ep := fset.PositionFor(end, false)
		s.End = Point{Line: ep.Line, Col: ep.Column, Offset: ep.Offset}
	}
	return s
}

// Resolve fills in the parts of s that Parse left unset (the offset from the
// line and column, or the line and column from the offset) using tokFile,
// whose contents s is assumed to describe, and checks that every part is in
// range. A column may point one byte past the end of its line, and an offset
// may equal the file size. A span with no position at all resolves to the
// start of the file.
func Resolve(s Span, tokFile *token.File) (Span, error) {
	if !s.Start.IsValid() {
		s.Start = Point{Line: 1, Col: 1, Offset: 0}
		s.End = s.Start
		return s, nil
	}
	start, err := resolvePoint(s.Start, tokFile)
	if err != nil {
		return Span{}, fmt.Errorf("%s: %w", s.File, err)
	}
	end := start
	if s.End.IsValid() {
		end, err = resolvePoint(s.End, tokFile)
		if err != nil {
			return Span{}, fmt.Errorf("%s: span end: %w", s.File, err)
		}
	}
	if end.Offset < start.Offset {
		return Span{}, fmt.Errorf("%s: span end %d:%d is before its start %d:%d",
			s.File, end.Line, end.Col, start.Line, start.Col)
	}
	s.Start, s.End = start, end
	return s, nil
}

func resolvePoint(p Point, f *token.File) (Point, error) {
	size := f.Size()
	var off int
	if p.HasPosition() {
		if p.Line > f.LineCount() {
			return Point{}, fmt.Errorf("line %d is out of range (file has %d lines)", p.Line, f.LineCount())
		}
		lineStart := f.Offset(f.LineStart(p.Line))
		lineEnd := size // the last line may lack a newline
		if p.Line < f.LineCount() {
			lineEnd = f.Offset(f.LineStart(p.Line+1)) - 1 // the newline
		}
		col := p.Col
		if col == 0 {
			col = 1
		}
		if col-1 > lineEnd-lineStart {
			return Point{}, fmt.Errorf("column %d is out of range (line %d has %d bytes)", col, p.Line, lineEnd-lineStart)
		}
		off = lineStart + col - 1
		if p.HasOffset() && p.Offset != off {
			return Point{}, fmt.Errorf("offset #%d does not match %d:%d (offset #%d)", p.Offset, p.Line, col, off)
		}
		return Point{Line: p.Line, Col: col, Offset: off}, nil
	}
	off = p.Offset
	if off > size {
		return Point{}, fmt.Errorf("offset #%d is out of range (file has %d bytes)", off, size)
	}
	pos := f.PositionFor(f.Pos(off), false)
	return Point{Line: pos.Line, Col: pos.Column, Offset: off}, nil
}

// FileURI returns the file:// URI of the absolute path p, as gopls writes it.
func FileURI(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // Windows drive letter: C:/x -> /C:/x
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// pathFromURI is the inverse of FileURI.
func pathFromURI(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parsing span uri: %w", err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("span uri %q is not a file URI", uri)
	}
	p := u.Path
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:] // /C:/x -> C:/x
	}
	return filepath.FromSlash(p), nil
}

type jsonPoint struct {
	Line   int `json:"line"`
	Col    int `json:"column"`
	Offset int `json:"offset"`
}

type jsonSpan struct {
	URI   string    `json:"uri"`
	Start jsonPoint `json:"start"`
	End   jsonPoint `json:"end"`
}

// MarshalJSON writes s in the shape gopls uses for `definition -json`:
// {"uri":"file:///abs/a.go","start":{"line":L,"column":C,"offset":O},"end":{...}}.
func (s Span) MarshalJSON() ([]byte, error) {
	return json.Marshal(jsonSpan{
		URI:   FileURI(s.File),
		Start: jsonPoint(s.Start),
		End:   jsonPoint(s.End),
	})
}

// UnmarshalJSON reads the shape written by MarshalJSON.
func (s *Span) UnmarshalJSON(data []byte) error {
	var js jsonSpan
	if err := json.Unmarshal(data, &js); err != nil {
		return err
	}
	file, err := pathFromURI(js.URI)
	if err != nil {
		return err
	}
	*s = Span{File: file, Start: Point(js.Start), End: Point(js.End)}
	return nil
}
