// Package source reads the input data files.
package source

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// ErrEmpty is returned for a data file without content.
var ErrEmpty = errors.New("source: empty file")

// ErrBadMagic is returned when a data file does not start with the magic.
var ErrBadMagic = errors.New("source: bad magic")

// Exists reports whether path exists; fs.ErrNotExist is not an error here.
func /*@exists*/Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("source: stat: %w", err)
	}
	return true, nil
}

// Missing is the complement: it reports only the not-exist condition.
func /*@missing*/Missing(path string) error {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Cleanup removes a temporary file, ignoring one that is already gone.
func /*@cleanup*/Cleanup(path string) error {
	err := os./*@remove*/Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Require fails with os.ErrNotExist when path is absent.
func /*@require*/Require(path string) error {
	ok, err := Exists(path)
	if err != nil {
		return err
	}
	if !ok {
		return os.ErrNotExist
	}
	return nil
}

// Optional is Require for files that may be absent: the os.ErrNotExist
// from Require is matched through its alias fs.ErrNotExist and dropped.
func /*@optional*/Optional(path string) error {
	err := Require(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Peek opens path and reads its magic; the deferred Close result is lost.
func /*@peek*/Peek(path string) ([4]byte, error) {
	f, err := os./*@open*/Open(path)
	if err != nil {
		return [4]byte{}, err
	}
	defer f./*@peekclose*/Close()
	return /*@magiccall*/Magic(f)
}

// Save writes the output file. Write, flush and close failures are joined.
func /*@save*/Save(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("source: create: %w", err)
	}
	_, werr := f.Write(data)
	return errors.Join(werr, /*@flushcall*/flush(f), f.Close())
}

// Durability describes how f was flushed. It always returns a non-nil
// error value, even when flush did nothing: fmt.Errorf never returns nil.
func /*@durability*/Durability(f *os.File) error {
	return fmt.Errorf("source: flush %s: %w", f.Name(), flush(f))
}

// Slurp reads a whole file. The Close error replaces a nil read error
// through the named result.
func /*@slurp*/Slurp(path string) (data []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("source: open: %w", err)
	}
	defer func() {
		if cerr := f./*@close*/Close(); cerr != nil && err == nil {
			err = fmt.Errorf("source: close: %w", cerr)
		}
	}()
	data, err = io./*@readall*/ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("source: read: %w", err)
	}
	if len(data) == 0 {
		return nil, ErrEmpty
	}
	return data, nil
}

// Magic reads the 4-byte magic from r. A short stream is reported as
// ErrBadMagic, a completely empty one as ErrEmpty.
func /*@magic*/Magic(r io.Reader) ([4]byte, error) {
	var buf [4]byte
	_, err := io./*@readfull*/ReadFull(r, buf[:])
	switch err {
	case nil:
		return buf, nil
	case io.EOF:
		return buf, ErrEmpty
	case io.ErrUnexpectedEOF:
		return buf, ErrBadMagic
	}
	return buf, err
}

// Header reads at least min bytes of header from an in-memory document.
func /*@header*/Header(doc string, min int) ([]byte, error) {
	buf := make([]byte, 64)
	n, err := io./*@readatleast*/ReadAtLeast(strings.NewReader(doc), buf, min)
	if err == io.EOF {
		return nil, nil
	}
	return buf[:n], err
}

// Lines splits an in-memory document into lines.
func /*@lines*/Lines(doc string) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(doc))
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out, sc./*@scanerr*/Err()
}

// FirstLine returns the first line of an in-memory document.
func /*@firstline*/FirstLine(doc string) (string, error) {
	br := bufio.NewReader(strings.NewReader(doc))
	return br./*@readstring*/ReadString('\n')
}

// CountLines counts lines with the io.EOF loop idiom: EOF ends the loop and
// is never returned.
func /*@countlines*/CountLines(doc string) (int, error) {
	br := bufio.NewReader(strings.NewReader(doc))
	n := 0
	for {
		_, err := br.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, fmt.Errorf("source: line %d: %w", n+1, err)
		}
		n++
	}
	return n, nil
}

// Finish reopens the output file written by Save and seals it.
func /*@finish*/Finish(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("source: reopen: %w", err)
	}
	return Seal(f, []byte("\n"))
}

// Seal appends the trailer to f, checks that the file is still there and
// closes it. Write, stat and close failures are joined.
func /*@seal*/Seal(f *os.File, trailer []byte) error {
	_, werr := f./*@sealwrite*/Write(trailer)
	_, serr := f.Stat()
	return errors.Join(werr, serr, f.Close())
}
