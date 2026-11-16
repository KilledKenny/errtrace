// Command plugins is a small notification system built from plugins:
// drivers, handlers, middlewares and a registry.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"example.com/plugins/core"
	"example.com/plugins/dispatch"
	"example.com/plugins/drivers"
	"example.com/plugins/middleware"
	"example.com/plugins/registry"
	"example.com/plugins/sink"
	"example.com/plugins/tmpl"
	"example.com/plugins/tree"
)

var (
	errEscalate = errors.New("escalation required")
	errUnnamed  = errors.New("handler without a name")
	errQuota    = errors.New("quota exceeded")
)

// pagerHandler is a Handler defined outside the registry package.
type pagerHandler struct{}

func (pagerHandler) Handle(e core.Event) error { return drivers.Pager{}.Send(e) }

var failed int

func report(err error) {
	if err != nil {
		failed++
		fmt.Fprintln(os.Stderr, "plugins:", err)
	}
}

func main() {
	ev := core.Event{Topic: "deploy", Body: "v2 is live", Priority: 3}
	tiers(ev)
	dispatcher(ev)
	alerts(ev)
	callbacks(ev)
	handlers(ev)
	templates(ev)
	report(pipeline(ev))
	report(guarded(ev))
	report(quota(ev))
	if failed > 0 {
		os.Exit(1)
	}
}

// tiers feeds every Send* function its fixed set of driver types.
func tiers(ev core.Event) {
	report(dispatch.SendDirect(drivers.Email{}, ev))

	report(dispatch.SendPair(drivers.Email{}, ev))
	report(dispatch.SendPair(&drivers.SMS{}, ev))

	report(dispatch.SendEither(drivers.Email{}, ev))
	report(dispatch.SendEither(&drivers.Email{}, ev))

	report(dispatch.SendSeven(drivers.Email{}, ev))
	report(dispatch.SendSeven(&drivers.SMS{}, ev))
	report(dispatch.SendSeven(drivers.Slack{}, ev))
	report(dispatch.SendSeven(&drivers.Webhook{}, ev))
	report(dispatch.SendSeven(drivers.Push{}, ev))
	report(dispatch.SendSeven(drivers.Pager{}, ev))
	report(dispatch.SendSeven(drivers.File{}, ev))

	report(dispatch.SendEight(drivers.Email{}, ev))
	report(dispatch.SendEight(&drivers.SMS{}, ev))
	report(dispatch.SendEight(drivers.Slack{}, ev))
	report(dispatch.SendEight(&drivers.Webhook{}, ev))
	report(dispatch.SendEight(drivers.Push{}, ev))
	report(dispatch.SendEight(drivers.Pager{}, ev))
	report(dispatch.SendEight(drivers.File{}, ev))
	report(dispatch.SendEight(drivers.Queue{}, ev))

	report(dispatch.SendQuiet(drivers.Email{}, ev))
	report(dispatch.SendQuiet(&drivers.SMS{}, ev))
	report(dispatch.SendQuiet(drivers.Slack{}, ev))
	report(dispatch.SendQuiet(&drivers.Webhook{}, ev))
	report(dispatch.SendQuiet(drivers.Push{}, ev))
	report(dispatch.SendQuiet(drivers.Pager{}, ev))
	report(dispatch.SendQuiet(drivers.File{}, ev))
	report(dispatch.SendQuiet(drivers.Log{}, ev))

	report(dispatch.SendNine(drivers.Email{}, ev))
	report(dispatch.SendNine(&drivers.SMS{}, ev))
	report(dispatch.SendNine(drivers.Slack{}, ev))
	report(dispatch.SendNine(&drivers.Webhook{}, ev))
	report(dispatch.SendNine(drivers.Push{}, ev))
	report(dispatch.SendNine(drivers.Pager{}, ev))
	report(dispatch.SendNine(drivers.File{}, ev))
	report(dispatch.SendNine(drivers.Queue{}, ev))
	report(dispatch.SendNine(drivers.Log{}, ev))

	report(dispatch.SendWrapped(drivers.Logged{Driver: drivers.Pager{}, Prefix: "ops"}, ev))
	report(dispatch.SendWrapped(drivers.Fancy{Color: "red"}, ev))
	report(dispatch.SendWrapped(drivers.Strict{SMS: &drivers.SMS{}}, ev))

	// Used directly, never as a core.Driver.
	report(drivers.Fax{}.Send(ev))
	report(drivers.Gateway{Up: true}.Send(ev))
}

func dispatcher(ev core.Event) {
	d, err := dispatch.New(&drivers.Webhook{URL: "https://example.com/hook"},
		dispatch.WithLimit(5),
		dispatch.WithName("main"),
		dispatch.WithFallback(sink.Remote{}),
	)
	if err != nil {
		report(err)
		return
	}
	d.SetHooks(dispatch.Hooks{
		Before: dispatch.Validate,
		After: func(e core.Event) error {
			if e.Priority > 5 {
				return errEscalate
			}
			return nil
		},
	})
	report(d.Send(ev))
	report(d.Audit(ev))
	report(d.Drop(ev))
	report(dispatch.Probe(ev))
	report(d.Async(registry.MustLookup("audit"), ev))
	dispatch.Fire(registry.MustLookup("metrics"), ev)
	report(d.Shutdown())
	report(d.Close())
}

func alerts(ev core.Event) {
	report(dispatch.AlertOps(ev))
	report(dispatch.AlertUser(ev, &drivers.SMS{}))
	report(dispatch.AlertVia(drivers.Queue{Cap: 1}, ev))
	report(dispatch.Route("sms", ev))
	conn := &drivers.Conn{}
	report(dispatch.Cycle(conn, ev))
	report(dispatch.Release(conn))
	report(dispatch.Release(drivers.Email{}))
	report(dispatch.SendQuietly(ev))
}

func callbacks(ev core.Event) {
	report(dispatch.Lazy(ev))
	report(dispatch.FirstError(drivers.Slack{}, []core.Event{ev, ev}))
	report(dispatch.ByAddr(drivers.File{}, ev))
	report(dispatch.Limited(ev))
	report(dispatch.RetrySMS(&drivers.SMS{}, ev))
	report(dispatch.RetryValidate(ev))
	report(dispatch.RetryDriver(drivers.Pager{}, ev))
	report(dispatch.ViaMethodExpr(ev))
	report(dispatch.ViaIfaceMethodExpr(registry.MustLookup("audit"), ev))
}

func handlers(ev core.Event) {
	registry.Register("pager", pagerHandler{})
	report(registry.Dispatch("pager", ev))
	report(registry.Each(/*@each_lit*/func(name string, h core.Handler) error {
		if name == "" {
			return errUnnamed
		}
		return nil
	}))
	report(registry.SelfTest(ev))
	report(registry.RunPlugins(ev))

	inner := (&tree.Group{Label: "storage"}).
		Add(tree.Leaf{Driver: drivers.File{Path: "/var/log/events"}}).
		Add(tree.Leaf{Driver: drivers.Queue{Cap: 16}})
	root := (&tree.Group{Label: "all"}).Add(inner)
	report(root.Handle(ev))
}

func templates(ev core.Event) {
	if _, err := tmpl.Load(&tmpl.Source{Data: "hello {{.Body}}"}); err != nil {
		report(err)
	}
	if _, err := tmpl.Load(strings.NewReader("plain")); err != nil {
		report(err)
	}
	report(tmpl.Header(&tmpl.Source{Broken: true}))
	report(tmpl.Header(strings.NewReader("")))
	_, err := tmpl.Format("json", ev)
	report(err)
}

// pipeline runs Tag(Filter(pagerHandler)).
func /*@pipeline*/pipeline(e core.Event) error {
	h := middleware.Chain(pagerHandler{}, middleware.Tag("ops"), middleware.Filter("deploy"))
	return h.Handle(e)
}

// guarded runs Recover(Retry(pagerHandler)).
func guarded(e core.Event) error {
	h := middleware.Chain(pagerHandler{}, middleware.Recover(), middleware.Retry(2))
	return h.Handle(e)
}

// quota calls the HandlerFunc adapter directly on a closure literal.
func quota(e core.Event) error {
	h := core.HandlerFunc(func(core.Event) error { return errQuota })
	return h./*@quota_call*/Handle(e)
}
