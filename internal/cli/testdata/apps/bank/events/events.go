// Package events is a tiny synchronous event bus.
package events

import (
	"errors"
	"fmt"
	"log"

	"example.com/bank/domain"
)

// Event is published after something happened to a transfer.
type Event struct {
	Kind     string
	Transfer domain.Transfer
}

// Handler reacts to an event.
type Handler func(Event) error

// Bus dispatches events to the handlers subscribed to their kind.
type Bus struct{ handlers map[string][]Handler }

// NewBus returns an empty bus.
func NewBus() *Bus { return &Bus{handlers: map[string][]Handler{}} }

// Subscribe registers h for kind.
func (b *Bus) /*@subscribe*/Subscribe(kind string, h Handler) {
	b.handlers[kind] = append(b.handlers[kind], h)
}

// Publish runs every handler and joins their failures.
func (b *Bus) /*@publish*/Publish(ev Event) error {
	var all error
	for i, h := range b.handlers[ev.Kind] {
		if err := h(ev); err != nil {
			all = errors.Join(all, fmt.Errorf("events: %s handler %d: %w", ev.Kind, i, err))
		}
	}
	return all
}

// PublishFirst stops at the first failing handler.
func (b *Bus) /*@first*/PublishFirst(ev Event) error {
	for _, h := range b.handlers[ev.Kind] {
		if err := /*@first_call*/h(ev); err != nil {
			return err
		}
	}
	return nil
}

// Notify is best effort: handler failures are logged and dropped.
func (b *Bus) /*@notify*/Notify(ev Event) error {
	if err := b.Publish(ev); err != nil {
		log.Printf("events: dropped: %v", err)
	}
	return nil
}
