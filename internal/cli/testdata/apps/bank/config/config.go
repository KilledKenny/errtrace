// Package config loads the service configuration from a key=value file.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"example.com/bank/domain"
)

// ErrNoToken is returned when the API token is empty.
var ErrNoToken = errors.New("config: token must be set")

// Config is the runtime configuration.
type Config struct {
	DataDir     string
	AuditPath   string
	Token       string
	PerTransfer int64
	Daily       int64
	Retries     int
}

// Default returns the built-in configuration.
func Default() *Config {
	return &Config{Token: "dev", PerTransfer: 1000, Daily: 5000, Retries: 3}
}

// Load reads path. A missing file is not an error: the defaults apply.
func /*@load*/Load(path string) (*Config, error) {
	f, err := os./*@open_call*/Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	defer f.Close()
	cfg := Default()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		if err := cfg./*@set_call*/set(sc.Text()); err != nil {
			return nil, fmt.Errorf("config: %s:%d: %w", path, n, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("config: read %s: %v", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) /*@set*/set(line string) error {
	key, val, ok := strings.Cut(line, "=")
	if !ok {
		return errors.New("missing '='")
	}
	var err error
	switch strings.TrimSpace(key) {
	case "data_dir":
		c.DataDir = val
	case "audit":
		c.AuditPath = val
	case "token":
		c.Token = val
	case "per_transfer":
		c.PerTransfer, err = strconv.ParseInt(val, 10, 64)
	case "daily":
		c.Daily, err = strconv.ParseInt(val, 10, 64)
	case "retries":
		c.Retries, err = strconv.Atoi(val)
	default:
		return fmt.Errorf("unknown key %q", key)
	}
	return err
}

// validate reports every problem at once.
func (c *Config) /*@validate*/validate() error {
	var tokenErr, limitErr error
	if c.Token == "" {
		tokenErr = ErrNoToken
	}
	if c.Daily < c.PerTransfer {
		limitErr = &domain.ValidationError{Field: "daily", Reason: "below per_transfer"}
	}
	return errors.Join(tokenErr, limitErr)
}
