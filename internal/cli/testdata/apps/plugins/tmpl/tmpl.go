// Package tmpl renders event bodies.
package tmpl

import (
	"errors"
	"fmt"
	"io"

	"example.com/plugins/core"
)

var ErrTemplate = errors.New("tmpl: broken template")

// Source is a local io.Reader.
type Source struct {
	Data   string
	Broken bool
	off    int
}

func (s *Source) Read(p []byte) (int, error) {
	if s.Broken {
		return 0, ErrTemplate
	}
	if s.off >= len(s.Data) {
		return 0, io.EOF
	}
	n := copy(p, s.Data[s.off:])
	s.off += n
	return n, nil
}

// Load reads a whole template.
func /*@load*/Load(r io.Reader) (string, error) {
	b, err := io./*@readall*/ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("tmpl: %w", err)
	}
	return string(b), nil
}

// Header reads the first bytes of a template.
func /*@header*/Header(r io.Reader) error {
	var buf [4]byte
	_, err := r.Read(buf[:])
	return err
}

func formatJSON(e core.Event) (string, error) {
	if e.Body == "" {
		return "", core.ErrEmptyBody
	}
	return fmt.Sprintf("{%q: %q}", e.Topic, e.Body), nil
}

func formatText(e core.Event) (string, error) {
	return e.Topic + ": " + e.Body, nil
}

// formats is filled by a package-level initialiser.
var formats = map[string]func(core.Event) (string, error){
	"json": formatJSON,
	"text": formatText,
}

// Format renders e with the named formatter.
func /*@format*/Format(kind string, e core.Event) (string, error) {
	f, ok := formats[kind]
	if !ok {
		return "", fmt.Errorf("unknown format %q", kind)
	}
	return f(e)
}
