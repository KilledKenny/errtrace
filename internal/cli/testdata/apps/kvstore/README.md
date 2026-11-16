# kvstore

A layered key-value / inventory service used as an errtrace test application.
Focus: **Error Origins and interprocedural propagation**.

    cmd/kvstore (main) -> api -> service -> repo -> engine (-> engine/internal/sys)
                                    \-> validate          config, kverr (shared errors)

## Packages

| Package | Role | Cases it provides |
|---|---|---|
| `kverr` | shared error vocabulary | exported / unexported sentinels, `var` block, pointer-typed sentinel (`ErrCorrupt`), sentinels of custom int/string types (`ErrQuota`, `ErrEmptyKey`), interface-typed sentinel holding a custom type (`ErrThrottled`), error constant (`CodeTooLarge`), concrete types with value receivers (`Code` int, `Reason` string, `FieldError` struct) and a pointer receiver with `Unwrap` (`*OpError`) |
| `engine` | in-memory storage | sentinel initialised then reassigned in `init` (`ErrClosed`), stdlib alias (`ErrEOF = io.EOF`), sentinel reassigned at run time (`ErrStale`), never-assigned sentinel (`ErrIgnored`), constant-false dead branches, a function without a body (`fsync`, linkname), returns from `switch`, `select`, `for`, `goto` and labeled `break`, callback parameter, struct-field and channel errors |
| `engine/internal/sys` | body of `engine.fsync` | linkname push, function unreachable by calls |
| `repo` | namespaced repository | re-exported alias (`ErrNotFound = kverr.ErrNotFound`), embedded struct method promotion (`base.Ping`), error result first (`Lookup`), two error results (`Swap`), concrete result type and typed nil (`find`/`Exists`/`Check`), closure callback, named results with bare return |
| `validate` | input validation | four Origin kinds in one function, `(error, int)`, two named error results, a function with no error result |
| `config` | command line | named results, opaque errors, `%v` that hides a stdlib error |
| `service` | business rules | mutual recursion (`resolve`/`follow`), direct recursion (`retry`), deferred closures wrapping / overwriting a named result, shadowed `err`, phi merges, errors through parameters, variadic error parameter, nil-only function |
| `api` | command dispatch | interface call with one Candidate, `errors.Is` / `==` guards over aliases, call sites inside multi-value returns |
| `cmd/kvstore` | `main` | reaches every layer |

A call-site Target must point at the callee name (`s.repo./*@m*/Store(`), so markers sit after the receiver.

## Queries

`+` = positive (origins expected in the set), `-` = negative (empty set, origin must be absent, or Target rejected).

| Query | Target (args) | Checks | +/- | Suspected bug |
|---|---|---|---|---|
| `api_backend_get_call` | `-chain-depth=2 api/api.go:@backend_get` | Call site through an interface (Backend.Get) with a single Candidate, (*service.Service).Get. | + | no |
| `api_clean_call` | `api/api.go:@clean_call` | Negative: call-site Target whose callee (validate.Clean) has no error result. | - | no |
| `api_close_call` | `api/api.go:@close_call` | SUSPECTED BUG (same as service_close_decl): call site of a function whose result is overwritten in a defer. | + | yes |
| `api_delete_call` | `api/api.go:@delete_call` | Call site in the middle of a multi-value return statement: return "ok", h.svc.Delete(key, 2). | + | no |
| `api_hfind_decl` | `api/api.go:@hfind` | SUSPECTED BUG (low confidence, same as repo_lookup_decl): 'possibly handled' notes claim engine.ErrClosed may equal kverr.ErrNotFound. | + | yes |
| `api_hget_decl` | `-chain-depth=2 api/api.go:@hget` | Interface call (1 Candidate: *service.Service) followed by errors.Is(err, kverr.ErrNotFound) => nil. | +/- | no |
| `api_list_decl` | `api/api.go:@list` | Named results plus `keys, err := ...` in the same scope (reuses err), error crossing api -> service -> repo -> engine -> closure in repo. | + | no |
| `asm_file` | `engine/stub.s:1:1` | Negative: Target inside a non-Go (assembly) file of a package. | - | no |
| `bad_file` | `engine/nosuch.go:1:1` | Negative: file that does not exist. | - | no |
| `bad_line` | `engine/engine.go:9999:1` | Negative: line past the end of the file. | - | no |
| `config_load_decl` | `config/config.go:@load` | Named results (cfg, err), an error assigned inside an if-init in a switch in a for loop. | +/- | no |
| `config_parselimit_call` | `config/config.go:@parselimit_call` | Call site inside a tuple assignment `cfg.Limit, err = parseLimit(val)` (one result stored to a field). | +/- | no |
| `engine_check_decl` | `engine/engine.go:@check` | SUSPECTED BUG: dead-branch origins propagate interprocedurally (see engine_verify_decl). | + | yes |
| `engine_compact_decl` | `engine/engine.go:@compact` | goto-based retry loop: err is reassigned on every iteration and compared with == before the goto. | + | no |
| `engine_dead_call` | `engine/engine.go:@dead_call` | Call site inside an `if paranoid` block whose condition is the constant false. | + | no |
| `engine_deepverify_decl` | `engine/engine.go:@deepverify` | Function only called from constant-false dead code. | +/- | no |
| `engine_delete_decl` | `engine/engine.go:@delete` | SUSPECTED BUG (low confidence): a never-assigned, always-nil package variable is listed as a sentinel. | + | yes |
| `engine_drain_decl` | `engine/engine.go:@drain` | SUSPECTED BUG (low confidence; plan S10 behaviour not present): an alias of a stdlib sentinel is reported under the alias name only. | +/- | yes |
| `engine_fsync_call` | `engine/engine.go:@fsync` | Call site of a function declared without a body (body pushed by go:linkname from engine/internal/sys). | + | no |
| `engine_fsync_decl` | `engine/engine.go:@fsync_decl` | SUSPECTED BUG: a declaration Target without a body yields '0 entries', i.e. claims the function cannot fail. | - | yes |
| `engine_get_decl` | `engine/engine.go:@get` | Method with (value, error) results and two early returns. | + | no |
| `engine_lasterr_decl` | `engine/engine.go:@lasterr` | Error loaded from a struct field (out of scope by design). | + | no |
| `engine_open_decl` | `engine/engine.go:@open` | Constructor returning (*Engine, error); the *Engine result is not an error. | + | no |
| `engine_put_decl` | `engine/engine.go:@put` | Phi/merge of several origins over early returns plus a body-less callee. | +/- | no |
| `engine_scan_decl` | `engine/engine.go:@scan` | Error stored in a local inside nested loops, left through a labeled break, then wrapped with %w. | +/- | no |
| `engine_snapshot_decl` | `engine/engine.go:@snapshot` | SUSPECTED BUG (low confidence; plan S10 behaviour not present): no hint that the sentinel is unstable. | +/- | yes |
| `engine_var_target` | `engine/engine.go:@errclosed` | Negative: the Target position is a package-level sentinel variable, not a function or call. | - | no |
| `engine_verify_decl` | `engine/engine.go:@verify` | SUSPECTED BUG: origins from constant-false dead branches are reported (low confidence: may be an accepted over-approximation, but go/types knows both conditions are constant false). | + | yes |
| `engine_wait_decl` | `engine/engine.go:@wait` | Returns from a select statement: a sentinel, an error received from a channel, and nil. | + | no |
| `kverr_internal_decl` | `kverr/kverr.go:@internal` | Unexported sentinel declared inside a var block, returned by an exported accessor. | +/- | no |
| `main_exit_call` | `cmd/kvstore/main.go:@exit_call` | Negative: call to os.Exit, a stdlib function without results. | - | no |
| `main_open_call` | `cmd/kvstore/main.go:@open_call` | Call site whose result is wrapped only after the call. | +/- | no |
| `main_pair_call` | `cmd/kvstore/main.go:@pair_call` | Call site in main of a function with two named error results. | + | no |
| `main_run_call` | `-chain-depth=1 cmd/kvstore/main.go:@run_call` | Top of the program: everything that can reach main's error, at -chain-depth=1 to keep the output bounded. | +/- | no |
| `repo_call_receiver` | `repo/repo.go:@recv` | Negative: position on the receiver expression (field eng) of a method call instead of the method name. | - | no |
| `repo_check_decl` | `repo/repo.go:@check` | Correct variant of Exists: the *OpError is nil-checked before it is converted. | +/- | no |
| `repo_exists_decl` | `repo/repo.go:@exists` | SUSPECTED BUG: the typed-nil Origin is missing. | + | yes |
| `repo_find_decl` | `repo/repo.go:@find` | Concrete result type (*kverr.OpError, not the error interface). | + | no |
| `repo_get_call` | `repo/repo.go:@get_call` | Call site r.eng.Get(...) through a promoted field (r.base.eng). | + | no |
| `repo_has_decl` | `repo/repo.go:@has` | SUSPECTED BUG (low/medium confidence): one sentinel value is listed twice under two names. | + | yes |
| `repo_keys_decl` | `repo/repo.go:@keys` | Closure passed as a callback down into the engine and its error coming back up. | + | no |
| `repo_load_decl` | `repo/repo.go:@load` | Wraps the engine error with %w. | + | no |
| `repo_lookup_decl` | `repo/repo.go:@lookup` | SUSPECTED BUG (low confidence): spurious 'possibly handled' note on engine.ErrClosed. | + | yes |
| `repo_ping_decl` | `repo/repo.go:@ping` | Method declared on an embedded struct (base), promoted to Repo. | + | no |
| `repo_remove_decl` | `repo/repo.go:@remove` | SUSPECTED BUG (low confidence, same as engine_delete_decl): the always-nil engine.ErrIgnored is propagated across packages as a sentinel. | + | yes |
| `repo_scan_call` | `repo/repo.go:@scan_call` | Call site with a function-literal argument: the dynamic call in Scan is narrowed to this closure. | +/- | no |
| `repo_store_decl` | `repo/repo.go:@store` | Concrete pointer-receiver wrapper type with Unwrap built around a callee's error. | + | no |
| `repo_swap_decl` | `repo/repo.go:@swap` | Two named error results, both redeclared with := in the same scope, and early returns with nil in either slot. | + | no |
| `repo_tail_decl` | `repo/repo.go:@tail` | Named results with a bare return; the aliased sentinel is compared with == and replaced by nil. | - | no |
| `service_annotate_decl` | `service/service.go:@annotate` | Error passed in through a parameter, nil-checked, wrapped and returned. | + | no |
| `service_close_decl` | `service/service.go:@close` | SUSPECTED BUG (known gap? plan S08): deferred closure that unconditionally overwrites the named result. | +/- | yes |
| `service_copy_decl` | `service/service.go:@copy` | Fixed variant of Move: the outer err receives the Load error or, when Load succeeded, the Store error. | + | no |
| `service_delete_decl` | `service/service.go:@delete` | Calls a directly recursive helper whose callee error is only compared with nil and never returned. | +/- | no |
| `service_exchange_decl` | `service/service.go:@exchange` | Caller of a function with two error results, returning one or the other. | + | no |
| `service_firsterr_call` | `service/service.go:@firsterr_call` | Call site passing three errors through a variadic parameter. | + | no |
| `service_firsterr_decl` | `service/service.go:@firsterr` | Variadic error parameter ranged over and returned. | + | no |
| `service_follow_decl` | `-chain-depth=2 service/service.go:@follow` | Declaration of the second member of the mutual recursion. | +/- | no |
| `service_fsck_decl` | `service/service.go:@fsck` | SUSPECTED BUG: dead-branch origins reach a Target 4 packages up (see engine_verify_decl). | +/- | yes |
| `service_get_decl` | `-chain-depth=3 service/service.go:@get` | Long chain service -> validate / repo -> engine, with mutual recursion (resolve <-> follow) that wraps on every round. | + | no |
| `service_health_decl` | `service/service.go:@health` | Switch assigning one local from four sources (phi), then passed through a wrapping helper. | +/- | no |
| `service_lookup_call` | `service/service.go:@lookup_call` | Call-site Target whose callee returns the error first: `err, v := s.repo.Lookup(key)`. | + | no |
| `service_move_decl` | `service/service.go:@move` | Shadowing: `if v, err := ...` declares a new err; the outer err that is returned is never assigned. | - | no |
| `service_pick_call` | `service/service.go:@pick_call` | Two error arguments, either of which may be returned (param passthrough of a call result and of a pointer sentinel). | +/- | no |
| `service_ping_call` | `service/service.go:@ping_call` | Call site of the promoted method s.repo.Ping() (selector through an embedded struct, implicit &r.base). | + | no |
| `service_put_decl` | `service/service.go:@put` | SUSPECTED BUG (known gap? plan S08): a named result modified by a deferred closure is not followed. | +/- | yes |
| `service_resolve_call` | `-chain-depth=2 service/service.go:@resolve_call` | Call site entering the mutually recursive pair resolve/follow. | + | no |
| `service_retry_call` | `service/service.go:@retry_call` | Recursive call site inside the recursive function itself. | + | no |
| `service_stats_decl` | `service/service.go:@stats` | Nil-only function: both returns have a literal nil error, although a callee error is inspected. | - | no |
| `service_store_call` | `service/service.go:@store_call` | Call site in a function with a deferred wrapper: the call-site Error Set is that of the call, not of the function. | + | no |
| `service_swap_call` | `service/service.go:@swap_call` | Call-site Target whose callee has two error results. | + | no |
| `sys_fsync_decl` | `engine/internal/sys/sys.go:@fsync` | The linkname-pushed implementation of engine.fsync: no Go caller, so not reachable from main by calls. | +/- | no |
| `validate_clean_decl` | `validate/validate.go:@clean` | Negative: declaration Target without any error result. | - | no |
| `validate_key_decl` | `validate/validate.go:@key` | SUSPECTED BUG (span only): constants converted to error get a zero-width span at the `return` keyword. | + | yes |
| `validate_pair_decl` | `validate/validate.go:@pair` | Two named error results (keyErr, valErr), the second assigned from the first result of a tuple call. | + | no |
| `validate_value_decl` | `validate/validate.go:@value` | Error result in first position: (error, int). | + | no |

## Suspected bugs (summary)

1. **Dead branches** (`engine_verify_decl`, `engine_check_decl`, `service_fsck_decl`): origins behind `if false` / `if constFalse` are reported and propagate upwards.
2. **Body-less declaration Target** (`engine_fsync_decl`): `0 entries` instead of `unresolved(no-body)`.
3. **Typed nil through a concrete result** (`repo_exists_decl`): `return r.find(key)` (`*OpError` to `error`) shows no `[typed nil]` entry.
4. **Deferred closures on named results** (`service_put_decl`, `service_close_decl`, `api_close_call`): only `unresolved(memory)` entries, all origins lost, one entry with the position `-:`.
5. **Zero-width spans for constant conversions** (`validate_key_decl`, also visible in `engine_verify_decl`): `N:3-3` at the `return` keyword.
6. **Aliases** (`engine_drain_decl`, `repo_has_decl`): an alias is shown under its own name, and one value can appear twice.
7. **Unstable sentinel not marked** (`engine_snapshot_decl`).
8. **Always-nil sentinel listed** (`engine_delete_decl`, `repo_remove_decl`).
9. **Init-reassigned sentinel "may be the same value as" another sentinel** (`repo_lookup_decl`, `api_hfind_decl`).

Items 4, 6 and 7 correspond to plan steps (S08, S10) that may simply not be implemented yet.

## Running

    go test ./internal/cli -run 'TestApps/kvstore/'
