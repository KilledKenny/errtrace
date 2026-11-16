// Package export writes documents to sinks chosen at run time.
package export

import (
	"errors"
	"fmt"

	"example.com/wrapchain/doc"
	"example.com/wrapchain/xerr"
)

// Sink is a destination for documents.
type Sink interface {
	Write(d *doc.Document) error
	Close() error
}

// FileSink writes to a directory.
type FileSink struct{ Dir string }

func (s *FileSink) Write(d *doc.Document) error {
	if s.Dir == "" {
		return doc.ErrClosed
	}
	if len(d.Lines) > 1000 {
		return &xerr.OpError{Op: "write " + s.Dir, Err: doc.ErrDisk}
	}
	return nil
}
func (s *FileSink) Close() error { return doc.ErrClosed }

// NetSink uploads to a remote service.
type NetSink struct{ URL string }

func (s NetSink) Write(d *doc.Document) error {
	if s.URL == "" {
		return &xerr.CodeError{Code: 502}
	}
	if len(d.Lines) > 100 {
		return fmt.Errorf("upload %s: %w", s.URL, doc.ErrQuota)
	}
	return nil
}
func (s NetSink) Close() error { return nil }

// NullSink is never instantiated by the program.
type NullSink struct{}

func (NullSink) Write(*doc.Document) error { return doc.ErrNever }
func (NullSink) Close() error              { return doc.ErrNever }

// Export wraps whatever the sink returns.
func /*@export*/Export(s Sink, d *doc.Document) error {
	if err := s./*@write*/Write(d); err != nil {
		return fmt.Errorf("export %s: %w", d.Path, err)
	}
	return nil
}

// Flush writes and joins the close error in a deferred closure.
func /*@flush*/Flush(s Sink, d *doc.Document) (err error) {
	defer func() {
		err = errors.Join(err, s.Close())
	}()
	return s.Write(d)
}

// Hook is a user callback run after export.
type Hook func(d *doc.Document) error

// RunHook wraps the result of a function value.
func /*@runhook*/RunHook(h Hook, d *doc.Document) error {
	return xerr.WithOp("hook", h(d))
}

// Audit is the hook installed by main.
func Audit(d *doc.Document) error {
	if d.Title == "" {
		return fmt.Errorf("audit: %w", doc.ErrNoTitle)
	}
	return nil
}
