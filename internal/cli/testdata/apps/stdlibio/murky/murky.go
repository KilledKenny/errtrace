// Package murky holds the importer's plumbing that moves errors through
// places a static analysis cannot follow precisely: channels, fields, maps,
// slices, run-time globals, panics, reflection, goroutines, atomics, unsafe
// and empty-interface round trips.
package murky

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"unsafe"
)

// ErrWorker is what the background worker reports.
var ErrWorker = errors.New("murky: worker failed")

// ErrStage is recorded by a failed pipeline stage.
var ErrStage = errors.New("murky: stage failed")

// ErrPlugin is returned by the reflectively invoked plugin.
var ErrPlugin = errors.New("murky: plugin failed")

// lastErr is a package-level error variable that is NOT a sentinel: it is
// assigned at run time by Remember.
var lastErr error

// Remember stores err as the most recent failure.
func Remember(err error) { lastErr = err }

// Last returns the most recent failure.
func /*@last*/Last() error {
	return lastErr
}

// FromChannel receives the worker's result from a channel.
func /*@fromchannel*/FromChannel() error {
	errc := make(chan error, 1)
	go func() { errc <- ErrWorker }()
	return <-errc
}

// Stage is one pipeline stage; its failure is kept in a field.
type Stage struct {
	name string
	err  error
}

// Fail records the stage's failure.
func (s *Stage) Fail() { s.err = fmt.Errorf("stage %s: %w", s.name, ErrStage) }

// Err returns the recorded failure.
func (s *Stage) /*@stageerr*/Err() error {
	return s.err
}

// FromMap looks a failure up by stage name.
func /*@frommap*/FromMap(name string) error {
	failures := map[string]error{"parse": ErrStage}
	return failures[name]
}

// FromSlice returns the first recorded failure.
func /*@fromslice*/FromSlice(stages []*Stage) error {
	var errs []error
	for _, s := range stages {
		if s.err != nil {
			errs = append(errs, s.err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs[0]
}

// Guarded runs fn and converts a panic into an error.
func /*@guarded*/Guarded(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
				return
			}
			err = fmt.Errorf("murky: panic: %v", r)
		}
	}()
	fn()
	return nil
}

// MustPositive panics with ErrStage for a non-positive count.
func MustPositive(n int) {
	if n <= 0 {
		/*@panic*/panic(ErrStage)
	}
}

func plugin() error { return ErrPlugin }

// Reflective invokes the plugin through reflection.
func /*@reflective*/Reflective() error {
	out := reflect.ValueOf(plugin).Call(nil)
	if err, ok := out[0].Interface().(error); ok {
		return err
	}
	return nil
}

// Group is a minimal errgroup: it keeps the first error of its goroutines.
type Group struct {
	wg   sync.WaitGroup
	once sync.Once
	err  error
}

// Go runs fn in a goroutine.
func (g *Group) Go(fn func() error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if err := fn(); err != nil {
			g.once.Do(func() { g.err = err })
		}
	}()
}

// Wait returns the first error.
func (g *Group) /*@groupwait*/Wait() error {
	g.wg.Wait()
	return g.err
}

// Parallel runs two workers in a Group.
func /*@parallel*/Parallel() error {
	var g Group
	g.Go(func() error { return ErrWorker })
	g.Go(func() error { return nil })
	return g.Wait()
}

var health atomic.Value

// SetHealth publishes the health status.
func SetHealth(err error) { health.Store(&err) }

// Health loads the published status from the atomic.Value.
func /*@health*/Health() error {
	p, ok := health.Load().(*error)
	if !ok {
		return nil
	}
	return *p
}

// Reinterpret reads an error back through an unsafe.Pointer.
func /*@reinterpret*/Reinterpret() error {
	err := ErrStage
	p := unsafe.Pointer(&err)
	return *(*error)(p)
}

// RoundTrip boxes an error in an empty interface and asserts it back.
func /*@roundtrip*/RoundTrip() error {
	var boxed any = ErrPlugin
	return boxed.(error)
}

// Boxed is like RoundTrip but the boxed value crosses a function boundary.
func /*@boxed*/Boxed() error {
	v := box(fmt.Errorf("murky: boxed: %w", ErrWorker))
	if err, ok := v.(error); ok {
		return err
	}
	return nil
}

func box(v any) any { return v }
