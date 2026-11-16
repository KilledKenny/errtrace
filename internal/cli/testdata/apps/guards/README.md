# guards

A payment processor with a retrying job runner. Every handler calls a fake
gateway, inspects the error and then handles, translates, re-wraps or
re-raises it. The application exists to test **guard filtering**: which Error
Origins are removed from (or must stay in) the Error Set after a check.

## Packages

| Package | Role |
|---|---|
| `payerr` | Error vocabulary: sentinels, `*CodeError`, value-type `ValidationError`, `*OpError` (custom `Unwrap`), `Temporary` interface and `*NetError`, `*FlexError` (custom `Is`), `*ShapeError` (custom `As`), zero-size `Empty`, value-type and zero-size sentinels. |
| `gateway` | Fake gateway. `Charge` (sentinels, concrete types, opaque), `Lookup` (wrapped by `fmt.Errorf` and `*OpError`), `Refund` (`errors.Join`), `Void`, `Capture` (concrete type behind a wrapper), `Quirky` (custom `Is`/`As`), `Validate` (value/zero-size), `Precheck*` (typed nil). |
| `fsx` | Stdlib-shaped fakes: `ErrNotExist`, `EOF`, `*PathError`, `IsNotExist`; `Stat` returns the real `fs.ErrNotExist` / `io.EOF`. |
| `processor` | The handlers. `eq.go`: `==`/`!=`/value switches; `is.go`: `errors.Is`; `as.go`: `errors.As`, assertions, type switches; `helper.go`: helper guards and guards on parameters; `flow.go`: defers and goroutines; `receipts.go`: fs / EOF idioms. |
| `runner` | Retry loops (`continue`, `break`, retries exhausted). |
| `cmd/guards` | `main`: runs every handler once, so all targets are reachable. |

## Queries

"positive" = the guard holds and removes origins; "negative (must not
filter)" = the guard must leave the origins in the set (or can only mark them
`possibly handled`); "negative (CLI error)" = the invocation is rejected.

| Query | Args | What it checks | Kind | Suspected bug |
|---|---|---|---|---|
| `as_and_field_keeps` | `processor/as.go:@asCode` | errors.As(err, &ce) && ce.Code == 402 -> nil: other CodeErrors are returned. | negative (must not filter) | no |
| `as_interface_target` | `processor/as.go:@asIface` | errors.As with an interface target (payerr.Temporary) on gateway.Lookup. | positive (filters) | no |
| `as_pointer_and_value` | `processor/as.go:@asTargets` | errors.As with a **CodeError target and a *ValidationError (value type) target. | positive (filters) | no |
| `as_through_unwrap` | `processor/as.go:@asUnwrap` | errors.As(&*CodeError) on gateway.Capture: CodeError bare and hidden behind *OpError. | positive (filters) | no |
| `as_wrapper_type` | `processor/as.go:@asWrapper` | if !errors.As(err, &opErr) { return err }; return nil: handles everything wrapped by *OpError. | positive (filters) | no |
| `assert_comma_ok` | `processor/as.go:@commaOk` | if _, ok := err.(*CodeError); ok { return nil }; return err. | positive (filters) | no |
| `assert_comma_ok_wrapped` | `processor/as.go:@commaOkWrapped` | Comma-ok assertion (ok && ce != nil) on gateway.Capture checks the top-level type only. | negative (must not filter) | no |
| `assert_single_value` | `processor/as.go:@singleAssert` | ce := err.(*CodeError) without comma-ok panics for every other type, so only *CodeError reaches the return. | negative (must not filter) | no |
| `custom_as_method` | `processor/as.go:@customAs` | errors.As(&*CodeError) on gateway.Quirky: *ShapeError has a custom As that accepts **CodeError. | negative (must not filter) | no |
| `custom_is_method` | `processor/is.go:@customIs` | errors.Is(err, ErrNotFound) on gateway.Quirky: *FlexError has a custom Is, *ShapeError only a custom As. | negative (must not filter) | no |
| `defer_clear_named` | `processor/flow.go:@deferClear` | deferred guards on a named result are not modelled (plan S08) and the direct origins are lost. | positive (filters) | yes |
| `defer_log_keeps` | `processor/flow.go:@deferLog` | as defer_clear_named. The deferred closure only reads err (logs on err == ErrDeclined) and | negative (must not filter) | yes |
| `defer_translate_named` | `processor/flow.go:@deferTranslate` | as defer_clear_named. The deferred closure replaces err == ErrTimeout by | positive (filters) | yes |
| `eof_loop_break` | `processor/receipts.go:@eofLoop` | Read loop: if err == fsx.EOF { break }; if err != nil { return err }; return nil after the loop. | positive (filters) | no |
| `eq_after_wrap` | `processor/eq.go:@wrapThenEq` | err is wrapped with fmt.Errorf before err == ErrDeclined, so the guard never matches. | negative (must not filter) | no |
| `eq_alias_copy` | `processor/eq.go:@aliasCopy` | Guard on a renamed copy (cause := err; if cause == ErrDeclined) still filters err. | positive (filters) | no |
| `eq_else_chain` | `processor/eq.go:@elseChain` | if nil / else if == ErrDeclined -> ErrDuplicate / else if == ErrTimeout -> nil / else return err. | positive (filters) | no |
| `eq_local_value` | `processor/eq.go:@localCompare` | Comparison against a local error value (first := Void(1)), not a sentinel: nothing can be removed for sure. | negative (must not filter) | no |
| `eq_other_variable` | `processor/eq.go:@otherVar` | Guard on cerr (Charge) while verr (Void) is returned in the guarded branch: verr is NOT filtered. | negative (must not filter) | no |
| `eq_reassigned` | `processor/eq.go:@reassign` | err is guarded, then overwritten by gateway.Void: the old guards must not filter the new value. | negative (must not filter) | no |
| `eq_return_nil` | `processor/eq.go:@ignoreDeclined` | if err == ErrDeclined { return nil }; return err, on gateway.Charge. | positive (filters) | no |
| `eq_return_self` | `processor/eq.go:@onlyDeclined` | if err == ErrDeclined { return err }; return nil: the guarded branch returns the error itself. | negative (must not filter) | no |
| `eq_typed_nil` | `processor/eq.go:@typedNil` | err == (*CodeError)(nil) on gateway.Precheck, which returns a typed nil or &CodeError. | positive (filters) | no |
| `eq_value_sentinels` | `processor/eq.go:@valueSentinels` | wrong span for the zero-size literal. The entry for the fresh payerr.Empty{} (gateway.go | positive (filters) | yes |
| `eq_wrapped_no_match` | `processor/eq.go:@eqWrapped` | == against three sentinels on gateway.Lookup, where two arrive wrapped. | negative (must not filter) | no |
| `flag_chain_depth_zero` | `-chain-depth 0 processor/eq.go:@ignoreDeclined` | Negative: -chain-depth 0 is rejected. | negative (CLI error) | no |
| `fs_helper_is_not_exist` | `processor/receipts.go:@fsHelper` | err != nil && !fsx.IsNotExist(err) -> return err: os.IsNotExist-shaped helper guard. | negative (must not filter) | no |
| `fs_is_not_exist` | `processor/receipts.go:@fsIs` | errors.Is(err, fsx.ErrNotExist) on a local PathError-shaped wrapper. | positive (filters) | no |
| `goroutine_channel` | `processor/flow.go:@goChan` | The error is guarded inside a goroutine and delivered over a channel (channels are out of scope). | negative (must not filter) | no |
| `goroutine_clear` | `processor/flow.go:@goClear` | A goroutine clears the captured err when it is ErrDeclined, and the function waits for it. | negative (must not filter) | no |
| `goroutine_log_keeps` | `processor/flow.go:@goLog` | A goroutine inspects the captured err but never changes it; the guard must not filter. | negative (must not filter) | no |
| `helper_func` | `processor/helper.go:@helperFunc` | Guard through a helper function isNotFound(err) bool. | negative (must not filter) | no |
| `helper_method` | `processor/helper.go:@helperMethod` | Guard through a method p.ignorable(err). | negative (must not filter) | no |
| `is_and_budget_keeps` | `processor/is.go:@andBudget` | Is(ErrRateLimited) && budget > 0 -> return nil: with no budget the rate limit is returned. | negative (must not filter) | no |
| `is_and_chain` | `processor/is.go:@andChain` | err != nil && !Is(ErrRateLimited) && !Is(ErrNotFound) -> return err. | positive (filters) | no |
| `is_join_maybe_nil` | `processor/is.go:@joinMaybe` | errors.Join(Refund(n), Void(n)) guarded by if errors.Is(err, ErrInternal) { return nil }. | positive (filters) | yes |
| `is_joined` | `processor/is.go:@isJoined` | errors.Is(err, ErrDuplicate) on errors.Join(ErrDeclined, ErrDuplicate) / ErrInternal. | positive (filters) | no |
| `is_log_only_keeps` | `processor/is.go:@logOnly` | The guard only logs and falls through to return err: nothing is filtered. | negative (must not filter) | no |
| `is_negated` | `processor/is.go:@notIs` | if !errors.Is(err, ErrNotFound) { return err }; return nil. | positive (filters) | no |
| `is_nested` | `processor/is.go:@nested` | Nested ifs: not-found is only swallowed when lenient (else returned), rate limit always swallowed. | positive (filters) | no |
| `is_or_force` | `processor/is.go:@orChain` | Is(ErrRateLimited) \|\| Is(ErrTimeout) \|\| force -> return nil; the false branch implies neither matched. | positive (filters) | no |
| `is_or_true_branch` | `processor/is.go:@orRewrap` | the true branch of an \|\| of errors.Is checks is not narrowed. | positive (filters) | yes |
| `is_rewrap_only` | `processor/is.go:@rewrap` | Only errors matching Is(ErrTimeout) are returned, re-wrapped; everything else becomes nil. | positive (filters) | no |
| `is_swap_on_match` | `processor/is.go:@swapOnIs` | if errors.Is(err, ErrTimeout) { err = gateway.Lookup(n+1) }; return err. | positive (filters) | no |
| `is_switch_true` | `processor/is.go:@switchTrue` | Tagless switch: case err == nil; case Is(ErrNotFound): nil; case Is(ErrRateLimited): ErrInternal; else err. | positive (filters) | no |
| `is_translate` | `processor/is.go:@translate` | if errors.Is(err, ErrNotFound) { return ErrDuplicate }; return err. | positive (filters) | no |
| `is_wrapped_unwrap` | `processor/is.go:@isWrapped` | errors.Is through fmt.Errorf %w (ErrNotFound) and through a custom Unwrap type (ErrTimeout in *OpError). | positive (filters) | no |
| `loop_break_keeps` | `runner/runner.go:@loopBreak` | if err != ErrTimeout { break } and return err after the loop: a timeout on the last attempt is returned. | negative (must not filter) | no |
| `loop_continue` | `runner/runner.go:@loopContinue` | if err == ErrTimeout { continue }; if err != nil { return err } inside a retry loop. | positive (filters) | no |
| `loop_last_error` | `runner/runner.go:@loopLast` | last = err only when err != nil && err != ErrTimeout; return last after the loop. | positive (filters) | no |
| `loop_retry_run` | `runner/runner.go:@run` | same \|\| narrowing gap as is_or_true_branch, on a continue edge. | positive (filters) | yes |
| `loop_retry_run_depth1` | `-chain-depth 1 runner/runner.go:@run` | same spurious entries as loop_retry_run (\|\| on a continue edge), here with -chain-depth 1. | positive (filters) | yes |
| `loop_retry_single` | `runner/runner.go:@runSingle` | the wrap of a nil error is not an entry. | positive (filters) | yes |
| `neq_else` | `processor/eq.go:@neqElse` | if err != ErrTimeout { return err } else { return nil }. | positive (filters) | no |
| `nil_check_typed_nil` | `processor/eq.go:@nilCheck` | the typed nil is missing. gateway.Precheck returns check(n), a *CodeError that is nil for | negative (must not filter) | yes |
| `nil_check_typed_nil_inline` | `processor/eq.go:@nilCheckInline` | typed nil not reported when it merges with a non-nil pointer. gateway.PrecheckInline does | negative (must not filter) | yes |
| `param_guard_call_capture` | `processor/helper.go:@classifyCaptureCall` | Call-site query: Classify(err) with err from gateway.Capture. | negative (must not filter) | no |
| `param_guard_call_lookup` | `processor/helper.go:@classifyLookupCall` | Call-site query: Classify(gateway.Lookup(n)); the callee's guard on its parameter filters the argument. | positive (filters) | no |
| `param_guard_decl` | `processor/helper.go:@classify` | Declaration query on a filter func(err error) error that drops Is(ErrNotFound) / == ErrDeclined and wraps the rest. | negative (must not filter) | no |
| `param_guard_partial` | `processor/helper.go:@paramPartial` | passThrough translates ErrInternal only when strict (strict && err == ErrInternal). | negative (must not filter) | no |
| `param_guard_twice` | `processor/helper.go:@classifyTwice` | Classify(gateway.Charge(n)) filtered in the callee, then errors.Is(err, ErrTimeout) in the caller (through the wrap). | positive (filters) | no |
| `std_is_not_exist` | `processor/receipts.go:@stdIs` | errors.Is(err, fs.ErrNotExist) \|\| err == io.EOF on &fs.PathError{Err: fs.ErrNotExist}, io.EOF, io.ErrUnexpectedEOF. | positive (filters) | no |
| `std_os_is_not_exist` | `processor/receipts.go:@stdHelper` | os.IsNotExist(err) as the guard (a helper call into the stdlib). | negative (must not filter) | no |
| `target_bool_helper` | `processor/helper.go:@isNotFoundDecl` | Negative: the Target is the guard helper isNotFound, which returns bool and has no error result. | negative (CLI error) | no |
| `target_not_a_call` | `cmd/guards/main.go:@mainIf` | Negative: the position is an identifier inside an if condition, neither a declaration name nor a call. | negative (CLI error) | no |
| `type_switch_iface` | `processor/as.go:@typeSwitchIface` | Type switch with a bound variable: case Temporary -> nil; case *OpError -> e.Err; case nil; default -> e. | positive (filters) | no |
| `type_switch_multi` | `processor/as.go:@typeSwitch` | Type switch: case nil; case *CodeError, ValidationError: return nil; default: return err. | positive (filters) | no |
| `type_switch_only_case` | `processor/as.go:@typeSwitchOnly` | Type switch that returns err only in case *CodeError; everything else becomes nil. | positive (filters) | no |
| `unwrap_eq_other_value` | `processor/is.go:@unwrapEq` | if errors.Unwrap(err) == ErrNotFound { return nil }: the guard tests a different value than err. | negative (must not filter) | no |
| `value_switch_default` | `processor/eq.go:@valueSwitch` | switch err { case nil; case ErrDeclined, ErrTimeout: return nil; default: return err }. | positive (filters) | no |
| `value_switch_keep` | `processor/eq.go:@valueSwitchKeep` | switch err: case ErrDeclined returns err, case ErrTimeout returns ErrInternal, default returns nil. | positive (filters) | no |

## Suspected bugs found

- **`||` true branch is not narrowed** (`is_or_true_branch`, `loop_retry_run`,
  `loop_retry_run_depth1`): after `if errors.Is(err, A) || errors.Is(err, B) { use err }`
  the guarded branch still carries every origin, although a single
  `errors.Is` narrows it (`is_rewrap_only`, `loop_retry_single`).
- **Deferred guards on named results** (`defer_*`): only
  `unresolved(memory): load of escaping variable err` is reported, without any
  origin and with one entry that has no position (`-:`).
- **Typed nil lost** (`nil_check_typed_nil`, `nil_check_typed_nil_inline`): a
  nil `*CodeError` returned through a helper, or merged with a non-nil pointer
  in one variable, is not reported as `[typed nil]`.
- **Join operand kept under `errors.Is`** (`is_join_maybe_nil`): the checked
  sentinel itself stays, marked `wrapped value may be nil`.
- **Zero-width span** for a zero-size composite literal (`eq_value_sentinels`).
- **Wrap of nil** (`loop_retry_single`, low confidence): `fmt.Errorf("%w", nil)`
  yields a non-nil error but no entry.

Not flagged, but worth knowing (documented scope): single-value type
assertions do not narrow (`assert_single_value`), a guard on
`errors.Unwrap(err)` does not filter `err` (`unwrap_eq_other_value`), helper
guards only mark `possibly handled`, variables captured by goroutines and
channel receives are `unresolved(memory)`, and a parameter in a declaration
query is `unresolved(depends-on-caller)`.

## Running

    go test ./internal/cli -run 'TestApps/guards/'
