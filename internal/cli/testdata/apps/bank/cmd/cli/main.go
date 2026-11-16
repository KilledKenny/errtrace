// Command cli runs single operations against an in-memory bank.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"example.com/bank/audit"
	"example.com/bank/domain"
	"example.com/bank/events"
	"example.com/bank/limits"
	"example.com/bank/middleware"
	"example.com/bank/parse"
	"example.com/bank/repo/memory"
	"example.com/bank/retry"
	"example.com/bank/rules"
	"example.com/bank/service"
)

var (
	errUsage  = errors.New("usage: cli transfer FROM TO AMOUNT [TIME] | balance ID | settled N | freeze ID | check FROM AMOUNT")
	errDryRun = errors.New("cli: dry run, nothing was moved")
)

// stats counts completed transfers; it is an event handler.
type stats struct{ done, max int }

func (s *stats) /*@on_transfer*/onTransfer(events.Event) error {
	if s.done++; s.done > s.max {
		return fmt.Errorf("cli: more than %d transfers in one run", s.max)
	}
	return nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type app struct {
	svc   *service.Service
	bus   *events.Bus
	trail *bytes.Buffer
}

func newApp(dry bool) *app {
	store := memory.New(
		&domain.Account{ID: "alice", Owner: "Alice", Balance: 500},
		&domain.Account{ID: "bob", Owner: "Bob", Balance: 20},
		&domain.Account{ID: "carol", Owner: "Carol", Status: domain.Frozen},
	)
	eng := rules.Default(limits.Policy{PerTransfer: 300, Daily: 400})
	if dry {
		eng.Add(rules.Func{Label: "dry-run", Fn: func(context.Context, rules.Input) error { return errDryRun }})
	}
	st := &stats{max: 100}
	bus := events.NewBus()
	bus.Subscribe("transfer.done", st.onTransfer)
	bus.Subscribe("account.status", func(ev events.Event) error {
		if ev.Transfer.From == "alice" {
			return &domain.ValidationError{Field: "id", Reason: "alice cannot be frozen"}
		}
		return nil
	})
	trail := new(bytes.Buffer)
	return &app{svc: service.New(store, eng, bus, audit.New(&audit.Quota{W: trail, Left: 1 << 16}), 3), bus: bus, trail: trail}
}

func /*@run*/run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) < 2 {
		return errUsage
	}
	a := newApp(os.Getenv("BANK_DRY_RUN") != "")
	defer io.Copy(out, a.trail)
	switch cmd, rest := args[0], args[1:]; cmd {
	case "transfer":
		t, err := parse./*@line_call*/Line(1, rest)
		if err != nil {
			return err
		}
		return a.svc./*@transfer_call*/Transfer(ctx, t)
	case "balance":
		bal, err := a.svc.Balance(ctx, domain.AccountID(rest[0]))
		if err != nil {
			return fmt.Errorf("cli: balance: %w", err)
		}
		fmt.Fprintln(out, bal)
		return nil
	case "settled":
		return /*@settled_call*/settled(a.svc, rest[0])
	case "freeze":
		return a.svc./*@setstatus_call*/SetStatus(ctx, domain.AccountID(rest[0]), domain.Frozen)
	case "check":
		return /*@check_call*/check(ctx, a.svc, rest)
	case "replay":
		return a.bus./*@first_call*/PublishFirst(events.Event{Kind: "transfer.done"})
	}
	return fmt.Errorf("%w (unknown command %q)", errUsage, args[0])
}

func /*@settled*/settled(svc *service.Service, arg string) error {
	n, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) {
			return fmt.Errorf("cli: transfer number %q: %w", ne.Num, ne.Err)
		}
		return err
	}
	return svc.Settled(domain.TransferID(n))
}

// check answers "could FROM pay AMOUNT?" without touching the ledger.
func /*@check*/check(ctx context.Context, svc *service.Service, rest []string) error {
	if len(rest) < 2 {
		return errUsage
	}
	amt, err := parse.Amount(rest[1])
	if err != nil {
		return err
	}
	in := rules.Input{
		Transfer: domain.Transfer{From: domain.AccountID(rest[0]), To: "bank", Amount: amt, Currency: "EUR"},
		From:     &domain.Account{ID: domain.AccountID(rest[0])},
		To:       &domain.Account{ID: "bank"},
	}
	if err := rules./*@explain_funds_call*/Explain(ctx, rules.Funds{}, in); err != nil {
		return err
	}
	if err := rules./*@explain_limit_call*/Explain(ctx, rules.Limit{Policy: limits.Policy{PerTransfer: 300}}, in); err != nil {
		return err
	}
	if err := /*@probe_call*/probe(ctx); err != nil {
		return err
	}
	return svc./*@canpay_call*/CanPay(ctx, in.Transfer.From, amt)
}

// probe exercises the plumbing with fixed, statically known pieces.
func /*@probe*/probe(ctx context.Context) error {
	ok := func(context.Context, middleware.Request) error { return nil }
	if err := middleware./*@guarded_ok_call*/Guarded(ctx, "t", middleware.Request{Token: "t"}, ok); err != nil {
		return err
	}
	var sink bytes.Buffer
	if err := audit./*@write_buf_call*/Write(&sink, audit.Entry{Kind: "probe"}); err != nil {
		return err
	}
	if err := audit./*@write_quota_call*/Write(&audit.Quota{W: io.Discard, Left: 8}, audit.Entry{Kind: "probe"}); err != nil {
		return err
	}
	busy := 2
	return retry./*@retry_busy_call*/Do(ctx, 2, func() error {
		if busy--; busy >= 0 {
			return retry.ErrBusy
		}
		return errDryRun
	})
}
