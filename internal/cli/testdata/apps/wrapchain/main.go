// Command wrapchain is a small document pipeline: parse, validate,
// transform, export. Each layer wraps the errors of the layer below.
package main

import (
	"fmt"
	"os"

	"example.com/wrapchain/doc"
	"example.com/wrapchain/export"
	"example.com/wrapchain/parse"
	"example.com/wrapchain/transform"
	"example.com/wrapchain/validate"
)

func load(path string) (*doc.Document, error) {
	f, err := parse.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d := &doc.Document{Path: path, Fields: map[string]string{}}
	if d.Version, err = parse.Version("1"); err != nil {
		return nil, err
	}
	if _, err = fieldOrZero("x"); err != nil {
		return nil, err
	}
	if _, err = parse.Field("size", "12"); err != nil {
		return nil, err
	}
	if _, err = parse.Percent("50"); err != nil {
		return nil, err
	}
	checks := []error{
		parse.Row(1, "a"), parse.Cell("a", "b"), parse.Column(1, 4, "x"),
		parse.Reorder("x"), parse.Tag("t", true), parse.Footer("EOF"), parse.Label("l"),
		parse.Section("intro", "text"),
	}
	for _, err := range checks {
		if err != nil {
			return nil, err
		}
	}
	return d, nil
}

func check(d *doc.Document) []error {
	return []error{
		validate.All(d), validate.Single(d), validate.None(), validate.Collect(d),
		validate.Report(d), validate.Labelled(d), validate.Lines(d),
		checkLenient(d),
	}
}

func rewrite(d *doc.Document) []error {
	return []error{
		transform.Title(d), transform.Noop(), transform.Stage(d),
		transform.Field(d, "author"), transform.Lazy(d), transform.Late(d),
		transform.Maybe(d, true), transform.Setter(d), transform.Bare(d), transform.Always(d), transform.Multi(d),
		transform.Retryable(d), transform.OverLimit(d), transform.Include(d, 0),
		transform.Normalize(d, true), transform.Trim(d), transform.Trace(d),
		transform.Peel(d), transform.PeelJoin(), transform.PeelPlain(),
		transform.PeelTwice(d),
		titleStrict(d), titleLenient(d), titleQuiet(d), normalizeQuiet(d),
	}
}

func ship(d *doc.Document) []error {
	sinks := []export.Sink{&export.FileSink{Dir: "out"}, export.NetSink{URL: "https://example.com"}}
	var errs []error
	for _, s := range sinks {
		errs = append(errs, export.Export(s, d), export.Flush(s, d))
	}
	return append(errs, export.RunHook(export.Audit, d), export.Rollout())
}

func main() {
	d, err := load("input.doc")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	failed := false
	for _, stage := range [][]error{check(d), rewrite(d), ship(d)} {
		for _, err := range stage {
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				failed = true
			}
		}
	}
	if failed {
		os.Exit(1)
	}
}
