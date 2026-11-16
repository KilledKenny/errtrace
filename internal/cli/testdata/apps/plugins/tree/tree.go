// Package tree composes handlers into groups (composite pattern).
package tree

import (
	"fmt"

	"example.com/plugins/core"
)

// Leaf adapts a Driver to a Handler.
type Leaf struct{ Driver core.Driver }

func (l Leaf) /*@leaf_handle*/Handle(e core.Event) error { return l.Driver.Send(e) }

// Group is a Handler made of Handlers, some of which are Groups.
type Group struct {
	Label   string
	Members []core.Handler
}

// Add appends a member and returns g.
func (g *Group) Add(h core.Handler) *Group {
	g.Members = append(g.Members, h)
	return g
}

// Handle stops at the first failing member.
func (g *Group) /*@group_handle*/Handle(e core.Event) error {
	for _, m := range g.Members {
		if err := m.Handle(e); err != nil {
			return fmt.Errorf("%s: %w", g.Label, err)
		}
	}
	return nil
}
