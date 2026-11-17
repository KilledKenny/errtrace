package seq

import "fmt"

// Node is a generic tree node.
type Node[T any] struct {
	Val  T
	Kids []*Node[T]
}

// Walk visits n and its descendants depth first (generic mutual recursion
// with walkKids).
func /*@walk*/Walk[T any](n *Node[T], visit func(T) error) error {
	if n == nil {
		return nil
	}
	if err := visit(n.Val); err != nil {
		return err
	}
	return walkKids(n.Kids, visit)
}

func walkKids[T any](kids []*Node[T], visit func(T) error) error {
	for i, k := range kids {
		if err := Walk(k, visit); err != nil {
			return fmt.Errorf("kid %d: %w", i, err)
		}
	}
	return nil
}
