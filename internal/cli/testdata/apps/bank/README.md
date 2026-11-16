# bank

A banking / ledger backend used as a realistic integration test application
for errtrace. It is deliberately written like production code: errors cross
five or six packages, get wrapped, joined, guarded, translated and swallowed
on the way. Almost every Query involves at least two of: wraps, guards,
dynamic dispatch, generics, closures/defer, stdlib, cross-package chains.

There are two main packages, so everything below is reachable from a root:

- `cmd/server` reads requests from stdin (`PATH TOKEN BODY`), serves them
  through `api.Server` (file-backed or in-memory store, audit file or stderr; the paths `local`,
  `validate` and `import` call the server in process, without middleware)
  and writes one status line per request.
- `cmd/cli` runs single operations (`transfer`, `batch`, `preview`,
  `explain`, `balance`, `owner`, `settled`, `freeze`, `check`, `ping`,
  `replay`) against an in-memory bank.

## Packages

| Package | Role | Error features |
|---|---|---|
| `domain` | accounts, transfers, error vocabulary | sentinels, `*ValidationError`, value type `InsufficientFundsError`, generic `NotFoundError[ID]` with a custom `Is` |
| `limits` | spending policy | `*Exceeded` whose `Unwrap` returns a sentinel |
| `parse` | strings / JSON to domain values | `strconv`, `time`, `encoding/json`, `errors.Is` / `errors.As` replacement, `%w` vs `%v` |
| `config` | key=value config file | `os.Open` + `fs.ErrNotExist` guard, `bufio`, `errors.Join` |
| `repo` | storage interfaces (`Store`, `Tx`) | interface dispatch |
| `repo/memory` | in-memory store | generic `lookup[K, V]`, `*retry.TimeoutError`, `ErrClosed` |
| `repo/filerepo` | file-backed store | `os` errors, `*CorruptError` (field `Unwrap`), named result + `errors.Join`, guards with `\|\|` |
| `tx` | transaction manager | `WithTx(func(tx) error)`, defer on a named result, rollback error joined, `Ping` |
| `retry` | retry helper | `errors.Is` / `errors.As` on transient errors, double `%w`, generic `Value[T]`, context cancellation |
| `rules` | rules engine | 10 implementations of `Rule` (fanout), `*Violation` wrapper, func adapter, join over a slice |
| `events` | event bus | dynamic handlers from a map, join in a loop, swallow-all `Notify` |
| `audit` | audit log | `io.Writer` / `io.Closer` dispatch, `Quota` writer |
| `middleware` | handler chain | closures returning closures, `recover`, error mapping |
| `service` | use cases | everything above combined; guards over large sets |
| `api` | transport layer | `Translate`: every error becomes an `*api.Error` |
| `cmd/server`, `cmd/cli` | mains | defer + named result, event handlers, call sites with concrete arguments |

## What the suspected bugs have in common

The result sections record what errtrace prints today. The 19 queries
marked `SUSPECTED BUG:` fall into a few groups (the query comment has the
details):

1. **Named result changed by a deferred closure** (`tx_withtx_decl`,
   `server_run_decl`, and through them `service_transfer_decl`,
   `service_batch_call`, `api_transfer_decl`): the whole Error Set collapses
   into `unresolved(memory): load of escaping variable err`, including the
   values stored before the defer runs; one entry is printed with the
   position `-`. Plan step S08 is not implemented in this build.
2. **No narrowing of Candidates by the arguments of a call**
   (`rules_explain_funds_call`, `rules_explain_limit_call`,
   `audit_write_buf_call`, `retry_busy_call`, `retry_value_decl`,
   `middleware_guarded_call`, and the closures of `os.ignoringEINTR` in
   `server_openstore_call`): a call-site Query reports the same Candidates as
   the declaration Query. Plan step S09 is not implemented in this build.
3. **`errors.Join` / multi-`%w` values are multiplied, and the cap of 1024
   counts combinations** (`service_transfer_decl`, `service_setstatus_decl`,
   `service_batch_call`, `cli_run_decl`, `api_transfer_decl`): a Join of two
   medium-sized sets ends in `unresolved(fixpoint-limit)` and an arbitrary
   prefix of the set; in `cli_run_decl` ten of eleven commands disappear.
   `events_publish_decl` (missing `[chain truncated]` variants at the default
   depth) probably has the same cause.
4. **Function values are resolved context-insensitively** (`api_serve_decl`,
   `server_respond_call`, `middleware_guarded_call`): every `Handler` is a
   Candidate of every `Handler` call, the middleware chain becomes a cycle,
   hits the cap of group 3, and the errors `Serve` really returns are cut.
5. **`errors.Unwrap` is resolved by Candidates** (`parse_unmarshal_call`):
   `json.Unmarshal` is reported to return `domain.ErrLimitExceeded`, the
   result of the app's `(*limits.Exceeded).Unwrap`.
6. **Smaller ones**: `internal/strconv.Error` reported for `strconv.ParseInt`
   / `Atoi` (`parse_amount_decl`), an origin printed without a position
   (`server_openstore_call`).

Sound over-approximations and v1 limits that are documented in the plan and
therefore *not* marked as bugs:

- guards are applied by dominance, so the merged side of a disjunction is
  not filtered: the body of `err == nil || err == X` (`filerepo_commit_decl`)
  and the fall-through of `!errors.Is(..) && !errors.As(..)`
  (`tx_ping_decl`; the same shape in `retry.Do` and `os.(*File).wrapErr`
  adds variants to many other sets);
- may-match values (custom `Is` methods such as `syscall.Errno`'s,
  Unresolved values) are kept under `errors.Is`, also behind
  `(*api.Error).Unwrap` in the translation layer (`api_balance_decl`,
  `api_import_decl`, `service_exists_decl`);
- `Unwrap` methods that do not return a field (`limits_check_decl`), slices
  passed to `errors.Join` (`rules_all_decl`), field loads
  (`cli_settled_decl`, `retry_sleep_decl`);
- group lines show only label and Candidate, so entries that differ in
  their chain look identical (`server_openstore_call`).

## Queries

78 queries; `go test ./internal/cli -run 'TestApps/bank/'`.

| Query | Target (args) | Checks | Kind | Suspected bug |
|---|---|---|---|---|
| `api_balance_decl` | `api/api.go:@balance` | Translate over the read path: only may-match values stay behind `(*api.Error).Unwrap` | negative | no |
| `api_handle_health_decl` | `api/api.go:@handle_health` | handler that logs and swallows everything: 0 entries | negative | no |
| `api_import_decl` | `api/api.go:@import` | translation layer where `ErrDuplicate` visibly keeps its cause; validation errors do not | positive + negative | no |
| `api_serve_decl` | `-fanout=2 api/api.go:@serve` | middleware chain from a map of handlers; only translated errors may leave | positive + negative | yes |
| `api_transfer_decl` | `api/api.go:@transfer` | translation layer end to end; only `ErrDuplicate` keeps its cause | positive + negative | yes |
| `api_translate_decl` | `api/api.go:@translate` | translation function alone: fresh `*api.Error`, sentinel, parameter behind custom Unwrap | positive | no |
| `api_validate_decl` | `api/api.go:@validate` | Translate over pure parse errors: no origin behind `(*api.Error).Unwrap` | negative | no |
| `audit_close_decl` | `audit/audit.go:@close` | `io.Closer` in a field, flag guard, `*os.File` close errors | positive | no |
| `audit_record_call` | `service/service.go:@record_call` | a large error set passed as an argument that is only logged must not leak | negative | no |
| `audit_record_decl` | `audit/audit.go:@record` | error parameter that is only formatted: no depends-on-caller entry; writer in a field | positive + negative | no |
| `audit_write_buf_call` | `cmd/cli/main.go:@write_buf_call` | call site with a `*bytes.Buffer` argument narrows `w.Write` to one Candidate | negative | yes |
| `audit_write_decl` | `audit/audit.go:@write` | `io.Writer` dispatch (3 Candidates) + `%w` wrap | positive | no |
| `cli_run_decl` | `-fanout=2 cmd/cli/main.go:@run` | top-down: the CLI entry point, a switch over 11 use cases | positive | yes |
| `cli_settled_decl` | `cmd/cli/main.go:@settled` | `errors.As`, then re-wrapping a field of the matched error | positive | no |
| `config_load_decl` | `config/config.go:@load` | `os.Open` + `errors.Is` guard, `%w` vs `%v`, joined validation errors, `defer f.Close()` | positive + negative | no |
| `config_set_call` | `config/config.go:@set_call` | method call site inside a loop: callee set without the caller's wrap | positive | no |
| `config_validate_decl` | `config/config.go:@validate` | `errors.Join` of two nil-able errors | positive | no |
| `domain_debit_decl` | `domain/domain.go:@debit` | leaf: wrapped sentinels + value-type error | positive | no |
| `domain_validate_decl` | `domain/domain.go:@validate` | leaf with a value receiver: 3 concrete values + 1 sentinel | positive | no |
| `events_first_call` | `events/events.go:@first_call` | dynamic call of a handler loaded from a map: 3 Candidates | positive | no |
| `events_notify_decl` | `events/events.go:@notify` | best-effort notify: logs and returns nil, 0 entries | negative | no |
| `events_publish_chain_depth3` | `-chain-depth=3 events/events.go:@publish` | same with `-chain-depth=3`: consistent truncation | positive | no |
| `events_publish_decl` | `events/events.go:@publish` | handlers wrapped and joined in a loop; chain variants up to the depth limit | positive | yes |
| `filerepo_append_decl` | `repo/filerepo/filerepo.go:@append` | named result, `errors.Join(werr, f.Close())`, wrapped `OpenFile` errors | positive | no |
| `filerepo_commit_decl` | `-fanout=0 repo/filerepo/filerepo.go:@commit` | `err == nil \|\| err == ErrTxDone` early return, otherwise wrap; dominance over-approximation | positive + negative | no |
| `filerepo_decode_call` | `repo/filerepo/filerepo.go:@decode_call` | call site: `strconv` chains without the caller's `*CorruptError` wrapper | positive + negative | no |
| `filerepo_get_decl` | `repo/filerepo/filerepo.go:@get` | `os.ReadFile` + `fs.ErrNotExist` guard -> generic `NotFoundError`, custom wrapper type | positive + negative | no |
| `filerepo_rollback_decl` | `repo/filerepo/filerepo.go:@rollback` | `errors.Is \|\| errors.Is` guard returning nil: `ErrTxDone` removed | negative | no |
| `flag_chain_depth_zero` | `-chain-depth=0 domain/domain.go:@debit` | `-chain-depth=0` is rejected with usage, exit 2 | negative | no |
| `limits_check_decl` | `limits/limits.go:@check` | custom `Unwrap` returning a sentinel (not a field): unresolved by design | positive | no |
| `memory_get_lookup_call` | `repo/memory/memory.go:@lookup_call` | call site of a generic function: one instance only | positive + negative | no |
| `memory_lookup_decl` | `repo/memory/memory.go:@lookup` | generic function + generic error type: two instances | positive | no |
| `middleware_auth_literal_target` | `-fanout=2 middleware/middleware.go:@auth_fn` | Target on a function literal (closure returned by `Auth`) | positive | no |
| `middleware_chain_no_error` | `middleware/middleware.go:@chain` | Target without an error result: exit 2 | negative | no |
| `middleware_guarded_call` | `-fanout=1 cmd/cli/main.go:@guarded_ok_call` | curried closures `Auth(token)(h)(ctx, req)` with a nil-returning `h` | negative | yes |
| `parse_amount_decl` | `parse/parse.go:@amount` | `strconv.ParseInt` + `errors.Is(ErrRange)` replaced by `*ValidationError` | positive + negative | yes |
| `parse_date_decl` | `parse/parse.go:@date` | errors dropped in a loop, fresh error afterwards: 1 entry | negative | no |
| `parse_line_decl` | `parse/parse.go:@line` | cross-function `%w` chains of 2-3 links | positive | no |
| `parse_line_when_call` | `parse/parse.go:@when_call` | call site: callee chain without the caller's wrap | positive | no |
| `parse_request_decl` | `parse/parse.go:@request` | JSON errors translated by `errors.As`, flattened with `%v`: no json origin | positive + negative | no |
| `parse_unmarshal_call` | `parse/parse.go:@unmarshal_call` | call site of `json.Unmarshal`: stdlib origins, and a domain sentinel that cannot come from it | positive + negative | yes |
| `parse_when_decl` | `parse/parse.go:@when` | `errors.As` guard that removes all but one origin | negative | no |
| `retry_busy_call` | `-fanout=1 cmd/cli/main.go:@retry_busy_call` | call site of `retry.Do` with a local closure; Is/As guards, double `%w` | positive + negative | yes |
| `retry_sleep_decl` | `retry/retry.go:@sleep` | `ctx.Err()` wrapped; context internals are unresolved by design | positive | no |
| `retry_value_decl` | `-fanout=1 retry/retry.go:@value` | generic `Value[T]` over a closure passed on to `Do` | positive + negative | yes |
| `rules_advisory_decl` | `rules/rules.go:@advisory` | `errors.Is(ErrLimitExceeded)` guard over the 10-rule fanout | positive | no |
| `rules_all_decl` | `rules/rules.go:@all` | `errors.Join(errs...)` over a slice: unresolved by design | positive | no |
| `rules_check_call_fanout10` | `-fanout=10 rules/rules.go:@check_call` | `-fanout=10` with exactly 10 Candidates: grouped | positive | no |
| `rules_check_call_fanout11` | `-fanout=11 rules/rules.go:@check_call` | `-fanout=11`: same entries, not grouped | positive | no |
| `rules_evaluate_decl` | `rules/rules.go:@evaluate` | 10 implementations of `Rule`, default grouping, `&Violation{Err: err}` | positive | no |
| `rules_evaluate_fanout0` | `-fanout=0 rules/rules.go:@evaluate` | same with `-fanout=0`: chains via `(*Violation).Unwrap` | positive | no |
| `rules_explain_decl` | `rules/rules.go:@explain` | interface parameter with 2 Candidates across callers | positive + negative | no |
| `rules_explain_funds_call` | `cmd/cli/main.go:@explain_funds_call` | call site with `rules.Funds{}` narrows to one Candidate | negative | yes |
| `rules_explain_limit_call` | `cmd/cli/main.go:@explain_limit_call` | call site with `rules.Limit{...}`: 3-link chain, no Funds origin | positive + negative | yes |
| `rules_explain_limit_chain_depth1` | `-chain-depth=1 cmd/cli/main.go:@explain_limit_call` | same call site, `-chain-depth=1` | positive | no |
| `rules_explain_limit_chain_depth2` | `-chain-depth=2 cmd/cli/main.go:@explain_limit_call` | same call site, `-chain-depth=2` | positive | no |
| `rules_func_check_decl` | `rules/builtin.go:@func_check` | func value in a struct field called through an adapter method | positive | no |
| `server_openstore_call` | `cmd/server/main.go:@openstore_call` | interface result from two concrete stores; only one can fail | positive + negative | yes |
| `server_respond_call` | `-fanout=2 cmd/server/main.go:@respond_call` | `errors.As(*api.Error)` guard applied to `srv.Serve`'s set at a call site | negative | yes |
| `server_respond_decl` | `cmd/server/main.go:@respond` | same function as declaration: parameter is depends-on-caller | positive | no |
| `server_respond_local_call` | `cmd/server/main.go:@respond_local_call` | guard removes everything `srv.Transfer` returns: 0 entries | negative | no |
| `server_respond_validate_call` | `cmd/server/main.go:@respond_validate_call` | guard removes everything `srv.Validate` returns: 0 entries | negative | no |
| `server_run_decl` | `cmd/server/main.go:@run` | end to end: deferred closure joins `logger.Close()` onto the named result | positive | yes |
| `service_batch_call` | `-fanout=2 cmd/cli/main.go:@svc_batch_call` | loop with `errors.Is` skip / return / join over `Transfer`'s set | positive + negative | yes |
| `service_canpay_decl` | `service/service.go:@canpay` | `errors.As` guard leaves exactly one origin | negative | no |
| `service_execute_decl` | `-fanout=2 service/service.go:@execute` | transaction body: stores, rules, domain methods, wraps (mid layer) | positive | no |
| `service_exists_decl` | `-fanout=2 service/service.go:@exists` | `errors.Is` matched through a custom `Is` method: possibly handled | positive | no |
| `service_notify_call` | `service/service.go:@notify_call` | swallow-all callee at a call site: 0 entries | negative | no |
| `service_preview_call` | `-fanout=2 cmd/cli/main.go:@preview_call` | call site in cmd/cli: `input()` wraps + `Advisory` | positive | no |
| `service_save_decl` | `service/service.go:@save` | variadic helper wrapping a dynamic `Put` in a loop | positive | no |
| `service_setstatus_decl` | `-fanout=2 service/service.go:@setstatus` | Get + Put + joined event handlers | positive | yes |
| `service_settled_decl` | `service/service.go:@settled` | type assertion to an anonymous interface + generic instance | positive + negative | no |
| `service_transfer_decl` | `-fanout=2 service/service.go:@transfer` | end to end: retry over transaction over closure, audit join, swallowed notify | positive + negative | yes |
| `tx_begin_call` | `tx/tx.go:@begin_call` | dynamic `store.Begin()` call site: 2 Candidates, no caller wrap | positive | no |
| `tx_ping_decl` | `tx/tx.go:@ping` | `!errors.Is && !errors.As` early return (exact), otherwise wrap (over-approximated) | positive + negative | no |
| `tx_readonly_decl` | `-fanout=1 tx/tx.go:@readonly` | callback (1 Candidate) + dynamic Begin/Rollback | positive | no |
| `tx_rollback_call` | `tx/tx.go:@rollback_call` | dynamic `t.Rollback()`: one Candidate filters with guards | positive + negative | no |
| `tx_withtx_decl` | `-fanout=2 tx/tx.go:@withtx` | callback transaction; defer joins rollback error / wraps commit error on the named result | positive | yes |
