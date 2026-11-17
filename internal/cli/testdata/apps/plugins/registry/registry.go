// Package registry maps names to handlers.
package registry

import (
	"errors"
	"fmt"
	"sort"

	"example.com/plugins/core"
)

var (
	ErrAuditFull   = errors.New("audit log full")
	ErrMetricsDown = errors.New("metrics backend down")
	ErrOrphan      = errors.New("orphan handler")
)

type auditHandler struct{ max int }

func (a auditHandler) Handle(e core.Event) error {
	if e.Priority > a.max {
		return ErrAuditFull
	}
	return nil
}

func (auditHandler) Name() string { return "audit" }

type metricsHandler struct{ seen int }

func (m *metricsHandler) Handle(e core.Event) error {
	m.seen++
	if m.seen > 1000 {
		return ErrMetricsDown
	}
	return nil
}

// orphanHandler is never registered.
type orphanHandler struct{}

func (orphanHandler) Handle(e core.Event) error { return ErrOrphan }
func (orphanHandler) Name() string              { return "orphan" }

// handlers starts with the audit handler; init adds metrics and main
// registers the rest.
var handlers = map[string]core.Handler{
	"audit": auditHandler{max: 8},
}

func init() {
	Register("metrics", &metricsHandler{})
}

// Register adds or replaces a handler.
func Register(name string, h core.Handler) { handlers[name] = h }

// Lookup finds a handler by name.
func /*@lookup*/Lookup(name string) (core.Handler, error) {
	h, ok := handlers[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", core.ErrNoRoute, name)
	}
	return h, nil
}

// Dispatch sends e to the named handler.
func /*@dispatch*/Dispatch(name string, e core.Event) error {
	h, err := Lookup(name)
	if err != nil {
		return err
	}
	return h./*@dispatch_handle*/Handle(e)
}

// Each calls fn for every handler in name order.
func /*@each*/Each(fn func(name string, h core.Handler) error) error {
	names := make([]string, 0, len(handlers))
	for name := range handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := fn(name, handlers[name]); err != nil {
			return fmt.Errorf("handler %s: %w", name, err)
		}
	}
	return nil
}

// SelfTest sends e to every handler.
func /*@selftest*/SelfTest(e core.Event) error {
	return Each(func(_ string, h core.Handler) error { return h.Handle(e) })
}

type byName []core.Plugin

func (p byName) Len() int           { return len(p) }
func (p byName) Less(i, j int) bool { return p[i].Name() < p[j].Name() }
func (p byName) Swap(i, j int)      { p[i], p[j] = p[j], p[i] }

// Plugins returns the handlers that are also Named, sorted by name.
func Plugins() []core.Plugin {
	var ps []core.Plugin
	for _, h := range handlers {
		if p, ok := h.(core.Plugin); ok {
			ps = append(ps, p)
		}
	}
	sort.Sort(byName(ps))
	return ps
}

// RunPlugins sends e to every plugin.
func /*@runplugins*/RunPlugins(e core.Event) error {
	for _, p := range Plugins() {
		if err := p.Handle(e); err != nil {
			return fmt.Errorf("plugin %s: %w", p./*@name_call*/Name(), err)
		}
	}
	return nil
}

// MustLookup is Lookup for names known to exist.
func MustLookup(name string) core.Handler {
	h, err := Lookup(name)
	if err != nil {
		panic(err)
	}
	return h
}
