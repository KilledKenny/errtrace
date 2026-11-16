// Package lintcfg is a nested module: it is not part of example.com/targets.
package lintcfg

import "errors"

// ErrNoConfig is returned when no configuration file exists.
var ErrNoConfig = errors.New("lintcfg: no config")

// Load reads the lint configuration.
func /*@load*/Load(path string) error {
	if path == "" {
		return ErrNoConfig
	}
	return nil
}
