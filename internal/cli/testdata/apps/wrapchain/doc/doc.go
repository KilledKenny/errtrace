// Package doc holds the document model and the pipeline's sentinel errors.
package doc

import "errors"

// Document is one parsed input file.
type Document struct {
	Path    string
	Version int
	Title   string
	Lines   []string
	Fields  map[string]string
}

// Sentinels shared by every stage of the pipeline.
var (
	ErrEmpty   = errors.New("empty")
	ErrSyntax  = errors.New("syntax error")
	ErrTooLong = errors.New("too long")
	ErrNoTitle = errors.New("missing title")
	ErrCycle   = errors.New("include cycle")
	ErrDisk    = errors.New("disk failure")
	ErrClosed  = errors.New("sink closed")
	ErrQuota   = errors.New("quota exceeded")
	ErrNever   = errors.New("never produced")
)

// MaxLine is the longest accepted line.
const /*@maxline*/MaxLine = 80
