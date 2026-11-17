// Command stdlibio imports CSV and JSON-lines data files as described by a
// JSON configuration file.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"example.com/stdlibio/config"
	"example.com/stdlibio/jobs"
	"example.com/stdlibio/murky"
	"example.com/stdlibio/records"
	"example.com/stdlibio/source"
	"example.com/stdlibio/stream"
	"example.com/stdlibio/walk"
)

func main() {
	if err := /*@runcall*/run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "stdlibio:", err)
		os.Exit(1)
	}
}

func /*@run*/run(args []string) error {
	if len(args) < 2 {
		return errors./*@usage*/New("usage: stdlibio <config> <dir>")
	}
	cfg, err := /*@settingscall*/settings(args[0])
	if err != nil {
		return err
	}
	fmt.Println(config.Describe(cfg))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx = jobs.WithHook(ctx, func(step string) error {
		return fmt.Errorf("hook: step %s rejected", step)
	})
	if err := jobs.Run(ctx, func(ctx context.Context) error {
		return importAll(ctx, args[1])
	}); err != nil {
		return err
	}
	return /*@diagcall*/diagnostics()
}

// settings loads the configuration and validates every scalar in it.
func /*@settings*/settings(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if _, err := config.Port(cfg.Port); err != nil {
		return nil, config.Explain(cfg.Port)
	}
	if _, err := config.Limit(cfg.Limit); err != nil {
		return nil, err
	}
	if _, err := config.Strict(cfg.Strict); err != nil {
		return nil, err
	}
	if _, err := config.Since(cfg.Since); err != nil {
		return nil, err
	}
	if _, err := config.Endpoint(cfg.Endpoint); err != nil {
		return nil, err
	}
	return cfg, nil
}

// importAll walks the input directory and imports every data file.
func importAll(ctx context.Context, dir string) error {
	root, err := walk.Root(dir)
	if err != nil {
		return err
	}
	if _, err := walk.Include(); err != nil {
		return err
	}
	if ok, err := walk.Has(root, "SKIP"); err != nil || ok {
		return err
	}
	files, err := walk.Find(root, 100)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := jobs.Check(ctx); err != nil {
			return jobs.Why(ctx)
		}
		if err := /*@importcall*/importFile(ctx, f); err != nil {
			murky.Remember(err)
			if nerr := jobs.Notify(ctx, f); nerr != nil {
				return nerr
			}
		}
	}
	if err := jobs.Wait(ctx, time.Millisecond); err != nil {
		return err
	}
	return jobs.Stop(len(files) > 0, false)
}

// importFile imports one data file and removes its lock file.
func /*@importfile*/importFile(ctx context.Context, path string) error {
	ok, err := source.Exists(path)
	if err != nil {
		return err
	}
	if !ok {
		return source.Missing(path)
	}
	if err := source.Optional(path + ".meta"); err != nil {
		return err
	}
	if _, err := source.Peek(path); err != nil {
		return err
	}
	data, err := source.Slurp(path)
	if err != nil {
		return err
	}
	doc := string(data)
	if _, err := source./*@magicmem*/Magic(strings.NewReader(doc)); err != nil {
		return err
	}
	if _, err := source.Header(doc, 8); err != nil {
		return err
	}
	if _, err := source.Lines(doc); err != nil {
		return err
	}
	if _, err := source.FirstLine(doc); err != nil && err != io.EOF {
		return err
	}
	if _, err := source.CountLines(doc); err != nil {
		return err
	}
	rows, err := records.ReadCSV(doc)
	if err != nil {
		rows, err = records.ReadJSONLines(strings.NewReader(doc))
	}
	if err != nil {
		return err
	}
	out, err := records.Encode(rows)
	if err != nil {
		return err
	}
	if _, err := stream.Transfer([][]byte{out}, 1<<20, 1<<20); err != nil {
		return err
	}
	if _, err := stream.Pump(os.Stdout, strings.NewReader(doc)); err != nil {
		return err
	}
	if err := source.Save(path+".json", out); err != nil {
		return err
	}
	if err := source.Finish(path + ".json"); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, source.Durability(os.Stdout))
	return source.Cleanup(path + ".lock")
}

// diagnostics exercises the importer's background plumbing.
func /*@diagnostics*/diagnostics() error {
	stage := &murky.Stage{}
	stage.Fail()
	murky.SetHealth(murky.ErrStage)
	checks := []func() error{
		murky.Last,
		murky.FromChannel,
		stage./*@methodvalue*/Err,
		func() error { return murky.FromMap("parse") },
		func() error { return murky.FromSlice([]*murky.Stage{stage}) },
		func() error { return murky.Guarded(func() { murky.MustPositive(0) }) },
		murky.Reflective,
		murky.Parallel,
		murky.Health,
		murky.Reinterpret,
		murky.RoundTrip,
		murky.Boxed,
	}
	for _, check := range checks {
		if err := /*@checkcall*/check(); err != nil {
			return err
		}
	}
	return nil
}
