# Suspected bugs found by the test applications

Each finding below was raised while building an application and then confirmed by an
independent review of the Go code (some with a runtime replica). Confidence is the
reviewer's. Several are known gaps of unimplemented plan steps (S08 defers, S09 call-site
narrowing, S10 sentinel labels) rather than regressions. The named query records today's
output and its comment describes the correct one; other queries showing the same issue
are flagged `SUSPECTED BUG` too.

## kvstore

### Declaration Target without a body yields an empty Error Set

- Query: `kvstore/queries/engine_fsync_decl.txtar` (confidence: high)
- Expected: engine.fsync is declared without a body (go:linkname push from engine/internal/sys). A declaration Query should give 1 entry unresolved(no-body) or a diagnostic, as the call-site Query engine_fsync_call does.
- Actual: `Error Set of example.com/kvstore/engine.fsync: 0 entries`, exit 0, no warning, which reads as 'cannot fail'.

### Typed-nil Origin missing when a concrete *T call result is returned as error

- Query: `kvstore/queries/repo_exists_decl.txtar` (confidence: high)
- Expected: `func (r *Repo) Exists(key string) error { return r.find(key) }`, where find returns *kverr.OpError and has a `return nil` path: find's 3 wrapped entries plus `concrete *kverr.OpError [typed nil]`. A scratch replica confirmed the result is non-nil at run time.
- Actual: The same 3 entries as the nil-checked variant Check (repo_check_decl.txtar); no typed-nil entry.

### Pointer that is nil on one path and allocated on the other loses its typed-nil Origin and its Unwrap chain

- Query: `kvstore/queries/repo_probe_decl.txtar` (confidence: medium)
- Expected: `var oe *kverr.OpError; if key == "" { oe = &kverr.OpError{Err: kverr.ErrEmptyKey} }; return oe` should give 2 entries: sentinel kverr.ErrEmptyKey under *kverr.OpError.Unwrap, and concrete *kverr.OpError [typed nil]. The direct form `var p *T; return p` is reported correctly (e2e origins/typed_nil).
- Actual: 1 bare `concrete *kverr.OpError` entry: no [typed nil], and kverr.ErrEmptyKey is dropped with no Unresolved entry, although errors.Is(err, ErrEmptyKey) holds at run time. Inherited by service_probe_decl.txtar and both main_run queries.

### Origins wrapped in a concrete error are lost when it passes through a concrete-typed receiver and is converted to error in the callee

- Query: `kvstore/queries/repo_stat_decl.txtar` (confidence: medium)
- Expected: `return r.find(key).OrNil()` (OrNil returns its non-nil receiver as error) should give find's 3 entries, kverr.ErrEmptyKey, engine.ErrClosed and kverr.ErrNotFound, each under *kverr.OpError.Unwrap.
- Actual: 1 bare `concrete *kverr.OpError` positioned inside OrNil (kverr.go:75:5-6, the `e` of `e == nil`, not `return e`): no chain and 0 unresolved. In a scratch replica a later `errors.Is(err, ErrA)` guard on such a value left the entry unchanged with no 'possibly handled' note, although the guard does return nil at run time. Also flagged in kverr_ornil_decl.txtar (low confidence there) and inherited by service_probe_decl.txtar.

### Named result captured by a deferred closure: only unresolved(memory), one entry with no position ('-:')

- Query: `kvstore/queries/service_put_decl.txtar` (confidence: medium)
- Expected: Put's defer wraps a non-nil err with "put %s: %w": every origin (kverr.ErrReadOnly, validate.Key's 4, Value's Code, Store's 4) should appear only under that wrap. service_close_decl.txtar and api_close_call.txtar (defer does `err = s.repo.Flush()`) should give engine.errTorn and engine.ErrClosed and not service.errGaveUp.
- Actual: 5 (Put) or 2 (Close) `unresolved(memory): load of escaping variable err` entries and no origins. One entry per function prints as `-: unresolved(memory)...` with the path hop `-: in (*service.Service).Put`; the others have zero-width spans. Plan step S08 is not implemented, so the missing origins are a known gap; the '-:' span is a rendering defect regardless. A defer that does not capture err works (service_sync_decl.txtar).

### Origins in constant-false dead branches are reported and propagated

- Query: `kvstore/queries/engine_verify_decl.txtar` (confidence: low)
- Expected: verify has `if paranoid { return e.deepVerify(key) }` with `const paranoid = false`, and `if false { return errTorn }`. Only `concrete kverr.Code` is reachable.
- Actual: 3 entries: also sentinel kverr.ErrCorrupt and sentinel engine.errTorn. They propagate to engine_check_decl.txtar, service_fsck_decl.txtar and the main_run queries. Sound over-approximation, not documented as scope.

### Zero-width span at the return keyword for constants converted to error

- Query: `kvstore/queries/validate_key_decl.txtar` (confidence: low)
- Expected: The Origin span of `return kverr.Reason("kv: reserved key")` and `return kverr.CodeTooLarge` should be the expression (plan: 'for a Concrete Type, the expression that builds it').
- Actual: `validate.go:19:3-3` and `22:3-3`: zero-width at the start of the return statement; same in engine.go (85:3-3, 104:3-3). Non-constant conversions get a proper span (30:10-37). The plan's fallback to the enclosing Return's position partly explains it.

### One sentinel value listed twice under an alias and its original name; aliases never name their source

- Query: `kvstore/queries/repo_has_decl.txtar` (confidence: low)
- Expected: `var ErrNotFound = kverr.ErrNotFound` in repo; Has should give 2 entries: engine.ErrClosed and one not-found entry (plan S10: `sentinel kverr.ErrNotFound (via repo.ErrNotFound)`).
- Actual: 3 entries, with repo.ErrNotFound and kverr.ErrNotFound as separate origins, although guards treat them as one value (api_hfind_decl.txtar). engine_drain_decl.txtar likewise prints `sentinel engine.ErrEOF` for `var ErrEOF = io.EOF` with no mention of io.EOF. Plan S10 is not implemented.

### Sentinel reassigned at run time is not marked unstable in the output

- Query: `kvstore/queries/engine_snapshot_decl.txtar` (confidence: low)
- Expected: ErrStale is reassigned by `func SetStaleError(err error)`. Plan S10 calls for `sentinel engine.ErrStale [mutable global: assigned at ...]`.
- Actual: A plain `sentinel example.com/kvstore/engine.ErrStale` entry. The guard logic does track instability (see repo_alive_decl.txtar); only the entry label lacks the mark. S10 is not implemented.

### Never-assigned (always nil) package error variable reported as a sentinel

- Query: `kvstore/queries/engine_delete_decl.txtar` (confidence: low)
- Expected: `ErrIgnored error` is never assigned anywhere, so `return ErrIgnored` returns nil: 2 entries, engine.ErrClosed and kverr.ErrEmptyKey.
- Actual: 3 entries including `sentinel example.com/kvstore/engine.ErrIgnored`; it also propagates to repo_remove_decl.txtar.

Coverage gaps:

- Deferred named-result cases (wrap in defer, overwrite in defer) only produce unresolved(memory) today, so LIFO ordering and guard filtering inside defers could not be checked; only the no-effect defer (service_sync_decl) and a deferred call site (main_deferred_call) give real sets.
- Variadic error parameters end in unresolved(memory) for the slice element (documented scope), so per-argument origins at the firstErr call site are not checked; this also hides the typed nil from repo.Exists at that site.
- Function without a body uses a go:linkname push plus an empty stub.s rather than real assembly; linkname is not followed, so sys.errShortWrite never shows at engine.fsync call sites.
- Flag coverage is limited to -chain-depth (1, 2, 3); -p, -tags, -fanout, -debug and -json are left to the e2e fixtures and sibling apps.
- The interplay of guards with the receiver-passthrough loss (errors.Is on the result of repo.Stat) was confirmed only in a scratch program, not recorded as an app query.
- The brief's 'sentinels reassigned in init' case is covered, but only through a user func init(); errtrace treats that as unstable, so no query shows an init-reassigned sentinel being removed by a guard.
- Several markers remain unused (api handle/hget_call/shutdown, service resolve/retry/audit/verify/list/find/follow_call/annotate_call, config parselimit, main handle_call); they duplicate existing coverage and were left without queries to keep the count down.
- Dead-branch handling was not checked against the SSA form: the queries record that origins behind constant-false conditions are reported, but not why.

## wrapchain

### fmt.Errorf %w with a maybe-nil operand: the path where the operand is nil (still a non-nil error) is dropped

- Query: `wrapchain/queries/errorf_nil_operand.txtar` (confidence: high)
- Expected: parse.Tag does `var cause error; if strict { cause = doc.ErrSyntax }; return fmt.Errorf("tag: %w", cause)`. Expected 2 entries: doc.ErrSyntax under fmt.Errorf "tag: %w", and an entry for the wrap that holds nothing (fmt.Errorf returns a non-nil *fmt.wrapError with a nil err; confirmed at run time). The tool prints exactly such an entry, `opaque fmt.Errorf "footer %q: %w"`, when the operand is always nil (errorf_always_nil_operand).
- Actual: 1 entry: only doc.ErrSyntax. The tool tracks that this Wrap may hold nil (errset.Value.MayHoldNil, used to weaken errors.Is) but emits no entry for the wrap itself, so the Error Set misses a path on which the function fails.

### Custom wrapper built unconditionally around a maybe-nil error: the nil-inner wrapper is dropped

- Query: `wrapchain/queries/wrapper_maybe_nil_inner.txtar` (confidence: high)
- Expected: transform.Always does `return &xerr.OpError{Op: "always", Err: upper(d)}`, which is never nil. Expected 2 entries: doc.ErrNoTitle with chain *xerr.OpError.Unwrap, and `concrete *xerr.OpError` for the case where upper returns nil. wrapper_nil_inner (`&xerr.OpError{Op: "bare"}`) does print `concrete *xerr.OpError`.
- Actual: 1 entry: doc.ErrNoTitle under *xerr.OpError.Unwrap only. Same root cause as errorf_nil_operand: a Wrap whose inner value may be nil yields entries only for its non-nil leaves.

### errors.Is cannot remove an origin under a Wrap of an unguarded call result, even when the callee never returns nil (also is_depth10)

- Query: `wrapchain/queries/is_unguarded_call_wrap.txtar` (confidence: low)
- Expected: export.SyncLenient does `err := fsync(); if errors.Is(err, doc.ErrDisk) { return nil }; return err`, where fsync is `fmt.Errorf("fsync: %w", commit())` and commit always returns doc.ErrDisk. Is always matches, so 0 entries. Likewise is_depth10 (the same guard on the 10-Wrap ladder with -chain-depth=10) should give 0 entries.
- Actual: 1 entry, 1 possibly handled: doc.ErrDisk under fmt.Errorf "fsync: %w" with `possibly handled: errors.Is with doc.ErrDisk: wrapped value may be nil`. Whether a call result may be nil is decided inside the calling function only (flow.mayBeNil treats any non-intrinsic call as maybe nil), not from the callee's results. The entry is marked, not silently wrong, so this is a precision gap; is_through_custom_unwrap, where the helper has an `err == nil` check, correctly gives 0 entries.

### Deferred closure joining into a named result: two Unresolved entries for one variable, one with no span ('-') and one zero-width

- Query: `wrapchain/queries/defer_join.txtar` (confidence: medium)
- Expected: export.Flush does `defer func() { err = errors.Join(err, s.Close()) }(); return s.Write(d)`. Changes made by deferred calls are not modelled yet (plan S08, e2e origins/named_result_defer), so an Unresolved entry for err is acceptable, but as one entry with a real source span.
- Actual: `2 entries, 2 unresolved`: `-: unresolved(memory): load of escaping variable err` with the path line `-: in example.com/wrapchain/export.Flush` (no position at all), and `export/export.go:65:2-2: unresolved(memory): load of escaping variable err` (zero-width span). The missing origins are a known unimplemented step; the positionless, duplicated entry is the defect.

Coverage gaps:

- %w with a literal nil operand and %w with a non-error or out-of-range operand under a constant format are not written directly, because go vet rejects them; they are covered through a nil error variable and through formats held in a local variable (errorf_nil_operand, errorf_always_nil_operand, errorf_non_error_operand, errorf_bad_index).
- errors.Unwrap is not modelled as an intrinsic, so the four unwrap_* queries all record the same two unresolved(memory) entries; I reclassified them from suspected bug to by-design, and the real origins behind errors.Unwrap are not verified.
- is_through_join_wrap was reclassified as by-design: the always-matched doc.ErrEmpty leaf stays listed as possibly handled because the Join may hold nil for an operand. The precise answer (doc.ErrNoTitle only) is stated in the comment but not what the tool prints.
- errors.Join(errs...) with a spread slice, a computed Unwrap and a field set through a setter resolve to unresolved(memory) by design (join_spread, unwrap_lazy, field_set_setter), so their underlying origins are not verified.
- Deferred wraps are covered only by defer_join, since changes to results by deferred calls are not implemented (plan S08).
- atoi_wrapped (17 entries, including internal/strconv.Error fall-through entries) and os_open_wrapped (one unresolved(memory) under *fs.PathError.Unwrap) depend on the toolchain's standard library and will change with the Go version; I accepted them as static over-approximations without checking each stdlib path.
- Recursion is queried only at -chain-depth=2; the default depth on a loop is covered by join_loop_default, but not on the recursive Include.
- A custom As on a wrapper that also has Unwrap, and nested errors.Join(errors.Join(...)), are not covered.
- braces.dev/errtrace.Wrap is not covered: stdlib only.

## guards

### Typed nil returned through a helper with a pointer result type is silently dropped

- Query: `guards/queries/nil_check_typed_nil.txtar` (confidence: high)
- Expected: gateway.Precheck does `return check(n)` where check returns *payerr.CodeError (nil or a literal); processor.PrecheckNilCheck forwards it on `err != nil`. 2 entries: concrete *CodeError (literal) and concrete *CodeError [typed nil]. The app's main prints this typed nil as a failed step.
- Actual: 1 entry: only the &CodeError literal in gateway.check; no entry and no Unresolved for the typed nil. In a scratch module, `func nilE() *E { return nil }; func T2() error { return nilE() }` gives `0 entries` although T2 never returns nil.

### Only possible error (typed nil) is missing and an impossible origin is listed

- Query: `guards/queries/eq_typed_nil_return_self.txtar` (confidence: high)
- Expected: processor.PrecheckOnlyTypedNil: `err := gateway.Precheck(n); if err == (*payerr.CodeError)(nil) { return err }; return nil`. Exactly 1 entry: concrete *CodeError [typed nil].
- Actual: 1 entry: the &CodeError literal at gateway.go:112, which can never equal a nil pointer and so cannot reach this return. The typed nil is absent. Same root cause as nil_check_typed_nil; the typed-nil comparison also does not narrow the true branch.

### fmt.Errorf with a %w operand that may be nil has no entry for the wrap-of-nil error

- Query: `guards/queries/is_errorf_wraps_nil.txtar` (confidence: medium)
- Expected: processor.VoidDescribe: `err := fmt.Errorf("void %d: %w", n, gateway.Void(n)); if errors.Is(err, ErrDeclined) { return nil }; return err`. ErrInternal under the chain, plus an opaque fmt.Errorf "void %d: %w" entry for the non-nil error returned when Void succeeds (the app prints `void 3: %!w(<nil>)`).
- Actual: 2 entries: ErrInternal with the chain, and ErrDeclined kept as `possibly handled: wrapped value may be nil` (the documented conservative rule). Nothing stands for the nil-wrapping error. The tool does print `opaque fmt.Errorf` when the operand is a constant nil, so the case is representable.

### Retry loop: `retries exhausted: %w` wrapping a nil error has no entry

- Query: `guards/queries/loop_retry_single.txtar` (confidence: medium)
- Expected: (*runner.Runner).RunTimeoutsOnly with Max == 0 returns fmt.Errorf("job %s: retries exhausted: %w", id, nil), a non-nil error; an extra opaque fmt.Errorf entry in addition to the 4 recorded ones.
- Actual: 4 entries, all correct for the non-nil cases; no entry for the wrap of nil.

### Same wrap-of-nil miss in the `||` retry loop

- Query: `guards/queries/loop_retry_run.txtar` (confidence: medium)
- Expected: (*runner.Runner).Run: ErrTimeout and ErrRateLimited under "retries exhausted", ErrNotFound and *NetError under "job %s: %w", plus an opaque fmt.Errorf entry for the wrap of nil when Max == 0.
- Actual: 6 entries and no wrap-of-nil entry. The two extra entries (ErrNotFound and *NetError under "retries exhausted") are not counted as a bug: guards apply by dominance only (plan S07), so the true side of an `||` is not narrowed. loop_retry_run_depth1.txtar shows the same with -chain-depth 1 and is flagged for the same reason.

### Deferred closure on a named result yields only unresolved entries, one with no position

- Query: `guards/queries/defer_clear_named.txtar` (confidence: medium)
- Expected: processor.SettleDeferred: `defer func(){ if errors.Is(err, ErrDeclined) { err = nil } }(); return gateway.Charge(n)`. Per plan S08: 4 entries (ErrTimeout, *CodeError, ValidationError, opaque), ErrDeclined removed. Every entry should have a position.
- Actual: `2 entries, 2 unresolved`, both `unresolved(memory): load of escaping variable err`. One has span `-:` (entry and hop), the other a zero-width span `flow.go:20:2-2`. The unresolved result is sound because S08 is not implemented (the tool's history stops at S07); the positionless entry is a defect regardless. Other apps' recorded outputs show `-:` too.

### Deferred translation of a named result is not reported (S08 not implemented)

- Query: `guards/queries/defer_translate_named.txtar` (confidence: medium)
- Expected: processor.SettleDeferredTranslate: 5 entries: ErrInternal with the "settle: %w" chain, ErrDeclined, *CodeError, ValidationError, opaque; no bare ErrTimeout.
- Actual: 2 unresolved(memory) entries (one with span `-:`), no origins, not even the ErrInternal the closure stores.

### Deferred closure that only reads the named result drops all origins

- Query: `guards/queries/defer_log_keeps.txtar` (confidence: medium)
- Expected: processor.SettleDeferredLog: the closure only logs on `err == ErrDeclined`; `return gateway.Void(n)`. 2 entries: ErrDeclined and ErrInternal.
- Actual: 2 unresolved(memory) `load of escaping variable err` entries (one with span `-:`), no origins, although nothing but the return writes err.

### Zero-width span for a zero-size composite literal origin (cosmetic)

- Query: `guards/queries/eq_value_sentinels.txtar` (confidence: low)
- Expected: The entry for `return payerr.Empty{}` (gateway.go:105) spans the literal, 105:10-24, like the ValidationError literal entry (101:10-49).
- Actual: Entry and its first hop are at `gateway.go:105:3-3`, a zero-width span on the `return` keyword. Filtering itself is as expected: both sentinels removed, the fresh values kept as possibly handled.

Coverage gaps:

- Defer guards on named results: the three defer_* queries only record unresolved(memory), because plan step S08 is not implemented; holds vs must-not-filter cannot be told apart yet.
- Goroutine boundaries (goroutine_log_keeps, goroutine_clear, goroutine_channel): captured variables and channel receives are unresolved(memory) by design, so the queries only pin that the set is not silently empty.
- Helper, method, closure and os.IsNotExist-style guards only mark `possibly handled`; no query shows a helper guard removing an origin, because the tool has no helper summaries.
- `||` true-branch narrowing (is_or_true_branch, and the continue in loop_retry_run) and Join/Errorf with operands that may be nil (is_join_maybe_nil) are recorded as documented, sound over-approximations with spurious entries; they are not flagged.
- eq_typed_nil records the right output but does not prove the typed-nil guard removes anything, since the typed nil through a helper is never reported; the inline variants give one unmarked entry for both the nil and non-nil pointer.
- A guard on a parameter in a declaration query (param_guard_decl) is unresolved(depends-on-caller); caller substitution is only covered by the two call-site queries and the enclosing declarations.
- Not covered: guards on errors read from struct fields or maps, generic helper guards, labeled break/continue or goto, fallthrough in switches, and a deliberately non-compiling file.
- cmd/guards panics by design in ChargeMustCode (single-value assertion) unless run with exactly two arguments; run-time ground truth from main was taken with two arguments only. I left this as is and noted it in the README.
- The builder's one-off unexplained FAIL on an -update run did not reproduce: one -update run and three plain -count=1 runs passed here.

## plugins

### Closure returning a captured parameter of its creator yields depends-on-caller even though the Target itself calls the creator

- Query: `plugins/queries/maintenance_decl.txtar` (confidence: medium)
- Expected: `failWith(err error) func(core.Event) error { return func(core.Event) error { return err } }` and `Maintenance(e) { check := failWith(ErrMaintenance); return check(e) }`: only `sentinel example.com/plugins/dispatch.ErrMaintenance`, through failWith$1 (1 of 1 candidates). The app's main prints exactly that error at run time.
- Actual: `1 entries, 1 unresolved`: `unresolved(depends-on-caller): parameter err (error)` at failWith's parameter, no Origin. The parameter of failWith is not substituted with the argument at the failWith(...) call inside the Target, and the label does not say whose parameter it is. Reproduced in a standalone scratch module, also with a wrap between parameter and closure. A closure capturing a local of its creator works (limited_decl).

### Unresolved entry for a named result written by a deferred closure is printed with the position-less span "-"

- Query: `plugins/queries/close_decl.txtar` (confidence: medium)
- Expected: Every entry carries a usable span. For (*Dispatcher).Close (named result err, deferred closure assigning it) the unresolved(memory) entry should sit at the return statement or the result variable. (True set under Go semantics: core.ErrDropped and dispatch.ErrSpoolBusy; those need plan step S08, which is not built, so their absence is documented interim behaviour.)
- Actual: `2 entries, 2 unresolved`: two `unresolved(memory): load of escaping variable err` entries for the same variable, one at the return statement (N:2-2) and one whose entry span and only path hop are `-` (render's noSpan). recover_lit shows the same, and the `-` entries propagate into hf_handle_decl, pipeline_decl and quota_call.

### Same position-less "-" entry for a closure with deferred recover() writing its named result

- Query: `plugins/queries/recover_lit.txtar` (confidence: medium)
- Expected: A usable span on both unresolved(memory) entries of middleware.Recover's closure. (True set: opaque fmt.Errorf "panic: %v" plus what next.Handle returns; needs S08.)
- Actual: `2 entries, 2 unresolved`, one at the return statement and one with span `-`; no Origins.

### Concrete Origin built by a constant conversion gets a zero-width span on the return keyword

- Query: `plugins/queries/fanout_static.txtar` (confidence: low)
- Expected: For `return Status(503)` in (drivers.Gateway).Send the entry `concrete example.com/plugins/drivers.Status` should carry the span of the conversion expression (cols 26-37 of that line), as the output contract says for Concrete Types.
- Actual: The entry is printed with a zero-width span at the start of the `return` keyword (N:3-3). The plan's "Positions and spans" section names the enclosing Return's position as the fallback for an implicit MakeInterface, so this may be accepted; the kvstore app's recorded outputs show the same pattern.

### Candidates are not narrowed by the caller's argument (plan S09 not implemented); also alertuser_decl, retrysms_call, retrydriver_decl, selftest_decl

- Query: `plugins/queries/ops_call.txtar` (confidence: low)
- Expected: `deliver(drivers.Pager{}, e)` where deliver's d.Send has 4 Candidates program-wide: only drivers.ErrPager, hop `1 of 4 candidates`. Likewise alertuser_decl: ErrEmail and ErrSMS only; retrysms_call: ErrSMS only; retrydriver_decl: ErrPager only; selftest_decl: no main.errUnnamed.
- Actual: Every caller gets all Candidates of the callee's parameter (`4 of 4`, `3 of 3`, `2 of 2 candidates`): ops_call and alertuser_decl list ErrEmail, ErrSMS, ErrPager, ErrQueue; retrysms_call and retrydriver_decl list ErrEmptyBody, ErrSMS, ErrPager; selftest_decl includes errUnnamed from the closure another caller passes to Each. Sound over-approximation, missing the precision S09 specifies.

### Guard after an un-narrowed dynamic call leaves impossible Origins instead of an empty set (S09); also nohttp_decl

- Query: `plugins/queries/quietly_decl.txtar` (confidence: low)
- Expected: SendQuietly passes only *SMS to SendPair and filters ErrSMS with errors.Is: 0 entries. SendNoHTTP passes only *Webhook to SendSeven and filters *HTTPError with errors.As: 0 entries.
- Actual: quietly_decl: 1 entry, drivers.ErrEmail (the other Candidate of SendPair). nohttp_decl: 6 entries, the Origins of the six other Candidates of SendSeven. The guards themselves remove exactly the right Origin (ErrSMS, concrete *HTTPError) with no possibly-handled marks.

### Static call of the HandlerFunc adapter on a closure literal returns every HandlerFunc closure's errors (S09); default chain depth explodes to 1093 entries

- Query: `plugins/queries/quota_call.txtar` (confidence: low)
- Expected: `h := core.HandlerFunc(func(core.Event) error { return errQuota }); return h.Handle(e)`: only main.errQuota.
- Actual: With -chain-depth=1: `28 entries, 10 unresolved` (errQuota, core.ErrDropped, drivers.ErrPager, middleware.ErrGaveUp and the Recover closure's unresolved(memory) entries, bare and under the middleware wraps; f(e) has 5 of 5 candidates). The builder reported 1093 entries and 273 unresolved with the default -chain-depth; I did not re-run that.

Coverage gaps:

- Per-caller Candidate narrowing cannot be shown working: the tool does not narrow today (plan S09), so eight queries record the un-narrowed output as suspected bugs. The only positive call-site narrowing is static (mexpr_send_call).
- Deferred dynamic calls that change a named result (close_decl, recover_lit) only record unresolved(memory); no positive result exists until plan S08 is built.
- Captures written by a callback, capture by address and a goroutine result over a channel (firsterr_decl, byaddr_decl, async_decl) are unresolved(memory) by design, so their true Origins (ErrSlack wrapped, ErrFile, the handlers' errors) are not asserted; main's run-time output does confirm the first two.
- Unreachable Targets only show the S06 warning plus unresolved(no-candidates); depends-on-caller for parameters and -tests roots (S12) cannot be exercised.
- sort.Interface has no error result, so the local implementation is only exercised inside the RunPlugins flow.
- A standalone []core.Handler broadcast is not covered (slices appear as Group.Members, []core.Plugin and the variadic []Option); generic handlers and registries are left to the e2e dynamic/generic_* fixtures.
- A typed nil pointer stored in an interface (as opposed to a nil interface, probe_decl) is not covered.
- The middleware queries are pinned to -chain-depth=1; deeper Wrap Chains through recursion are only exercised in the tree (group_handle_decl, -chain-depth=3).
- JSON output and the -tags flag are not exercised by this app.

## generics

### Unwrap of an instantiated generic type is 'not modelled' (wrapped origins lost)

- Query: `generics/queries/deep_wrap_decl.txtar` (confidence: high)
- Expected: deepWrap returns errs.Wrap(1, errs.Wrap("job", fmt.Errorf("stop: %w", errs.ErrStop))): one sentinel ErrStop with wrap chain fmt.Errorf "stop: %w", *Wrapped[string].Unwrap, *Wrapped[int].Unwrap.
- Actual: Only entry is unresolved(memory) "*errs.Wrapped[int].Unwrap not modelled"; ErrStop is missing. Confirmed with my own repro: the non-generic struct W gives sentinel ErrX under a W.Unwrap chain, the identical GW[C] gives unresolved. Likely cause: unwrapMethod in internal/flow/wrap.go looks up the method body with prog.FuncValue, which does not give the instance's body, so the shape falls to unwrapOther. Also seen in deep_wrap_chain_depth, wrap_generic_decl, getorload_alias_call, save_user_call, save_tags_call, save_decl, each_decl.

### Propagation Path drops the generic-instance hop and names the wrong callee

- Query: `generics/queries/check_explicit_call.txtar` (confidence: high)
- Expected: Path for validate.Check[validate.Name](n): '(Name).Validate' -> 'in validate.Check[validate.Name], call to (Name).Validate' -> 'in checkExplicit, call to validate.Check[validate.Name]'.
- Actual: The last hop reads 'in checkExplicit, call to (validate.Name).Validate' at the span of the Check call; the instance hop is gone. Error Sets are unaffected. Cause confirmed in internal/summary/path.go:164: the fold for synthetic wrappers tests src.Callee.Synthetic != "", which is also true for generic instances. Also seen in check_decl, check_inferred_call, check_iface_call, new_ptr_call, lparen_ptr_instance_call, all_decl, all_inner_call, map_named_func_call, map_closure_call, nested_first_filter, total_decl, each_explicit_inst_callback, each_inferred_inst_callback, each_decl, ttl_get_call, find_retry_decl, walk_recursive_call, save_user_call, save_tags_call.

### Call-site Target inside a generic function fails when the generic is also called from another generic body

- Query: `generics/queries/check_inner_call.txtar` (confidence: high)
- Expected: Call site v.Validate() inside Check[T Validator]: the union over the six real instances, the same 7 entries as the declaration query check_decl.
- Actual: exit 2: 'errtrace: validate/validate.go:54:82: no SSA call instruction for call to v.Validate in example.com/generics/validate.Check[T]'. All[T] and (*Repo[ID,T]).Save call Check with their own type parameter, which creates an abstract instance Check[T] that is an instantiation wrapper without the call; callTarget in internal/target/target.go errors on it instead of skipping it. Minimal repro: func Check[T V](v T) error { return v.Validate() } plus func All[T V](v T) error { return Check(v) }; without All the same query succeeds.

### Callback Candidates not narrowed at the call site (spurious origin)

- Query: `generics/queries/funcval_call.txtar` (confidence: medium)
- Expected: conv := seq.Map[int,string]; conv(nums, closure) should yield only the closure's concrete *TypedErr[int].
- Actual: errOdd from labelOdd$1 (the other user of Map[int,string]) is also reported, '2 of 2 candidates'. Confirmed with a non-generic apply(cb) called from two functions: a declaration query on one caller reports both closures' sentinels. The plan specifies this narrowing in step S09, which is not built yet (no narrow.go or localtypes.go; git history ends at S07), so this is an unimplemented step rather than a regression.

### Range-over-func yields only nameless 'load of escaping variable' Unresolved entries

- Query: `generics/queries/iter_range_direct_decl.txtar` (confidence: medium)
- Expected: for _, err := range seq.Parse(src, parsePort) { if err != nil { return err } }; return nil should give ErrEmpty and *TypedErr[string] from parsePort, or at least an Unresolved that names what could not be followed.
- Actual: 2 entries 'unresolved(memory): load of escaping variable ' with an empty variable name (trailing space) and zero-width spans (pipeline.go:105:4-4 and 108:2-2), one of them for the plain `return nil`. Same in iter_collect_decl and iter_collect_generic_decl, and in a non-generic iter.Seq[error] repro. Explicit Unresolved entries are permitted by the plan; the defects are the empty detail, the zero-width spans and that a plain range-over-func loop is not followed at all.

### errors.Is against a pointer-typed package variable: 'target that is not a sentinel'

- Query: `generics/queries/is_generic_sentinel_decl.txtar` (confidence: medium)
- Expected: errors.Is(err, errs.ErrIntRange) with var ErrIntRange = &TypedErr[int]{...} returns nil: ErrIntRange is removed and the other 3 classify origins are kept without notes.
- Actual: All 4 entries are kept, each marked possibly handled 'errors.Is with a target that is not a sentinel', although the tool labels ErrIntRange 'sentinel' in the same output. Confirmed non-generic: var ErrP = &P{} shows the same, while var ErrPI error = &P{} is handled and removed. The recogniser does not look through the implicit conversion of a concretely typed global.

### == against a pointer-typed sentinel variable is ignored with no note

- Query: `generics/queries/eq_generic_sentinel_decl.txtar` (confidence: medium)
- Expected: if err == errs.ErrIntRange { return nil }; return err gives 3 entries (concrete *TypedErr[int], concrete *TypedErr[string], sentinel ErrStrRange); ErrIntRange is removed.
- Actual: 4 entries; sentinel ErrIntRange is kept with no possibly-handled note. Confirmed non-generic with var ErrP = &P{}; the same guard against an error-typed variable removes the sentinel.

### Type-switch binding re-wrapped with %w loses the sentinel and moves the origin span

- Query: `generics/queries/type_switch_generic_decl.txtar` (confidence: medium)
- Expected: switch err := classify(n, s).(type) { case *errs.TypedErr[string]: return fmt.Errorf("string: %w", err) ... } gives concrete *TypedErr[string] (origin at the literal in classify) and sentinel ErrStrRange, both under "string: %w".
- Actual: 1 entry: concrete *TypedErr[string] whose origin span is the use of the switch variable inside fmt.Errorf (failures.go:60:35-38); sentinel ErrStrRange is missing. Confirmed non-generic, where errors.Is(Switch(0), ErrP) is true at run time but the set has no ErrP. The set under-reports here: a sentinel that does reach the result is absent.

### Sentinel passed through a parameter of concrete pointer type becomes a fresh Concrete Type origin

- Query: `generics/queries/lift_ptr_sentinel_call.txtar` (confidence: medium)
- Expected: errs.Lift(errs.ErrIntRange), with func Lift[E error](e E) error { return e } instantiated as Lift[*TypedErr[int]]: one entry, sentinel ErrIntRange (errors.Is(liftRange(), ErrIntRange) holds at run time).
- Actual: 1 entry: 'concrete *errs.TypedErr[int]' located at the conversion inside Lift (errs/errs.go:66:49-50); the sentinel identity of the argument is lost. Same family as the type-switch binding case: a non-interface value converted to error is treated as a new origin instead of being substituted from the caller's argument.

### == between two parameters is ignored at substitution, with no note

- Query: `generics/queries/skip_plain_sentinel_call.txtar` (confidence: low)
- Expected: errs.Skip(err, errs.ErrNotFound), where Skip[T comparable](v, skip T) T returns the zero value when v == skip and err comes from (*Cache).Get (ErrNotFound only): 0 entries, or ErrNotFound marked possibly handled.
- Actual: 1 entry: sentinel ErrNotFound with no note. Same for a non-generic func skip(v, s error) error and for skip_comparable_call (Skip[error](classify(...), errs.ErrIntRange) keeps ErrIntRange). The plan's guard table lists only == against a sentinel or nil, so the kept entry is a sound over-approximation; only the removal or a note is missing.

### Helper guard returning (E, bool) leaves no possibly-handled note

- Query: `generics/queries/astype_helper_guard_decl.txtar` (confidence: low)
- Expected: if _, ok := errs.AsType[*errs.TypedErr[string]](err); ok { return nil } is a guard through a helper: under the plan's rule the string-typed entries should be kept and marked possibly handled.
- Actual: All 4 classify origins are listed with no possibly-handled note. Confirmed non-generic. The bool-returning helper errs.Equal in equal_helper_guard_decl is marked correctly.

Coverage gaps:

- skip_decl was unflagged, not confirmed: a declaration Target whose result type is a type parameter (Skip[T comparable](v, skip T) T) is rejected with 'has no error result'. That follows the plan's documented predicate on the declared result type, so it is recorded as a limit; call sites of Skip[error] are accepted.
- Typed nil through a generic conversion (lift_typed_nil_decl): the entry is correct but carries no [typed nil] mark; recorded as a precision limit, not a bug.
- is_through_instances_decl cannot show an empty set: Retry wraps a possibly nil `last` and constants (n = 3) are not tracked, so ErrNotFound is kept as possibly handled; is_wrapped_instance_decl covers the true 0-entry case instead.
- The -chain-depth truncation on generic wrappers (deep_wrap_chain_depth) cannot show truncation while the generic Unwrap is not modelled; walk_recursive_call with -chain-depth=2 covers truncation on %w wraps.
- Fan-out grouping is only exercised with one entry (-fanout=1 on the Flusher call), since the generic Flush implementations share a single origin.
- Per-call-site narrowing of callback Candidates is only observable as a suspected bug (funcval_call), because plan step S09 is not built; no query can show it working.
- The call site v.Validate() inside Check (static in five instances, dynamic in Check[Validator]) is recorded only as the exit-2 failure in check_inner_call; its Error Set cannot be checked until that bug is fixed.
- No deliberately broken (non-compiling) file and no -tags, -tests or -json query: -json and -tests are not flags today, and load errors are covered by e2e fixtures.
- Symbol-style targets (import/path.Func[...]) are not covered because the CLI accepts only position targets.
- Not covered: a self-recursive generic function (only mutual recursion Walk/walkKids), a closure declaration Target (func keyword of a FuncLit) inside a generic body, and deferred changes to named results in generic code (S08 not built).
- The query count is 85, above the suggested 25-40; many are call-site/declaration pairs on the same code to check narrowing to one instantiation.

## stdlibio

### NEW: errors.Join operand sets are multiplied and silently cut at 1024 combinations, with no Unresolved entry

- Query: `stdlibio/queries/seal_join_silent_truncation.txtar` (confidence: high)
- Expected: source.Seal returns errors.Join(werr, serr, f.Close()) on one *os.File (Write: 22 values, Stat: 17, Close: 8). Expected the union of the three operand sets under one errors.Join link, including every origin of seal_write_call_site, or at least an explicit unresolved(fixpoint-limit).
- Actual: 27 entries and no fixpoint-limit entry. Six Write origins are silently missing: io.ErrUnexpectedEOF (bare and under *fs.PathError.Unwrap), poll.ErrDeadlineExceeded and poll.ErrNotPollable under *fs.PathError.Unwrap, and both "waiting for unsupported file type" entries. Cause: /home/user/errtrace/internal/summary/eval.go, mapTreeRaw builds one Wrap per combination and does `if len(nextCombos) == maxValues { break }` without adding a marker. Reproduced outside the app: Join of three calls with 11 sentinels each prints 31 of 33 sentinels and 0 unresolved.

### errors.Join with a call operand that can only return nil drops the whole Join

- Query: `stdlibio/queries/save_join_nil_operand.txtar` (confidence: high)
- Expected: source.Save returns errors.Join(werr, flush(f), f.Close()); flush only returns nil in the default build. Expected the (*os.File).Write and Close origins under errors.Join, plus the os.Create entry under "source: create: %w".
- Actual: Only the os.Create entry (1 entry). Reproduced outside the app: errors.Join(eof(), none()) prints 0 entries, while a literal nil operand prints io.EOF. Same function as above: in mapTreeRaw an operand with an empty substituted set gives an empty product.

### fmt.Errorf with %w of a nil-only call result reports 0 entries

- Query: `stdlibio/queries/durability_errorf_nil_operand.txtar` (confidence: high)
- Expected: source.Durability returns fmt.Errorf("source: flush %s: %w", f.Name(), flush(f)), which is never nil. Expected 1 entry, opaque fmt.Errorf (this is what the tool prints for a literal nil or a nil local variable as the %w operand).
- Actual: "Error Set of example.com/stdlibio/source.Durability: 0 entries". Reproduced outside the app, also for fmt.Errorf("x: %w %w", eof(), none()), which loses io.EOF. Same root cause as the Join case.

### Three-operand errors.Join hits the 1024 cap: fixpoint-limit entry and a cut listing

- Query: `stdlibio/queries/save_join_durable.txtar` (confidence: medium)
- Expected: With -tags durable, the union of the Write, Sync (under "source: sync: %w") and Close origins under errors.Join plus the os.Create entry; a few dozen entries, no fixpoint-limit.
- Actual: "unresolved(fixpoint-limit): result 0 of example.com/stdlibio/source.Save can carry more than 1024 values" and a cut listing (io.ErrUnexpectedEOF, the opaque "waiting for unsupported file type" and the *fs.PathError-wrapped poll.ErrDeadlineExceeded/ErrNotPollable of Write are missing). The marker appears only because the os.Create value pushes the 1024 combinations over the per-result cap. The cap is documented (plan P08/S04), so the incompleteness is announced; the defect is the product representation.

### Same Join as Seal plus one value: cut listing, here with a fixpoint-limit entry

- Query: `stdlibio/queries/finish_join_fixpoint_limit.txtar` (confidence: medium)
- Expected: source.Finish: the os.OpenFile entry under "source: reopen: %w" plus the full union of Seal's Write/Stat/Close origins under errors.Join, no fixpoint-limit.
- Actual: The same six Write origins are missing as in Seal, and "unresolved(fixpoint-limit): result 0 of example.com/stdlibio/source.Finish can carry more than 1024 values" is printed. Added as the contrast case to seal_join_silent_truncation.

### Package variable assigned at run time (sync.Once callback) reported as a plain sentinel

- Query: `stdlibio/queries/root_once_runtime_global.txtar` (confidence: medium)
- Expected: walk.Root returns rootErr, nil at init and assigned inside rootOnce.Do. Expected unresolved(memory) or the "[mutable global: assigned at ...]" mark of plan S10.
- Actual: One entry "sentinel example.com/stdlibio/walk.rootErr", 0 unresolved, no mark; walk.ErrNoRoot and filepath.Abs's errors are hidden without a trace. S10 is not implemented yet, but today's output claims completeness.

### Uninitialised package error variable set by a setter reported as a plain sentinel

- Query: `stdlibio/queries/murky_runtime_global.txtar` (confidence: medium)
- Expected: murky.Last returns lastErr (no initialiser, assigned by Remember(err)). Expected unresolved(memory) or a mutable-global mark.
- Actual: "sentinel example.com/stdlibio/murky.lastErr", 1 entry, 0 unresolved.

### Unresolved entry for the recover exit of a named result has the span "-"

- Query: `stdlibio/queries/slurp_named_result_defer_close.txtar` (confidence: medium)
- Expected: Every Unresolved entry's span is its site (output contract). Final expectation (plan S08, not implemented) is the Open/ReadAll/Close origins under their %w links plus source.ErrEmpty.
- Actual: 5 entries, all "unresolved(memory): load of escaping variable err"; this is the documented interim behaviour for a captured variable written by a closure, so I narrowed the flag to the one real defect: the recover-exit entry is printed as "-: unresolved(memory): ..." with path hop "-: in example.com/stdlibio/source.Slurp". The same "-" span appears in the kvstore and bank apps' recorded outputs.

### recover converted to error: Unresolved entry with the span "-"

- Query: `stdlibio/queries/murky_recover_to_error.txtar` (confidence: medium)
- Expected: A source position for the recover-exit entry. Final expectation (plan S08 recover_exit / S10, not implemented) is opaque fmt.Errorf "murky: panic: %v" plus unresolved(recover).
- Actual: 2 entries, both "unresolved(memory): load of escaping variable err" (not a silent drop); one has span "-" and path hop "-". It also shows in diagnostics_fanout_group, diagnostics_call_site_ungrouped and main_run_call_site.

### walk.Has, which cannot fail, gets the 41 entries of walk.Find (callback Candidates shared between WalkDir callers)

- Query: `stdlibio/queries/has_walkdir_skipall.txtar` (confidence: medium)
- Expected: 0 entries. filepath.WalkDir never returns an error of its own: Lstat/ReadDir failures are handed to the callback and only the callback's results come back; Has's callback ignores its err argument and returns only nil or fs.SkipAll. (The builder's expectation "WalkDir's own lstat/readdir errors" was wrong and is corrected.)
- Actual: 41 entries, identical to find_walkdir_callback, every one through walk.Find$1 as Candidate "2 of 2" of the fn parameter, including sentinel walk.ErrTooMany. Narrowing at substitution (plan P08/S09) is not implemented yet.

### io.ReadAtLeast on a local *strings.Reader lists (*os.File).Read errors from another caller

- Query: `stdlibio/queries/header_readatleast_eof_filtered.txtar` (confidence: low)
- Expected: source.Header: exactly io.ErrUnexpectedEOF and io.ErrShortBuffer (2 entries).
- Actual: 21 entries: those two plus os.ErrInvalid, os.ErrClosed, internal/poll and syscall values from (*os.File).Read, which only Peek -> Magic passes in. S09 narrowing is not implemented yet.

### Call-site query does not narrow Candidates by the argument's concrete type

- Query: `stdlibio/queries/magic_call_site_strings_reader.txtar` (confidence: low)
- Expected: Call site source.Magic(strings.NewReader(doc)): source.ErrEmpty, source.ErrBadMagic and io.ErrShortBuffer only (3 entries).
- Actual: 22 entries, identical to the declaration set of Magic, including all (*os.File).Read errors. S09 is not implemented yet.

### Call site Pump(sink, src) with local *Sink/*Limited lists os.File errors of the other caller

- Query: `stdlibio/queries/transfer_pump_call_site.txtar` (confidence: low)
- Expected: stream.ErrQuota, stream.ErrSinkFull, io.ErrShortWrite and io.errInvalidWrite only.
- Actual: 39 entries, the same as the declaration set of Pump, including os/poll/syscall errors and Syscall6 no-body entries that only main's Pump(os.Stdout, ...) can cause. S09 is not implemented yet.

Coverage gaps:

- Targets inside the standard library itself cannot be expressed: query args take paths relative to the app directory and the harness has no $GOROOT substitution for args. "Outside the module" is covered by the nested module (loaded ad hoc as command-line-arguments), a _test.go file, a build-tag-excluded file, a non-existent file and a symbol-name argument.
- Refuted and unflagged: nonexistent_file. "file is not in any loaded package" with exit 2 is the documented message (plan S02); a "no such file" hint would only be friendlier. symbol_name_target prints the same message because symbol Targets (S13) are not implemented.
- Not flagged, documented over-approximation worth knowing: (*os.File).wrapErr starts with `if err == nil || err == io.EOF { return err }`, a merge of two guard edges without phi that plan P07 says is ignored. Every poll/syscall error of File.Read/Write/Sync is therefore listed twice, once under *fs.PathError.Unwrap (real) and once bare (cannot happen). Confirmed with a stand-alone repro; described in the README and the affected comments.
- Not flagged: Candidates of the callback of os.ignoringEINTR / poll.ignoringEINTR are shared by all callers, so os.Stat and os.Remove sets contain entries of os.chmod$1, os.openFileNolog$1 etc. Same cause as the S09 narrowing flags, inside the stdlib.
- Not flagged: with the Go 1.27 stdlib strconv.toError switches over internal/strconv constants, so every strconv query lists bare internal/strconv.Error values and e.g. ErrRange for ParseBool.
- Reflection, recover, atomic.Value, unsafe and any round trips show as unresolved(memory) rather than the plan's dedicated kinds unresolved(reflection)/unresolved(recover); nothing is silently dropped, and S08-S10 are not implemented, so these are not flagged.
- encoding/json is covered only by decode_as_syntaxerror, readjson_decoder_eof and main_run_call_site; I checked those for the guard effects (io.EOF absent, everything under the %w link, only *json.SyntaxError-compatible values under Decode's link) but did not verify each of the 100+ json v2 leaves individually. json.Unmarshal/json.Marshal call-site queries and Marshal's panic/recover path remain uncovered because of their size.
- Recorded outputs depend on the repository's Go 1.27.4 toolchain stdlib; note that running errtrace by hand inside the app directory picks the local go1.24.7 stdlib (the app's go.mod says go 1.24) unless GOTOOLCHAIN=go1.27.4 is set, which gives different sets than the harness.
- Query count is 85, well above the 25-40 guideline; one run of the app takes about 65 s.
- sort callbacks and strings.Map are not covered (no error-returning callback); a position on a non-Go file and a line past end of file are left to the e2e cli fixtures; -tests, -depth and -boundary are not implemented and are represented only by the unknown-flag query json_flag_rejected.
- Outside my directory (reported, not touched): git status shows modified files in sibling app directories (bank and others), presumably from the agents working there; no file outside internal/cli/testdata/apps/ is modified.

## targets

### Call-site Target drops the [+N more paths] marker that the callee's declaration Target shows

- Query: `targets/queries/call_go.txtar` (confidence: medium)
- Expected: For `go warm(s)` the store.ErrClosed entry should carry [+1 more paths]: it reaches warm via ping() and via s.Get->Check, both unwrapped. decl_warm_paths (declaration target on warm) prints exactly that marker.
- Actual: Same two entries, but ErrClosed has no marker. Confirmed cause in internal/query/query.go: MorePaths is len(f.Leaves[k])-1, the number of sources in the top function only; at a call site the single call is the only source. Not specific to go statements.

### Same path-count loss on a plain call: run(os.Args[1:]) in main

- Query: `targets/queries/call_main_run.txtar` (confidence: low)
- Expected: Same 11 entries as decl_run_ingest, with errUsage marked as having more paths (direct return in run, hook via ingest, closure via ingest).
- Actual: Entries match, but errUsage has no [+N more paths] marker, while decl_run_ingest shows one for the same function. Same cause as call_go.

### [+N more paths] undercounts paths that merge inside a callee

- Query: `targets/queries/decl_run_ingest.txtar` (confidence: low)
- Expected: errUsage [+2 more paths]: three distinct paths reach run (direct return, hook(key) inside ingest, the immediately invoked closure inside ingest).
- Actual: errUsage [+1 more paths]: the two paths that merge inside ingest count as one source. All 11 entries (1 unresolved) are otherwise correct. plan.md S04 says MorePaths 'counts the other distinct sources reached', so this may be intended; the data model text says 'distinct paths'.

### -p wildcard pattern that matches no package is silently ignored

- Query: `targets/queries/p_wildcard_matches_nothing.txtar` (confidence: medium)
- Expected: `-p ./cmd/zzz... internal/platform/platform.go:5:6` should fail with exit 2 or at least surface go list's warning ("./cmd/zzz..." matched no packages), as `-p ./nope/...` fails in p_matches_nothing.
- Actual: Exit 0. The pattern is dropped, only the target file's package is loaded, the Probe Error Set is printed, and stderr has only the unrelated `warning: target not reachable from main/init`. A mistyped -p silently narrows the analysed program (fewer Candidates).

### Garbled -p wildcard pattern is silently ignored (same cause as p_wildcard_matches_nothing)

- Query: `targets/queries/p_malformed.txtar` (confidence: medium)
- Expected: `-p ./cmd/...ingest/[` should give exit 2 with a go list error or a matched-no-packages warning.
- Actual: Exit 0 with the Probe Error Set and only the not-reachable warning. The pattern contains `...`, so go list treats it as a wildcard that matches nothing; it is the same defect, not a separate one.

### One extra column accepted on the last line of a newline-terminated file

- Query: `targets/queries/pos_column_out_of_range.txtar` (confidence: low)
- Expected: `internal/platform/platform.go:8:3` (line 8 is `}` plus newline, 1 byte) should be rejected with `column 3 is out of range (line 8 has 1 bytes)`, as 5:22 is rejected for a 20-byte line.
- Actual: 8:3 is accepted and echoed as `platform.go:8:3: no function declaration or call at position`. span.resolvePoint uses lineEnd = file size for the last line, so the trailing newline counts as line content. Mitigating: 8:3 is also how the tool prints the documented EOF offset #180, so I lowered the builder's confidence.

### Out-of-range message miscounts the last line's bytes (same cause as pos_column_out_of_range)

- Query: `targets/queries/pos_column_last_line_past_eof.txtar` (confidence: low)
- Expected: `internal/platform/platform.go:8:4` rejected with `column 4 is out of range (line 8 has 1 bytes)`.
- Actual: Rejected with `column 4 is out of range (line 8 has 2 bytes)`: the final newline is counted for the last line only (line 5 is reported as 20 bytes without its newline).

### File of a nested module is analysed as package command-line-arguments

- Query: `targets/queries/file_nested_module.txtar` (confidence: low)
- Expected: For tools/lintcfg/cfg.go (own go.mod, module example.com/lintcfg): either exit 2 saying the file is outside the main module, or the Error Set named with its real package (example.com/lintcfg.Load, example.com/lintcfg.ErrNoConfig).
- Actual: Exit 0, `Error Set of command-line-arguments.Load: 1 entries` with `sentinel command-line-arguments.ErrNoConfig`, plus the not-reachable warning. The set is correct; the names are go/packages' ad-hoc package for a file= query outside the main module.

### Missing file reported as 'file is not in any loaded package'

- Query: `targets/queries/file_nonexistent.txtar` (confidence: low)
- Expected: `internal/store/missing.go:3:1` should give exit 2 with a message saying the file does not exist.
- Actual: Exit 2 with `file is not in any loaded package`, the same text used for an existing file excluded by -p or build tags. The same generic message is recorded for file_nonexistent_dir, file_abs_nonexistent, file_parent_outside, file_directory, file_directory_pos, file_non_go, file_go_mod, pos_file_uri and cli_dash_h_after_terminator. Message quality only.

### go list errors printed with a stray '-: ' position placeholder

- Query: `targets/queries/p_no_go_files.txtar` (confidence: low)
- Expected: `errtrace: no Go files in assets` for `-p ./assets`.
- Actual: `errtrace: -: no Go files in assets`. load.packageErrors uses packages.Error.Error(), which prints `-` for an empty Pos. The same prefix appears in p_matches_nothing, p_nonexistent_dir, p_nested_module_pattern and the new p_unknown_import_path. Cosmetic.

Coverage gaps:

- Absolute path to an existing file: each query runs in a fresh temp dir that args cannot name, so only an absolute non-existent path is recorded (file_abs_nonexistent). Checked by hand on a scratch copy: the absolute path to platform.go:5:6 prints the Probe set.
- An existing file outside the module tree: only the nested module (file_nested_module) and non-existent ../ paths (file_parent_outside) are covered; the app may not create files outside its own directory, and `-p ../outside` leaks the random temp-dir parent into the message.
- _test.go targets are covered only as negative cases (test_file_func, test_file_call, test_file_external): the CLI has no -tests flag yet (plan step S12), so in-package and external test files give 'file is not in any loaded package'.
- Arguments containing spaces or an empty-string argument (space-separated -tags, an empty target): the args section is split on white space.
- -debug=timing on its own is excluded by the brief (nondeterministic); only its rejection next to an invalid mode is recorded (cli_debug_timing_invalid).
- Positions on the defer/go keywords and on the inner selector `be` use literal line:col (call_defer_keyword, call_go_keyword, call_selector_inner_sel) rather than markers, to avoid shifting the columns of existing queries; they must be updated by hand if those lines are edited.
- Windows drive-letter paths in the position grammar were not exercised (Linux-only run).
- A call site inside a closure body, and a call of a call's result (f()()), are not present in the app.
- Outside my directory (reported, not touched): git status shows modified files under the sibling apps bank, generics, guards, kvstore, plugins, stdlibio and wrapchain, from the other agents; nothing outside internal/cli/testdata/apps is changed.

## bank

### errors.Unwrap is resolved by Candidates: json.Unmarshal is reported to return the app sentinel domain.ErrLimitExceeded

- Query: `bank/queries/parse_unmarshal_call.txtar` (confidence: medium)
- Expected: Call site json.Unmarshal(data, &w): encoding/json origins only (*SyntaxError, *UnmarshalTypeError, plus accepted static over-approximation of other json/time/strconv origins and Unresolved reflection entries). No origin from the application.
- Actual: 158 entries, including 'sentinel example.com/bank/domain.ErrLimitExceeded', bare and behind (*json.UnmarshalTypeError).Unwrap. The path shows errors.Unwrap(err) in encoding/json.transformSyntacticError: the u.Unwrap() call inside errors.Unwrap is resolved to all 14 Unwrap methods of the program ('14 of 14 candidates'), including (*limits.Exceeded).Unwrap, which returns that sentinel, instead of unwrapping the argument's values.

### Top-level entry point is cut to an arbitrary prefix: 10 of 11 switch cases disappear behind one fixpoint-limit entry

- Query: `bank/queries/cli_run_decl.txtar` (confidence: medium)
- Expected: cmd/cli.run: errUsage bare and behind "%w (unknown command %q)", parse.Line's set, and the sets of Transfer, Balance (behind "cli: balance: %w"), Owner, batch, Explain, Preview, settled, SetStatus, check, Ready and PublishFirst.
- Actual: 87 entries, all from the first case (parse.Line, Service.Transfer) plus bare errUsage, and 'unresolved(fixpoint-limit): result 0 of run can carry more than 1024 values'. Absent: "cli: balance: %w", "%w (unknown command %q)", ErrUnsupported, NotFoundError[TransferID], ErrTxDone, and every rule and event-handler origin. The cut is announced, but the 1024 values are used up by the operand combinations of errors.Join(err, aerr) in Service.Transfer.

### errors.Join makes one value per combination of operand values, so a 15-line function hits the 1024 cap and loses its non-joined entries

- Query: `bank/queries/service_transfer_decl.txtar` (confidence: medium)
- Expected: (*service.Service).Transfer: execute's set behind "transfer %d: %w", Begin errors behind "tx: begin: %w", ErrGaveUp, ctx errors behind "retry: interrupted: %w", each with and without errors.Join; audit errors behind errors.Join; no event-handler origin.
- Actual: 68 entries plus unresolved(fixpoint-limit). Every entry is behind errors.Join: the plain 'return err' variants and the "retry: interrupted: %w" entries are gone. execute's errors are absent (hidden behind WithTx's unresolved values, S08), and errors of other retry.Do callers appear (S09). internal/flow/analyze.go maxCombos and internal/summary/solver.go maxValues are both 1024 and count combinations, not origins.

### fixpoint-limit reported although the callee sets total about 110 entries

- Query: `bank/queries/service_setstatus_decl.txtar` (confidence: medium)
- Expected: (*service.Service).SetStatus: store.Get's set, store.Put's set, and Publish's 3 handler origins behind "events: %s handler %d: %w" + errors.Join; no limit.
- Actual: Those entries (56 + 30 + 21) plus 'unresolved(fixpoint-limit): result 0 of SetStatus can carry more than 1024 values', while events.Publish queried alone reports no limit. Likely cause: all = errors.Join(all, ...) in Publish's loop multiplies values per combination.

### Missing [chain truncated] variants at the default chain depth for a join-in-a-loop

- Query: `bank/queries/events_publish_decl.txtar` (confidence: low)
- Expected: (*events.Bus).Publish: for each of the 3 handler origins, chain variants up to -chain-depth 8 and one [chain truncated] entry for longer chains.
- Actual: 21 entries. Only the opaque (*stats).onTransfer origin has a [chain truncated] entry (errors.Join x8). For ErrLimitExceeded and *ValidationError the variants stop at 8 links with no truncated entry, so longer chains are silently dropped. With -chain-depth=3 (events_publish_chain_depth3) all three origins are treated consistently. Probably the same combination cap.

### Middleware chain: the cut at fixpoint-limit drops every error Serve really returns and keeps untranslated handler errors

- Query: `bank/queries/api_serve_decl.txtar` (confidence: medium)
- Expected: (*api.Server).Serve: *api.Error (404 no route + Translate's sites), api.ErrUnavailable, ErrDuplicate behind (*api.Error).Unwrap, Recover's opaque "middleware: panic serving %s: %v".
- Actual: 209 entries plus unresolved(fixpoint-limit). 206 come from the handleTransfer/handleBalance Candidates (untranslated *ValidationError, NotFoundError, ErrBusy, audit and parse errors). Translate's *api.Error values, api.ErrUnavailable and middleware.ErrUnauthorized are all missing; only the 404 *api.Error and one Recover entry remain. That raw handlers are Candidates is defensible (Chain returns h itself when mws is empty, and VTA is flow-insensitive); the defect is the cycle and the cut that discards the real origins.

### Guarded call site inherits the over-approximated, cut Serve set

- Query: `bank/queries/server_respond_call.txtar` (confidence: low)
- Expected: respond(out, srv.Serve(ctx, req)): 1 entry, Recover's opaque panic error behind "server: unhandled: %w"; everything else is an *api.Error and is swallowed.
- Actual: 208 entries behind "server: unhandled: %w" (domain, parse, audit and filerepo errors) plus fixpoint-limit. The errors.As guard itself works: the *api.Error entry is removed. Derived from api_serve_decl.

### Curried closure call Auth(token)(h)(ctx, req): the free variable next gets every Handler in the program

- Query: `bank/queries/middleware_guarded_call.txtar` (confidence: low)
- Expected: Call site middleware.Guarded(ctx, "t", req, ok) with ok a closure returning nil: 1 entry, sentinel middleware.ErrUnauthorized.
- Actual: 209 entries plus unresolved(fixpoint-limit). ErrUnauthorized is present, but next (bound from Auth$1's parameter) has all 7 Handler Candidates, because Chain also calls Auth$1. This goes beyond the narrowing plan S09 promises (the dynamic call is on a free variable of a returned closure, not on a parameter), so it is closer to a design limit than a defect.

### Named result captured by a deferred closure collapses the whole set to unresolved(memory), with a position-less entry (plan S08 not implemented)

- Query: `bank/queries/tx_withtx_decl.txtar` (confidence: medium)
- Expected: (*tx.Manager).WithTx: Begin errors behind "tx: begin: %w", ctx.Err() bare, fn's errors bare and behind errors.Join with "tx: rollback: %w" errors, commit errors behind "tx: commit: %w".
- Actual: 4 entries, all 'unresolved(memory): load of escaping variable err'. Honest but uninformative: even the "tx: begin: %w" return, stored before the defer is registered, is lost. The first entry is printed with position '-:' and the others with zero-width spans (tx.go:25:3-3); that rendering defect stands regardless of S08.

### Same defer / named-result collapse at the server entry point (plan S08 not implemented)

- Query: `bank/queries/server_run_decl.txtar` (confidence: medium)
- Expected: cmd/server.run: config.Load's set behind "server: %w", openStore's and audit.Open's errors, serve's unhandled error and logger.Close() errors, all behind errors.Join.
- Actual: 5 'unresolved(memory): load of escaping variable err' entries and nothing else; the first has position '-:'.

### Batch: guards over Transfer's set degrade to 'possibly handled' on nearly every entry, plus fixpoint-limit

- Query: `bank/queries/service_batch_call.txtar` (confidence: low)
- Expected: svc.Batch: Transfer's set behind errors.Join (cancellations also bare); domain.ErrDuplicate removed by the errors.Is skip.
- Actual: 89 entries, 88 marked possibly handled, plus unresolved(fixpoint-limit). ErrDuplicate is absent only because execute's errors are invisible (S08). Derived from tx_withtx_decl and service_transfer_decl; each note follows the documented may-match rule.

### Translation layer end to end: ErrDuplicate (the one cause that must be kept) is missing and the set is cut

- Query: `bank/queries/api_transfer_decl.txtar` (confidence: medium)
- Expected: (*api.Server).Transfer: fresh *api.Error (400/404/402/500), api.ErrUnavailable, and behind (*api.Error).Unwrap only values containing domain.ErrDuplicate.
- Actual: 72 entries. ErrDuplicate is missing (execute is hidden behind WithTx, S08) and handleTransfer hits unresolved(fixpoint-limit). The builder's 'leak' claim is refuted: the entries behind (*api.Error).Unwrap (retry.ErrBusy, errDryRun, NotFoundError, filerepo and audit errors) all belong to Join or double-%w values with a may-match part (Unresolved, or syscall.Errno's custom Is), which the documented rule keeps. api_import_decl shows the same translation is exact when the callee set is visible.

### Call-site argument does not narrow Candidates of a dynamic call on a parameter (plan S09 not implemented)

- Query: `bank/queries/rules_explain_funds_call.txtar` (confidence: medium)
- Expected: Call site rules.Explain(ctx, rules.Funds{}, in): 1 entry, concrete domain.InsufficientFundsError behind "%s says no: %w".
- Actual: 3 entries, identical to the declaration query: also the two limits.Exceeded results of the rules.Limit Candidate ('2 of 2 candidates').

### Same missing narrowing for the rules.Limit argument

- Query: `bank/queries/rules_explain_limit_call.txtar` (confidence: medium)
- Expected: 2 entries (the (*Exceeded).Unwrap results behind "%s says no: %w", one also behind "daily budget of %s: %w"); no InsufficientFundsError.
- Actual: Also 'concrete domain.InsufficientFundsError' from the rules.Funds Candidate. The same spurious entry is in rules_explain_limit_chain_depth1 and rules_explain_limit_chain_depth2, whose comments reference this query rather than carrying their own flag.

### io.Writer argument of concrete type *bytes.Buffer does not narrow w.Write (plan S09 not implemented)

- Query: `bank/queries/audit_write_buf_call.txtar` (confidence: medium)
- Expected: Call site audit.Write(&sink, ...) with sink a bytes.Buffer: 1 entry, io.ErrShortWrite behind "audit: write: %w".
- Actual: 24 entries, the same as the declaration query: audit.ErrFull (Quota) and all (*os.File).Write errors; '3 of 3 candidates'.

### Closure argument of retry.Do is not narrowed (plan S09 not implemented)

- Query: `bank/queries/retry_busy_call.txtar` (confidence: medium)
- Expected: Call site retry.Do(ctx, 2, closure returning ErrBusy or errDryRun): errDryRun bare, ErrGaveUp and ErrBusy behind "%w after %d attempts: %w", ctx.Err() behind "retry: interrupted: %w".
- Actual: 125 entries: op has 3 Candidates (also retry.Value's closure and Service.Transfer's closure), so NotFoundError[AccountID], filerepo read errors and WithTx's unresolved values appear. errDryRun behind the gave-up wrap is the documented dominance over-approximation, not part of this bug. Running the cli with BANK_DRY_RUN confirmed errDryRun is returned without the gave-up wrap.

### Generic Value[T] reports errors of closures that other callers pass to retry.Do (plan S09 not implemented)

- Query: `bank/queries/retry_value_decl.txtar` (confidence: medium)
- Expected: retry.Value (only instance Value[*domain.Account], op = store.Get): NotFoundError[AccountID], filerepo read/corrupt errors, ctx.Err(), ErrGaveUp.
- Actual: Also cmd/cli.errDryRun and retry.ErrBusy (candidate cmd/cli.probe$2) and WithTx's unresolved values (candidate Service.Transfer$1): the MakeClosure that Value passes to Do does not narrow op's Candidates.

### Origin printed with position '-', and foreign closures as Candidates of os.ignoringEINTR

- Query: `bank/queries/server_openstore_call.txtar` (confidence: medium)
- Expected: Call to openStore: os.MkdirAll errors behind (*fs.PathError).Unwrap + "filerepo: %w" + "server: store: %w", each with a $GOROOT position, from MkdirAll's own closures.
- Actual: (1) The syscall.ENOTDIR origin in os.MkdirAll (a constant converted to error inside &PathError{...}) is printed as '-: concrete syscall.Errno'. (2) The 9 Candidates of fn in os.ignoringEINTR include os.chmod$1, os.openFileNolog$1 and os.checkPidfd$1, which MkdirAll never passes (S09); the same noise is in filerepo_commit_decl, filerepo_rollback_decl, tx_rollback_call and tx_ping_decl. The builder's 'duplicate lines in a group' claim is refuted: they are distinct entries differing in the *fs.PathError site, which the documented group format does not show.

### strconv.ParseInt/Atoi report 11 spurious 'concrete internal/strconv.Error' entries (Go 1.27 stdlib)

- Query: `bank/queries/parse_amount_decl.txtar` (confidence: low)
- Expected: parse.Amount: strconv.ErrSyntax and the opaque base/bit-size errors behind (*strconv.NumError).Unwrap + "amount %q: %w", two *domain.ValidationError; no ErrRange.
- Actual: Those, plus 11 'concrete internal/strconv.Error' entries. strconv.toError maps every internal/strconv.Error constant to a *NumError in a value switch, so the type never leaves strconv; comparing against constants of a value type is outside the plan's guard table (only sentinels and nil), so this is arguably documented imprecision. The same noise is in every query reaching ParseInt/Atoi (config_load_decl, config_set_call, cli_settled_decl, parse_line_decl, parse_request_decl, filerepo_get_decl, filerepo_decode_call).

Coverage gaps:

- Refuted builder flags, now unflagged with explanatory comments: filerepo_commit_decl (err == nil || err == X) and tx_ping_decl (!errors.Is && !errors.As). Plan S07 applies guards by dominance only, so the merged side of a disjunction is not filtered. It is sound and documented, but it affects retry.Do and os.(*File).wrapErr and so adds spurious variants to many sets in this app.
- Refuted builder flag: api_balance_decl. Everything behind (*api.Error).Unwrap is a documented may-match (custom Is of NotFoundError and syscall.Errno, Unresolved values), kept under errors.Is(err, ErrDuplicate) without a note.
- Named results changed in defer (WithTx rollback join, server run's Close join): plan S08 is not implemented, so those queries record only unresolved(memory). Service.Transfer, Batch, api.Transfer and Serve therefore cannot show execute's error set; the correct sets are in the comments only.
- Call-site queries that should narrow Candidates compared with the declaration (rules.Explain, audit.Write, retry.Do, middleware.Guarded): plan S09 narrowing is not implemented, so call-site and declaration outputs are identical. Real differences show only for static callees, generic instances (memory_get_lookup_call) and callee-without-caller-wrap sites (parse_line_when_call, config_set_call, filerepo_decode_call, tx_begin_call).
- Context cancellation origins (context.Canceled / DeadlineExceeded) never appear as sentinels: (*context.cancelCtx).Err is always 'unresolved(memory): value of TypeAssert not modelled', so Translate's 503 branch and Batch's cancellation guard are exercised only as may-match.
- Custom Unwrap returning a sentinel (limits.Exceeded) and errors.Join over a slice (EvaluateAll) are documented v1 limits; rules_advisory_decl therefore cannot show the Limit results being removed by its errors.Is guard.
- No -json query (S11 is not in this build's flag set), no deliberately broken package or load-error query, and no -p / -tags query in this app; sibling apps (targets) cover CLI and target edge cases.
- Outputs containing stdlib internals (os, syscall, internal/poll, internal/strconv, encoding/json v2) depend on the toolchain the test runs with (go1.27.4 via the repository go.mod); parse_unmarshal_call (830 lines) is especially sensitive.
- The largest recorded outputs (api_transfer_decl, api_balance_decl, filerepo_get_decl, service_batch_call, the 209-entry middleware ones) were verified through per-entry label and chain summaries of ungrouped re-runs, not line by line. service_preview_call, middleware_auth_literal_target and service_batch_call were checked by comment, entry counts and their callee sets only.
- Root causes for the fixpoint-limit group (combination cap on errors.Join) and for the missing [chain truncated] entries were inferred from the outputs and from reading internal/flow/wrap.go and internal/summary/solver.go, not isolated in a minimal reproduction.
- Marked positions that still have no query include cmd/server.serve, Service.Owner, Service.Ready, memory.Store.Begin, filerepo.Store.Begin as a declaration, and the audit.Write call with a Quota over io.Discard (it would duplicate audit_write_buf_call).
