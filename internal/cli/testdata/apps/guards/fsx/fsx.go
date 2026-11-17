// Package fsx is a stdlib-shaped fake of the receipt store's file system.
package fsx

import (
	"errors"
	"io"
	"io/fs"
)

var (
	ErrNotExist   = errors.New("file does not exist")
	ErrPermission = errors.New("permission denied")
	EOF           = errors.New("EOF")
	ErrShort      = errors.New("short read")
)

// PathError mirrors fs.PathError.
type PathError struct {
	Op, Path string
	Err      error
}

func (e *PathError) Error() string { return e.Op + " " + e.Path + ": " + e.Err.Error() }
func (e *PathError) Unwrap() error { return e.Err }

// IsNotExist mirrors os.IsNotExist.
func IsNotExist(err error) bool { return errors.Is(err, ErrNotExist) }

// Open fails with PathErrors.
func Open(name string) error {
	switch name {
	case "":
		return &PathError{Op: "open", Path: name, Err: ErrNotExist}
	case "/root":
		return &PathError{Op: "open", Path: name, Err: ErrPermission}
	}
	return nil
}

// Read fails with EOF at the end and ErrShort on a torn record.
func Read(i int) error {
	if i > 3 {
		return EOF
	}
	if i == 2 {
		return ErrShort
	}
	return nil
}

// Stat fails with the real stdlib sentinels.
func Stat(name string) error {
	switch name {
	case "":
		return &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	case "eof":
		return io.EOF
	case "torn":
		return io.ErrUnexpectedEOF
	}
	return nil
}
