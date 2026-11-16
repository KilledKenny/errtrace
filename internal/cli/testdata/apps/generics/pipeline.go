package main

import (
	"errors"
	"fmt"
	"strconv"

	"example.com/generics/errs"
	"example.com/generics/seq"
	"example.com/generics/validate"
)

var errOdd = errors.New("odd")

// parsePort parses a decimal port number.
func parsePort(s string) (int, error) {
	if s == "" {
		return 0, errs.ErrEmpty
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, &errs.TypedErr[string]{Op: "port", Val: s}
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// parsePorts passes a named function as the callback.
func parsePorts(src []string) ([]int, error) {
	return seq./*@mapport*/Map(src, parsePort)
}

// lengths passes a closure that wraps the constraint method's error.
func lengths(names []validate.Name) ([]int, error) {
	return seq./*@maplen*/Map(names, func(n validate.Name) (int, error) {
		if err := n.Validate(); err != nil {
			return 0, fmt.Errorf("lengths: %w", err)
		}
		return len(n), nil
	})
}

// firstEven nests one generic call in another: Filter's results feed First.
func firstEven(nums []int) (int, error) {
	return seq./*@nested*/First(seq.Filter(nums, func(n int) (bool, error) {
		if n < 0 {
			return false, errs.ErrNegative
		}
		return n%2 == 0, nil
	}))
}

// stringify calls Map[int, string] through a generic function value.
func stringify(nums []int) ([]string, error) {
	conv := seq.Map[int, string]
	return /*@funcval*/conv(nums, func(n int) (string, error) {
		if n < 0 {
			return "", &errs.TypedErr[int]{Op: "stringify", Val: n}
		}
		return strconv.Itoa(n), nil
	})
}

// labelOdd is a second user of Map[int, string], with another callback.
func labelOdd(nums []int) ([]string, error) {
	return seq.Map(nums, func(n int) (string, error) {
		if n%2 == 1 {
			return "", errOdd
		}
		return "even", nil
	})
}

// total folds with Reduce; the callback checks a type-set constraint.
func /*@total*/total(nums []int) (int, error) {
	return seq.Reduce(nums, 0, func(acc, n int) (int, error) {
		if err := validate.Positive(n); err != nil {
			return acc, fmt.Errorf("total: %w", err)
		}
		return acc + n, nil
	})
}

// validateNames passes an explicit generic instantiation as the callback.
func validateNames(names []validate.Name) error {
	return seq./*@eachname*/Each(names, validate.Check[validate.Name])
}

// validateIDs passes an inferred generic instantiation as the callback.
func validateIDs(ids []validate.UserID) error {
	return seq./*@eachid*/Each(ids, validate.Check)
}

// collectPorts drains a range-over-func iterator through Collect.
func /*@collectports*/collectPorts(src []string) ([]int, error) {
	return seq.Collect(seq.Parse(src, parsePort))
}

// firstBad ranges over the iterator directly.
func /*@firstbad*/firstBad(src []string) error {
	for _, err := range seq.Parse(src, parsePort) {
		if err != nil {
			return err
		}
	}
	return nil
}

// walkNames walks a generic tree (generic mutual recursion).
func walkNames(root *seq.Node[validate.Name]) error {
	return seq./*@walk*/Walk(root, func(n validate.Name) error { return n.Validate() })
}
