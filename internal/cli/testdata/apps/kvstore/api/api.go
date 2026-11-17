// Package api turns textual commands into service calls.
package api

import (
	"errors"
	"fmt"

	"example.com/kvstore/kverr"
	"example.com/kvstore/service"
	"example.com/kvstore/validate"
)

// Backend is what the handlers need from the service layer.
type Backend interface {
	/*@iface_get*/Get(key string) (string, error)
	Put(key, val string) error
	Delete(key string, n int) error
	List(prefix string) ([]string, error)
}

var _ Backend = (*service.Service)(nil)

// Handler dispatches commands.
type Handler struct {
	backend Backend
	svc     *service.Service
}

// New returns a handler over svc.
func New(svc *service.Service) *Handler {
	return &Handler{backend: svc, svc: svc}
}

// Handle runs one command and returns its output.
func (h *Handler) /*@handle*/Handle(cmd string, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("api: %s needs a key", cmd)
	}
	key := validate./*@clean_call*/Clean(args[0])
	switch cmd {
	case "get":
		return h./*@hget_call*/get(key)
	case "put":
		if len(args) < 2 {
			return "", kverr.FieldError{Field: "value", Msg: "missing"}
		}
		return "ok", h.svc.Put(key, args[1])
	case "del":
		return "ok", h.svc./*@delete_call*/Delete(key, 2)
	case "health":
		return "ok", h.svc.Health(key)
	case "audit":
		return "ok", h.svc.Audit(key)
	case "verify":
		return "ok", h.svc.Verify(key)
	case "move":
		if len(args) < 2 {
			return "", kverr.FieldError{Field: "dst", Msg: "missing"}
		}
		if err := h.svc.Move(key, args[1]); err != nil {
			return "", err
		}
		return "ok", h.svc.Copy(key, args[1])
	case "swap":
		if len(args) < 2 {
			return "", kverr.FieldError{Field: "other", Msg: "missing"}
		}
		return "ok", h.svc.Exchange(key, args[1])
	case "find":
		return h.find(key)
	case "has":
		return "ok", h.svc.Has(key)
	case "fsck":
		return "ok", h.svc.Fsck(key)
	case "stats":
		n, err := h.svc.Stats()
		return fmt.Sprint(n), err
	case "probe":
		return "ok", h.svc.Probe(key)
	case "sync":
		return "ok", h.svc.Sync()
	}
	return "", fmt.Errorf("api: unknown command %q", cmd)
}

// get hides "not found" behind an empty value.
func (h *Handler) /*@hget*/get(key string) (string, error) {
	v, err := h.backend./*@backend_get*/Get(key)
	if errors.Is(err, kverr.ErrNotFound) {
		return "", nil
	}
	return v, err
}

// find maps the shared not-found sentinel to a placeholder value.
func (h *Handler) /*@hfind*/find(key string) (string, error) {
	v, err := h.svc.Find(key)
	if err == kverr.ErrNotFound {
		return "(none)", nil
	}
	return v, err
}

// List prints the keys with a prefix.
func (h *Handler) /*@list*/List(prefix string) (out string, err error) {
	keys, err := h.backend.List(prefix)
	for _, k := range keys {
		out += k + "\n"
	}
	return out, err
}

// Shutdown closes the service.
func (h *Handler) /*@shutdown*/Shutdown() error {
	return h.svc./*@close_call*/Close()
}
