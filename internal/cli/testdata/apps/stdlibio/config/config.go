// Package config reads the importer's JSON configuration and parses the
// scalar settings (numbers, flags, dates, URLs) it contains.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

// ErrNoEndpoint is returned when the configuration names no upload endpoint.
var ErrNoEndpoint = errors.New("config: no endpoint")

// Config is the importer configuration file.
type Config struct {
	Port     string `json:"port"`
	Limit    string `json:"limit"`
	Strict   string `json:"strict"`
	Since    string `json:"since"`
	Endpoint string `json:"endpoint"`
}

// Port parses the port setting; the *strconv.NumError is wrapped with %w.
func /*@port*/Port(s string) (int, error) {
	n, err := /*@atoi*/strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("config: port %q: %w", s, err)
	}
	return n, nil
}

// Limit parses the row limit. An out-of-range value is clamped instead of
// being reported, so strconv.ErrRange never leaves this function.
func /*@limit*/Limit(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 32)
	if errors.Is(err, strconv.ErrRange) {
		return 1 << 30, nil
	}
	return n, err
}

// Explain returns the reason inside a failed port conversion.
func /*@explain*/Explain(s string) error {
	_, err := strconv.Atoi(s)
	switch e := err.(type) {
	case nil:
		return nil
	case *strconv.NumError:
		return e.Err
	default:
		return err
	}
}

// Strict parses the strict flag; an empty value means false.
func /*@strict*/Strict(s string) (bool, error) {
	if s == "" {
		return false, nil
	}
	return strconv./*@parsebool*/ParseBool(s)
}

// Since parses the "since" date.
func /*@since*/Since(s string) (time.Time, error) {
	t, err := time./*@timeparse*/Parse(time.DateOnly, s)
	if err != nil {
		var pe *time.ParseError
		if errors.As(err, &pe) {
			return time.Time{}, fmt.Errorf("config: since: bad %s in %q", pe.LayoutElem, pe.Value)
		}
		return time.Time{}, err
	}
	return t, nil
}

// Endpoint parses the upload endpoint.
func /*@endpoint*/Endpoint(s string) (*url.URL, error) {
	if s == "" {
		return nil, ErrNoEndpoint
	}
	u, err := url./*@urlparse*/Parse(s)
	if err != nil {
		return nil, fmt.Errorf("config: endpoint: %w", err)
	}
	return u, nil
}

// Decode parses the raw configuration document.
func /*@decode*/Decode(data []byte) (*Config, error) {
	var c Config
	if err := json./*@unmarshal*/Unmarshal(data, &c); err != nil {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return nil, fmt.Errorf("config: syntax error at byte %d: %w", se.Offset, err)
		}
		return nil, errors.New("config: malformed document")
	}
	return &c, nil
}

// ReadRaw reads the configuration file. A missing file is not an error:
// the built-in defaults are returned instead.
func /*@readraw*/ReadRaw(path string) ([]byte, error) {
	data, err := os./*@readfile*/ReadFile(path)
	if os.IsNotExist(err) {
		return []byte(`{"port":"8080"}`), nil
	}
	return data, err
}

// Load reads and decodes the configuration file.
func /*@load*/Load(path string) (*Config, error) {
	data, err := ReadRaw(path)
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

// Describe renders a one-line summary; it cannot fail.
func /*@describe*/Describe(c *Config) string {
	return fmt./*@sprintf*/Sprintf("port=%s limit=%s", c.Port, c.Limit)
}
