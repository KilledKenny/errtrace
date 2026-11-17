// Command kvstore is a toy key-value store with a layered architecture.
package main

import (
	"fmt"
	"os"

	"example.com/kvstore/api"
	"example.com/kvstore/config"
	"example.com/kvstore/engine"
	"example.com/kvstore/repo"
	"example.com/kvstore/service"
	"example.com/kvstore/validate"
)

func main() {
	if err := /*@run_call*/run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "kvstore:", err)
		os./*@exit_call*/Exit(1)
	}
}

func /*@run*/run(args []string) error {
	cfg, err := config.Load(args)
	if err != nil {
		return err
	}
	eng, err := engine./*@open_call*/Open(cfg.Limit)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer eng./*@eng_close_call*/Close()
	if cfg.Command == "check" {
		kerr, verr := validate./*@pair_call*/Pair(cfg.Args[0], cfg.Args[1])
		if kerr != nil {
			return kerr
		}
		return verr
	}
	h := api.New(service.New(repo.New(eng, "inv"), cfg.ReadOnly))
	defer h.Shutdown()
	if cfg.Command == "list" {
		out, err := h.List("")
		fmt.Print(out)
		return err
	}
	out, err := h./*@handle_call*/Handle(cfg.Command, cfg.Args)
	if err != nil {
		if lerr := eng.LastErr(); lerr != nil {
			return lerr
		}
		return err
	}
	fmt.Println(out)
	return nil
}
