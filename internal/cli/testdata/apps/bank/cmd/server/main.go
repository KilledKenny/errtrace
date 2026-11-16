// Command server reads requests ("PATH TOKEN BODY", one per line) from
// stdin and writes one status line per request.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"

	"example.com/bank/api"
	"example.com/bank/audit"
	"example.com/bank/config"
	"example.com/bank/domain"
	"example.com/bank/events"
	"example.com/bank/limits"
	"example.com/bank/middleware"
	"example.com/bank/repo"
	"example.com/bank/repo/filerepo"
	"example.com/bank/repo/memory"
	"example.com/bank/rules"
	"example.com/bank/service"
)

func main() {
	path := "bank.conf"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	if err := run(path, os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func /*@run*/run(path string, in io.Reader, out io.Writer) (err error) {
	cfg, err := config./*@load_call*/Load(path)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
	store, err := /*@openstore_call*/openStore(cfg)
	if err != nil {
		return err
	}
	logger, err := openAudit(cfg)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, logger./*@close_call*/Close()) }()

	bus := events.NewBus()
	bus.Subscribe("transfer.done", func(ev events.Event) error {
		if ev.Transfer.Amount > domain.Money(cfg.Daily) {
			return fmt.Errorf("server: transfer %d needs review: %w", ev.Transfer.ID, domain.ErrLimitExceeded)
		}
		return nil
	})
	policy := limits.Policy{PerTransfer: domain.Money(cfg.PerTransfer), Daily: domain.Money(cfg.Daily)}
	svc := service.New(store, rules.Default(policy), bus, logger, cfg.Retries)
	srv := api.NewServer(svc, cfg.Token)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := srv./*@selftest_call*/Balance(ctx, "cash"); err != nil {
		log.Printf("server: self-test: %v", err)
	}
	return /*@serve_call*/serve(ctx, srv, in, out)
}

// openStore picks the file store when a data directory is configured.
func /*@openstore*/openStore(cfg *config.Config) (repo.Store, error) {
	if cfg.DataDir == "" {
		return memory.New(&domain.Account{ID: "cash", Owner: "bank", Balance: 1_000_000}), nil
	}
	s, err := filerepo.Open(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("server: store: %w", err)
	}
	return s, nil
}

func /*@openaudit*/openAudit(cfg *config.Config) (*audit.Logger, error) {
	if cfg.AuditPath == "" {
		return audit.New(os.Stderr), nil
	}
	return audit.Open(cfg.AuditPath)
}

// serve handles requests until stdin ends or a response cannot be written.
func /*@serve*/serve(ctx context.Context, srv *api.Server, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		path, rest, _ := strings.Cut(sc.Text(), " ")
		token, body, _ := strings.Cut(rest, " ")
		if path == "local" { // trusted in-process caller: no middleware
			if err := /*@respond_local_call*/respond(out, srv.Transfer(ctx, []byte(rest))); err != nil {
				return err
			}
			continue
		}
		if path == "validate" {
			if err := /*@respond_validate_call*/respond(out, srv.Validate([]byte(rest))); err != nil {
				return err
			}
			continue
		}
		if path == "import" {
			if err := /*@respond_import_call*/respond(out, srv.Import(ctx, []byte(rest))); err != nil {
				return err
			}
			continue
		}
		req := middleware.Request{Path: path, Token: token, Body: []byte(body)}
		if err := /*@respond_call*/respond(out, srv./*@srv_serve_call*/Serve(ctx, req)); err != nil {
			return err
		}
	}
	return nil
}

// respond writes the status line for err. API errors are part of the
// protocol; anything else means the server itself is broken.
func /*@respond*/respond(out io.Writer, err error) error {
	var ae *api.Error
	switch {
	case err == nil:
		fmt.Fprintln(out, "200 ok")
	case errors.As(err, &ae):
		fmt.Fprintf(out, "%d %s\n", ae.Code, ae.Message)
	default:
		return fmt.Errorf("server: unhandled: %w", err)
	}
	return nil
}
