// Command ingest reads records from the disk store and normalises them.
package main

import (
	"errors"
	"fmt"
	"os"

	"example.com/targets/internal/codec"
	"example.com/targets/internal/store"
	"example.com/targets/internal/store/disk"
)

var errUsage = errors.New("usage: ingest <key>...")

// kind classifies a record by its size.
type kind int

// hook runs before each record.
var hook = func(key string) error {
	if key == "" {
		return errUsage
	}
	return nil
}

func /*@init*/init() {
	if err := /*@init_call*/codec.Register("text"); err != nil {
		panic(err)
	}
}

func /*@main*/main() {
	if err := /*@main_call*/run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func /*@run*/run(args []string) error {
	if /*@builtin*/len(args) == 0 {
		return errUsage
	}
	d, err := disk.Open("data")
	if err != nil {
		return err
	}
	s := store.New(d)
	defer s./*@defer_call*/Close()
	go /*@go_call*/warm(s)
	for _, key := range args {
		if err := ingest(s, key); err != nil {
			return err
		}
	}
	return /*@chain_pkg*/store./*@chain_new*/NewBatch(s)./*@chain_add*/Add("last", nil)./*@chain_commit*/Commit()
}

// warm primes the store; a missing warm-up record is fine.
func warm(s *store.Store) error {
	ping := s./*@mval*/Ping
	if err := /*@bound*/ping(); err != nil {
		return err
	}
	_, err := s.Get("warm")
	if errors.Is(err, disk.ErrNoEntry) {
		return nil
	}
	return /*@warm_ret*/err
}

func ingest(s *store.Store, key string) error {
	if err := /*@fnvar*/hook(key); err != nil {
		return err
	}
	raw, err := fetch(s, key)
	if err != nil {
		return err
	}
	k := /*@conv*/kind(len(raw))
	text, err := codec./*@decode_call*/Decode/*@decode_bracket*/[/*@decode_targ*/string](raw, toText)
	if err != nil {
		return err
	}
	err = /*@iife_kw*/func() error {
		if k > 1 && text == "" {
			return errUsage
		}
		return nil
	}/*@iife_paren*/()
	if err != nil {
		return err
	}
	if s./*@hits_call*/Hits() > 1000 {
		return s./*@promoted*/Check()
	}
	return /*@outer*/annotate(key, /*@inner*/s.Ping())
}

// fetch reads one record and says which key failed.
func /*@fetch*/fetch(s *store.Store, key string) ([]byte, error) {
	raw, err := /*@sel_x*/s./*@sel_sel*/Get/*@lparen*/(/*@arg_ident*/key/*@rparen*/)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", key, err)
	}
	return raw, nil
}

func toText(raw []byte) (string, error) {
	if len(raw) > 0 && raw[0] == 0 {
		return "", codec.ErrSyntax
	}
	return string(raw), nil
}

func annotate(key string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", key, err)
}
