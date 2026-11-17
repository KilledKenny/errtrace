// Package config parses the command line into a Config.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrNoCommand is returned when no command is given.
var ErrNoCommand = errors.New("config: no command")

// Config is the parsed command line.
type Config struct {
	Limit    int
	ReadOnly bool
	Command  string
	Args     []string
}

// Load parses args.
func /*@load*/Load(args []string) (cfg *Config, err error) {
	cfg = &Config{Limit: 16}
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		switch name, val, _ := strings.Cut(args[0], "="); name {
		case "-limit":
			if cfg.Limit, err = /*@parselimit_call*/parseLimit(val); err != nil {
				return nil, err
			}
		case "-ro":
			cfg.ReadOnly = true
		default:
			return nil, fmt.Errorf("config: unknown flag %s", name)
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return nil, ErrNoCommand
	}
	cfg.Command, cfg.Args = args[0], args[1:]
	return cfg, nil
}

// parseLimit hides the strconv error on purpose: %v, not %w.
func /*@parselimit*/parseLimit(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("config: bad limit %q: %v", s, err)
	}
	if n == 0 {
		return 0, errors.New("config: limit must not be zero")
	}
	return n, nil
}
