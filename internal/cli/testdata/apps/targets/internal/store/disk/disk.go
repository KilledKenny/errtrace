// Package disk is the on-disk Backend of the store.
package disk

import "errors"

// ErrNoEntry is returned when a key has no file.
var ErrNoEntry = errors.New("disk: no entry")

// DiskError describes a failed disk operation.
type DiskError struct{ Op, Path string }

func (e *DiskError) Error() string { return "disk: " + e.Op + " " + e.Path }

// Disk keeps one file per key under root.
type Disk struct {
	root  string
	files map[string][]byte
}

// Validate reports why root cannot be used, as its concrete type.
func /*@validate*/Validate(root string) *DiskError {
	if root == "" {
		return &DiskError{Op: "validate", Path: root}
	}
	return nil
}

// Open opens the store rooted at root.
func /*@open*/Open(root string) (*Disk, error) {
	if e := /*@validate_call*/Validate(root); e != nil {
		return nil, e
	}
	return &Disk{root: root, files: make(map[string][]byte)}, nil
}

// Read returns the content of key.
func (d *Disk) /*@read*/Read(key string) ([]byte, error) {
	val, ok := d.files[key]
	if !ok {
		return nil, ErrNoEntry
	}
	return val, nil
}

// Write replaces the content of key.
func (d *Disk) Write(key string, val []byte) error {
	if len(val) > 1<<20 {
		return &DiskError{Op: "write", Path: d.root + "/" + key}
	}
	d.files[key] = val
	return nil
}
