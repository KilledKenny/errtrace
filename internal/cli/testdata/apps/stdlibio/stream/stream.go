// Package stream holds the importer's own io.Reader and io.Writer
// implementations and the copy loop that connects them.
package stream

import (
	"errors"
	"io"
)

// ErrQuota is returned by Limited once the byte quota is used up.
var ErrQuota = errors.New("stream: quota exceeded")

// ErrSinkFull is returned by Sink when its capacity is exhausted.
var ErrSinkFull = errors.New("stream: sink full")

// Limited is a reader that fails with ErrQuota after N bytes.
type Limited struct {
	R io.Reader
	N int
}

func (l *Limited) /*@limread*/Read(p []byte) (int, error) {
	if l.N <= 0 {
		return 0, ErrQuota
	}
	if len(p) > l.N {
		p = p[:l.N]
	}
	n, err := l.R./*@innerread*/Read(p)
	l.N -= n
	return n, err
}

// Chunks is a reader over fixed chunks; it ends with io.EOF.
type Chunks struct {
	Data [][]byte
}

func (c *Chunks) Read(p []byte) (int, error) {
	if len(c.Data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.Data[0])
	c.Data = c.Data[1:]
	return n, nil
}

// Sink is a bounded in-memory writer.
type Sink struct {
	Buf []byte
	Cap int
}

func (s *Sink) Write(p []byte) (int, error) {
	if len(s.Buf)+len(p) > s.Cap {
		return 0, ErrSinkFull
	}
	s.Buf = append(s.Buf, p...)
	return len(p), nil
}

// Pump copies src to dst with io.Copy.
func /*@pump*/Pump(dst io.Writer, src io.Reader) (int64, error) {
	return io./*@copy*/Copy(dst, src)
}

// Transfer pumps a quota-limited chunk reader into a sink.
func /*@transfer*/Transfer(chunks [][]byte, quota, capacity int) ([]byte, error) {
	sink := &Sink{Cap: capacity}
	src := &Limited{R: &Chunks{Data: chunks}, N: quota}
	if _, err := /*@pumpcall*/Pump(sink, src); err != nil {
		return nil, err
	}
	return sink.Buf, nil
}
