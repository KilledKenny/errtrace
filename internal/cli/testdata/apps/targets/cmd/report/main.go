// Command report prints totals from an in-memory snapshot.
package main

import (
	"fmt"
	"os"

	"example.com/targets/internal/codec"
	"example.com/targets/internal/platform"
	"example.com/targets/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func /*@run*/run() error {
	if err := platform./*@probe_call*/Probe(); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	if err := codec.Format("text"); err != nil {
		return err
	}
	s := store.New(store.NewMem(map[string][]byte{"total": []byte(" 42 ")}))
	defer s.Close()
	raw, err := s./*@get_call*/Get("total")
	if err != nil {
		return err
	}
	if raw, err = codec./*@trim_call*/Trim(raw); err != nil {
		return err
	}
	n, err := codec./*@decode_inferred*/Decode(raw, codec.ParseInt)
	if err != nil {
		return err
	}
	sum, err := codec.Checksum(raw)
	if err != nil {
		return err
	}
	if err := codec.Länge(raw); err != nil {
		return err
	}
	fmt.Println(n, sum)
	return nil
}
