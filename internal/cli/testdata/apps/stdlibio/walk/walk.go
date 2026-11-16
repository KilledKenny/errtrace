// Package walk discovers the data files below the input directory.
package walk

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
)

// ErrTooMany stops a walk that found more files than allowed.
var ErrTooMany = errors.New("walk: too many files")

// ErrNoRoot is returned by Root when no input directory was configured.
var ErrNoRoot = errors.New("walk: no input directory")

// Find lists the .csv files below root. Hidden directories are skipped with
// fs.SkipDir, which WalkDir consumes; ErrTooMany comes back out of WalkDir.
func /*@find*/Find(root string, max int) ([]string, error) {
	var files []string
	err := filepath./*@walkdir*/WalkDir(root, /*@findfn*/func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		if filepath.Ext(path) == ".csv" {
			files = append(files, path)
		}
		if len(files) > max {
			return ErrTooMany
		}
		return nil
	})
	return files, err
}

// Has reports whether a file named name exists below root. The callback
// only ever returns fs.SkipAll, so the walk's own failures are all that is
// left.
func /*@has*/Has(root, name string) (bool, error) {
	found := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if d != nil && d.Name() == name {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}

var (
	rootOnce sync.Once
	rootDir  string
	rootErr  error
)

// Root resolves the input directory once; the error is kept in a package
// variable assigned at run time by the sync.Once callback.
func /*@root*/Root(configured string) (string, error) {
	rootOnce.Do(func() {
		if configured == "" {
			rootErr = ErrNoRoot
			return
		}
		rootDir, rootErr = filepath.Abs(configured)
	})
	return rootDir, rootErr
}

// Pattern compiles the include pattern once per process.
var Pattern = sync.OnceValues(func() (string, error) {
	if _, err := filepath.Match("[", ""); err != nil {
		return "", fmt.Errorf("walk: self-test: %w", err)
	}
	return "*.csv", nil
})

// Include returns the include pattern.
func /*@include*/Include() (string, error) {
	return /*@patterncall*/Pattern()
}
