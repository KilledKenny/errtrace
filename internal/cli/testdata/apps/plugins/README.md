# plugins

A plugin-based notification system used as an errtrace test application.
Focus: **dynamic calls and Candidate resolution** (VTA), the `-fanout`
grouping threshold, and `unresolved(no-candidates)`.

    main -> dispatch -> drivers, sink        (core.Driver implementations)
         -> registry                         (map of core.Handler, init, var initialiser)
         -> middleware                       (func(Handler) Handler chains)
         -> tree                             (composite of Handlers)
         -> tmpl                             (io.Reader, map of functions)
                 core                        (Event, interfaces, HandlerFunc, shared sentinels)

## Packages

| Package | Role | Cases it provides |
|---|---|---|
| `core` | shared types | `Handler`, `Driver`, `Plugin` (embeds `Named` and `Handler`), `Auditor` (no implementation), the `HandlerFunc` adapter (dynamic call on the receiver) |
| `drivers` | built-in drivers | nine instantiated `Driver`s with one recognisable Origin each (sentinel, wrapped sentinel, concrete `*HTTPError`, opaque; value and pointer receivers; `Log` never fails), `Carrier` (never instantiated), `Fax` (instantiated, never converted to `Driver`), `Logged` (interface embedded in a struct), `Fancy` (embedded struct), `Strict` (embedded pointer), `Audited` (embeds the interface and declares its own `Send`), `Channel` (embeds `core.Driver` and `io.Closer`) with `*Conn`, `Status` (`fmt.Stringer` + `error`) |
| `sink` | external driver | a `Driver` implemented in another package |
| `dispatch` | delivery | `fanout.go`: one `d.Send(e)` site per tier, fed 1, 2, 7, 8, 8 (one silent), 9 driver types by `main`, plus the promoted-method and decorator tiers; `dispatch.go`: functional options, function-typed and interface-typed struct fields, never-set fields and a nil interface (no Candidates), deferred and goroutine dynamic calls, one call site shared by several callers (`deliver`), driver picked by `switch`, type assertion to `io.Closer`, guards after a dynamic call (`errors.Is`, `errors.As`); `funcs.go`: closures (mutated capture, capture written by a callback, capture by address), functions returned from functions (capturing a local, capturing an error parameter), a callback parameter fed method values / functions, method expressions; `legacy.go`: functions nothing calls |
| `registry` | handler registry | `map[string]core.Handler` filled by a package-level initialiser, `init()` and `main`; a never-registered handler; callback iteration (`Each`) with two callers; `[]core.Plugin` built by an interface-to-interface type assertion and sorted through a local `sort.Interface` |
| `middleware` | decorators | `Chain`, `Tag`, `Filter`, `Retry` (`"%w: %w"`), `Recover` (deferred `recover()` writing the named result); all closures go through `core.HandlerFunc`, so they recurse through the interface |
| `tree` | composite | `*Group` of `Handler`s containing `Leaf`s and `Group`s (recursion through an interface, growing Wrap Chains) |
| `tmpl` | templates | local `io.Reader` handed to `io.ReadAll`, direct `r.Read` with a local and a stdlib implementation, a map of functions |
| root (`main`) | wiring | reaches everything except `dispatch/legacy.go`, `WithTLS`, `Carrier` and `orphanHandler` (`dispatch.Probe` panics by design, so `main` only runs it when `PLUGINS_PROBE` is set); `pagerHandler` is a `Handler` defined in package `main` |

`main.tiers` calls each `dispatch.Send*` function once per driver type on
purpose: VTA merges all slices of one element type, so passing the drivers
through `[]core.Driver` slices would give every tier the same Candidates.

A call-site Target must point at the callee name (`d.closer./*@m*/Close()`)
or at the opening parenthesis (`pair_lparen_call`), so markers sit after the
receiver (`defer_recv` checks that a position on the receiver is rejected).

## Known state of the tool that the queries record

- Plan step S09 (call-site and local narrowing) is not implemented at the
  time of writing. Queries whose correct result needs it are kept and
  marked `SUSPECTED BUG (precision; ...)` with the step named in the comment.
- Plan step S08 (deferred changes to named results) is not implemented
  either. Its queries (`close_decl`, `recover_lit`) record the
  `unresolved(memory)` result that the existing e2e fixtures document; they
  are marked only for the position-less span `-` of one entry.
- Plan step S12 is not implemented: an unreachable Target gets the S06
  warning and `unresolved(no-candidates)` for dynamic calls on its
  parameters (`legacy_decl`, `legacy_send_call`, `nine_pkg_only`). This is
  documented interim behaviour and not marked.
- VTA over-approximation that is accepted by design (map values, slice
  elements and fields are merged per type; the `HandlerFunc` adapter merges
  all its receivers) is described in the query comment but not marked as a
  bug. The same holds for captured variables written by another closure or
  through a pointer, and for channel receives (`unresolved(memory)`).

## Queries

`+` = positive (Origins expected in the set), `-` = negative (empty set, an
Origin that must be absent, an Unresolved-only set, a rejected Target or flag).

| Query | Target (args) | Checks | +/- | Suspected bug |
|---|---|---|---|---|
| `direct_fanout1` | `-fanout=1 dispatch/fanout.go:@direct` | Interface call with exactly one Candidate, grouping threshold 1. | + | no |
| `pair_decl` | `dispatch/fanout.go:@pair` | Interface call with two Candidates: a value receiver (Email) and a pointer receiver (*SMS). | + | no |
| `pair_fanout2` | `-fanout 2 dispatch/fanout.go:@pair` | Same as pair_decl with "-fanout 2" (two-argument flag form): 2 entries at a site with 2 Candidates reach the threshold, so one group of 2. | + | no |
| `either_decl` | `dispatch/fanout.go:@either` | The same type passed by value (Email) and by pointer (*Email) to one interface parameter; (*Email).Send is the promoted wrapper of the value method. | + | no |
| `seven_default` | `dispatch/fanout.go:@seven` | Exactly 7 Candidates, default -fanout (8): below the threshold. | + | no |
| `seven_fanout7` | `-fanout=7 dispatch/fanout.go:@seven` | Exactly 7 Candidates with -fanout=7: at the threshold, so the same 7 entries as seven_default are printed as one group of 7. | + | no |
| `seven_fanout_hex` | `-fanout=0x7 dispatch/fanout.go:@seven` | -fanout is parsed like a Go integer literal, so 0x7 is 7. Expected: same output as seven_fanout7 (one group of 7). | + | no |
| `eight_default` | `dispatch/fanout.go:@eight` | Exactly 8 Candidates, each with one Origin, default -fanout (8). | + | no |
| `eight_fanout9` | `-fanout=9 dispatch/fanout.go:@eight` | 8 Candidates with the threshold one above: no group, 8 full entries. | + | no |
| `quiet_default` | `dispatch/fanout.go:@quiet` | 8 Candidates but only 7 entries: Log.Send never fails. Grouping needs at least -fanout entries AND Candidates, so with the default 8 nothing is grouped. | + | no |
| `quiet_fanout7` | `-fanout=7 dispatch/fanout.go:@quiet` | 8 Candidates, 7 entries, -fanout=7. Expected: one group of 7 entries whose header says 8 candidates. | + | no |
| `nine_default` | `dispatch/fanout.go:@nine` | 9 Candidates (eight failing drivers plus Log), 8 entries, default -fanout. | + | no |
| `nine_fanout0` | `-fanout=0 dispatch/fanout.go:@nine` | -fanout=0 never groups. Expected: the 8 entries of nine_default in full, every dynamic hop "N of 9 candidates". | + | no |
| `nine_fanout_max` | `-fanout=9223372036854775807 dispatch/fanout.go:@nine` | The largest accepted -fanout value (max int64). Expected: accepted, no group, same output as nine_fanout0. | + | no |
| `nine_call_site` | `dispatch/fanout.go:@nine_call` | Call-site Target on the dynamic call d.Send itself (9 Candidates). | + | no |
| `fanout_negative` | `-fanout=-1 dispatch/fanout.go:@nine` | Negative: -fanout below 0 is a usage error. Expected: exit 2, message "invalid -fanout -1 (want 0 or more)" and the usage text on stderr. | - | no |
| `fanout_overflow` | `-fanout=99999999999999999999 dispatch/fanout.go:@nine` | Negative: -fanout does not fit an int. Expected: exit 2, a flag parse error ("value out of range") and the usage text; nothing on stdout. | - | no |
| `fanout_not_number` | `-fanout=many dispatch/fanout.go:@nine` | Negative: -fanout is not a number. Expected: exit 2, flag parse error and usage text; nothing on stdout. | - | no |
| `fanout_missing_value` | `dispatch/fanout.go:@nine -fanout` | Negative: flags after the Target are positional arguments (flag package stops at the first non-flag). | - | no |
| `fanout_static` | `-fanout=1 drivers/extra.go:@gw_send` | SUSPECTED BUG (low confidence): the concrete drivers.Status entry has a zero-width span on the return keyword instead of the conversion Status(503) that builds it. Otherwise: -fanout=1 on a Target without dynamic calls groups nothing. | + | yes |
| `route_fanout3` | `-fanout=3 dispatch/dispatch.go:@route` | Driver selected by a switch in pick(); Route also returns a static sentinel. | + | no |
| `route_send_call` | `dispatch/dispatch.go:@route_send` | Call-site Target on d.Send where d comes from a switch in another function. | + | no |
| `wrapped_decl` | `dispatch/fanout.go:@wrapped` | Promoted methods as Candidates: Logged embeds the core.Driver interface (holding a Pager), Fancy embeds struct base, Strict embeds *SMS. | + | no |
| `new_decl` | `dispatch/dispatch.go:@new` | Functional options: a variadic slice of closures called in a loop. | + | no |
| `opt_call` | `dispatch/dispatch.go:@opt_call` | Call-site Target on the dynamic call opt(d) of a range variable. | + | no |
| `withname_lit` | `dispatch/dispatch.go:@withname_lit` | Declaration Target on a closure literal (its func keyword). Expected: only opaque errors.New "dispatch: empty name". | + | no |
| `disp_send_decl` | `dispatch/dispatch.go:@disp_send` | Function-typed struct fields (Hooks.Before/After) and two interface fields (primary, fallback), each holding one thing. | + | no |
| `before_call` | `dispatch/dispatch.go:@before` | Call-site Target on a call through a function-typed field of a nested struct. | + | no |
| `audit_decl` | `dispatch/dispatch.go:@audit` | Dynamic call with NO Candidates: core.Auditor has no implementation and the field is never set. | - | no |
| `drop_decl` | `dispatch/dispatch.go:@drop` | A function-typed field that is never assigned: the call has no Candidates. | + | no |
| `probe_decl` | `dispatch/dispatch.go:@probe` | Interface holding nil: "var d core.Driver; return d.Send(e)" (panics at run time). | - | no |
| `close_decl` | `dispatch/dispatch.go:@close` | SUSPECTED BUG: one of the two unresolved(memory) entries has the position-less span "-". Deferred closure calling an io.Closer dynamically and assigning the named result; the missing Origins (ErrDropped, ErrSpoolBusy) are plan step S08, not built yet. | + | yes |
| `shutdown_decl` | `dispatch/dispatch.go:@shutdown` | "defer d.closer.Close()" discards the result. Expected: only core.ErrDropped; ErrSpoolBusy must NOT be in the set. | - | no |
| `defer_close_call` | `dispatch/dispatch.go:@defer_close` | Call-site Target on a deferred dynamic call (stdlib interface io.Closer implemented locally by *spool), pointing at the method name. | + | no |
| `async_decl` | `dispatch/dispatch.go:@async` | The error of a dynamic call made in a goroutine comes back over a channel. | + | no |
| `go_handle_call` | `dispatch/dispatch.go:@go_handle` | Call-site Target on a dynamic call inside a goroutine closure; h is a captured parameter fed from the registry map. | + | no |
| `go_stmt_call` | `dispatch/dispatch.go:@go_stmt` | Call-site Target on the call of a go statement (go h.Handle(e)). | + | no |
| `deliver_decl` | `dispatch/dispatch.go:@deliver` | One dynamic call site reached from four callers with different drivers. | + | no |
| `ops_call` | `dispatch/dispatch.go:@ops_call` | SUSPECTED BUG (precision; plan step S09 "local narrowing" not built yet): deliver(drivers.Pager{}, e) can only fail with ErrPager, and the hop should read "1 of 4 candidates". | + | yes |
| `alertuser_decl` | `dispatch/dispatch.go:@alertuser` | SUSPECTED BUG (precision; S09 not built yet, see ops_call): deliver(Email{}) and deliver(sms) can only give ErrEmail and ErrSMS; actual also ErrPager and ErrQueue. | + | yes |
| `alertvia_decl` | `dispatch/dispatch.go:@alertvia` | deliver(d, e) where d is AlertVia's own interface parameter; its only caller passes a Queue. | + | no |
| `quietly_decl` | `dispatch/dispatch.go:@quietly` | SUSPECTED BUG (consequence of the missing narrowing, see ops_call): SendPair(&SMS{}) can only fail with ErrSMS, and errors.Is(err, ErrSMS) returns nil for it, so the correct Error Set is empty (0 entries). | - | yes |
| `cycle_decl` | `dispatch/dispatch.go:@cycle` | Interface that embeds two interfaces (core.Driver and io.Closer); *Conn is the only implementation. | + | no |
| `release_decl` | `dispatch/dispatch.go:@release` | Interface-to-interface type assertion d.(io.Closer) narrows the Candidates: Release is given an Email (no Close method) and a *Conn. | + | no |
| `lazy_decl` | `dispatch/funcs.go:@lazy` | Closure capturing a variable that is reassigned after the closure is created and before it runs. | + | no |
| `firsterr_decl` | `dispatch/funcs.go:@firsterr` | A result-less callback writes a captured variable; inside it a dynamic call on a captured interface parameter. | + | no |
| `byaddr_decl` | `dispatch/funcs.go:@byaddr` | Capture by address: collect(&last, d, e) stores the error of a dynamic call through the pointer. | + | no |
| `limited_decl` | `dispatch/funcs.go:@limited` | Function returned from a function and called through a local variable; the closure has mutable captured state. | + | no |
| `retry_decl` | `dispatch/funcs.go:@retry` | Callback parameter with three kinds of function value: a bound method of a concrete type (sms.Send), a plain function (Validate) and a method value bound to an interface (d.Send, d is a Pager). | + | no |
| `retrysms_call` | `dispatch/funcs.go:@retrysms_call` | SUSPECTED BUG (precision; plan step S09 not built yet, see ops_call): retry(2, e, sms.Send) passes a method value of a concrete type, so only drivers.ErrSMS is possible. | + | yes |
| `retrydriver_decl` | `-fanout=1 dispatch/funcs.go:@retrydriver` | SUSPECTED BUG (precision; S09 not built yet, see retrysms_call): a method value bound to an interface (only ever a Pager) passed as a callback can only give ErrPager; actual: all three Candidates of retry's fn, as one group (-fanout=1). | + | yes |
| `mv_noncall` | `dispatch/funcs.go:@mv_noncall` | Negative: the position is on a method value (d.Send without a call). | - | no |
| `mexpr_decl` | `dispatch/funcs.go:@mexpr` | Method expressions (*SMS).Send and Email.Send stored in variables and called. | + | no |
| `imexpr_decl` | `dispatch/funcs.go:@imexpr` | Interface method expression core.Handler.Handle called with a handler from the registry map. | + | no |
| `legacy_decl` | `dispatch/legacy.go:@legacy` | Negative: unreachable declaration with dynamic calls on two interface parameters. Expected today (S06 interim behaviour): the warning and two unresolved(no-candidates) entries, no borrowed Origin. | - | no |
| `debugcarrier_decl` | `dispatch/legacy.go:@debugcarrier` | Unreachable Target that is the only place instantiating Carrier as a core.Driver. | + | no |
| `dispatch_decl` | `registry/registry.go:@dispatch` | Map of handlers filled by a package-level initialiser (audit), init() (metrics) and main (pagerHandler, a type from package main). | + | no |
| `dispatch_handle_call` | `registry/registry.go:@dispatch_handle` | Call-site Target on h.Handle with h loaded from a map in another function. | + | no |
| `each_decl` | `registry/registry.go:@each` | Callback parameter with two callers: main's closure (errUnnamed) and SelfTest's closure (calls h.Handle). | + | no |
| `selftest_decl` | `registry/registry.go:@selftest` | SUSPECTED BUG (precision; plan step S09 not built yet, see ops_call): SelfTest passes its own closure literal to Each, so only that closure can run: ErrAuditFull, ErrMetricsDown, ErrPager wrapped by "handler %s: %w". | + | yes |
| `each_lit` | `main.go:@each_lit` | Declaration Target on a closure literal passed as an argument in package main. | + | no |
| `runplugins_decl` | `registry/registry.go:@runplugins` | Slice of core.Plugin (interface embedding Named and Handler) built by a type assertion h.(core.Plugin) and sorted with a local sort.Interface. | + | no |
| `name_call` | `registry/registry.go:@name_call` | Negative: call-site Target on a dynamic call whose method has no error result (p.Name()). Expected: exit 2, "p.Name has no error result". | - | no |
| `handle_iface` | `core/core.go:@handle_iface` | Negative: the position is a method name in an interface type declaration. Expected: exit 2, "interface method has no body; query a call site". | - | no |
| `status_conv` | `drivers/extra.go:@status_conv` | Negative: Status(503) is a conversion, not a call. Expected: exit 2, "not a call to a function". | - | no |
| `hf_handle_decl` | `-chain-depth=1 core/core.go:@hf_handle` | Declaration of the adapter method HandlerFunc.Handle: the called function is the receiver. | + | no |
| `quota_call` | `-chain-depth=1 main.go:@quota_call` | SUSPECTED BUG (precision; plan step S09 not built yet): a static call of the adapter HandlerFunc.Handle on a closure literal can only return main.errQuota. | + | yes |
| `pipeline_decl` | `-chain-depth=1 main.go:@pipeline` | Middleware chain Tag(Filter(pagerHandler)) built by Chain (a slice of func(Handler) Handler) and called through the interface. | + | no |
| `recover_lit` | `-chain-depth=1 middleware/middleware.go:@recover_lit` | SUSPECTED BUG (same as close_decl): an Unresolved entry with span "-". Closure with a deferred recover() writing the named result; Origins missing until S08. | + | yes |
| `group_handle_decl` | `-chain-depth=3 tree/tree.go:@group_handle` | Composite pattern: (*Group).Handle calls m.Handle on members that are Leafs or Groups (recursion through the interface). | + | no |
| `leaf_handle_decl` | `tree/tree.go:@leaf_handle` | Dynamic call through an interface-typed struct field of a value receiver. | + | no |
| `load_decl` | `tmpl/tmpl.go:@load` | Local io.Reader passed to the stdlib (io.ReadAll calls r.Read dynamically); Load is given a *tmpl.Source and a *strings.Reader. | + | no |
| `header_decl` | `tmpl/tmpl.go:@header` | Direct r.Read on an io.Reader with a local and a stdlib implementation. | + | no |
| `format_decl` | `tmpl/tmpl.go:@format` | Map of functions filled by a package-level initialiser. Expected: opaque fmt.Errorf "unknown format %q" and core.ErrEmptyBody (formatJSON); formatText never fails. | + | no |
| `defer_recv` | `dispatch/dispatch.go:@defer_recv` | Negative: the position is on the receiver expression of a deferred call (defer d.closer.Close()), not on the callee name or its "(". | - | no |
| `lookup_decl` | `registry/registry.go:@lookup` | Error result after a non-error interface result; the Wrap puts %w first. | + | no |
| `fallback_call` | `dispatch/dispatch.go:@fallback` | Call-site Target on an interface field whose only value comes from a functional option closure and is a type of another package (sink.Remote). | + | no |
| `readall_call` | `tmpl/tmpl.go:@readall` | Call-site Target on a stdlib function that calls back into local code (io.ReadAll -> r.Read). | + | no |
| `chain_decl` | `middleware/middleware.go:@chain` | Negative: middleware.Chain returns a Handler, not an error. Expected: exit 2, "middleware.Chain has no error result". | - | no |

## Suspected bugs in short

| Queries | Expected | Actual |
|---|---|---|
| `maintenance_decl` | `sentinel dispatch.ErrMaintenance`: the closure returns a captured parameter of `failWith`, which the Target calls with that sentinel | `unresolved(depends-on-caller): parameter err (error)` and no Origin |
| `close_decl`, `recover_lit` (also visible in `hf_handle_decl`, `pipeline_decl`, `quota_call`) | every entry has a usable span | one of the two `unresolved(memory)` entries for the named result is printed with the span `-` |
| `fanout_static` | concrete `drivers.Status` at the conversion `Status(503)` | zero-width span on the `return` keyword |
| `ops_call`, `retrysms_call`, `selftest_decl`, `quota_call`, `alertuser_decl`, `retrydriver_decl` | Candidates narrowed by the argument the caller passes (S09) | all Candidates of the callee's parameter, `N of N candidates` |
| `quietly_decl`, `nohttp_decl` | empty set (the only possible error is filtered by the guard) | Origins of Candidates this caller never passes (S09) |
