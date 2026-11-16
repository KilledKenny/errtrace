// Package audit writes one line per business event to an io.Writer.
package audit

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"example.com/bank/domain"
)

var (
	// ErrClosed is returned by Record after Close.
	ErrClosed = errors.New("audit: log closed")
	// ErrFull is returned by a Quota writer that ran out of room.
	ErrFull = errors.New("audit: quota exhausted")
)

// Entry is one audit line.
type Entry struct {
	At       time.Time
	Kind     string
	Transfer domain.TransferID
	Outcome  string
}

// Logger records entries.
type Logger struct {
	w      io.Writer
	c      io.Closer
	closed bool
}

// New logs to w.
func New(w io.Writer) *Logger { return &Logger{w: w} }

// Open appends to the file at path.
func /*@open*/Open(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}
	return &Logger{w: f, c: f}, nil
}

// Record logs the outcome of one operation on a transfer.
func (l *Logger) /*@record*/Record(kind string, t domain.Transfer, outcome error) error {
	if l.closed {
		return ErrClosed
	}
	e := Entry{At: time.Now(), Kind: kind, Transfer: t.ID, Outcome: "ok"}
	if outcome != nil {
		e.Outcome = outcome.Error()
	}
	return Write(l.w, e)
}

// Write formats e as one line on w.
func /*@write*/Write(w io.Writer, e Entry) error {
	line := fmt.Sprintf("%s %s %d %q\n", e.At.Format(time.RFC3339), e.Kind, e.Transfer, e.Outcome)
	n, err := w./*@w_write_call*/Write([]byte(line))
	if err != nil {
		return fmt.Errorf("audit: write: %w", err)
	}
	if n < len(line) {
		return fmt.Errorf("audit: write: %w", io.ErrShortWrite)
	}
	return nil
}

// Close closes the underlying file, if the logger owns one.
func (l *Logger) /*@close*/Close() error {
	if l.closed {
		return ErrClosed
	}
	l.closed = true
	if l.c == nil {
		return nil
	}
	return l.c.Close()
}

// Quota is a writer that accepts at most Left more bytes.
type Quota struct {
	W    io.Writer
	Left int
}

func (q *Quota) /*@quota_write*/Write(p []byte) (int, error) {
	if len(p) > q.Left {
		return 0, ErrFull
	}
	q.Left -= len(p)
	return q.W.Write(p)
}
