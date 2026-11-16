# wrapchain

A small document pipeline (parse -> validate -> transform -> export) whose
layers wrap the errors of the layer below. The application exists to exercise
**Wraps and Wrap Chains**: every spelling of `fmt.Errorf`, `errors.Join`,
custom `Unwrap` types, project-local wrap helpers, wraps in loops and
recursion, `-chain-depth`, and the interaction of wraps with guards and
dynamic calls.

## Packages

| Package | Role |
|---|---|
| `.` (`main.go`, `pipeline.go`) | Runs the four stages, so every Target is reachable from `main`. `pipeline.go` holds the guard functions (`errors.Is`, `errors.As`, `==`, type switch/assert) applied to wrapped errors. |
| `doc` | The `Document` model and the pipeline's sentinels (`ErrEmpty`, `ErrSyntax`, `ErrTooLong`, `ErrNoTitle`, `ErrCycle`, `ErrDisk`, `ErrClosed`, `ErrQuota`; `ErrNever` is only returned by a type that is never instantiated). |
| `xerr` | Wrap helpers (`Annotatef`/`Tracef` forward a format, `Wrapf(err, format, args...)`, `WithOp(op, err)`, `WithStage` two levels deep; the last three return nil for a nil err) and wrapper types: `*OpError` (pointer `Unwrap`), `FieldError` (value `Unwrap`), `*LazyError` (computed `Unwrap`), `*StageError` (setter), `*MultiError` (`Unwrap() []error`), `*CodeError` (custom `Is`), `*QuotaError` (custom `As`), `*LimitError`. |
| `parse` | `fmt.Errorf` in all its forms: one and several `%w`, `%w` next to other verbs, explicit indexes and `*` widths, flags on `%w`, nil and non-error operands, `%v`, `%%w`, constant / concatenated / run-time formats, dropped causes; wraps of `strconv.Atoi` and `os.Open`. |
| `validate` | `errors.Join`: with nils, one operand, only nils, a spread slice, joined-then-wrapped, wrapped-then-joined, accumulated in a loop. |
| `transform` | Custom wrappers and helpers, recursion, one origin through several chains, wraps of a parameter, `errors.Unwrap`. |
| `export` | Wraps of dynamic call results (interface `Sink`, function value `Hook`), a deferred `errors.Join`, and `deliver.go`: a ladder of 10 functions that each add exactly one Wrap around `doc.ErrDisk`, for `-chain-depth`. |

## Queries

"negative" means the point of the query is what must *not* be in the Error
Set (or that the invocation is rejected). Queries marked as suspected bugs
record today's output; their comment states the expected result.

| Query | Args | What it checks | Kind | Suspected bug |
|---|---|---|---|---|
| `annotatef_helper_decl` | `xerr/helpers.go:@annotatef` | Declaration Target of a helper that forwards format and args to fmt.Errorf. | positive | no |
| `as_through_wrap` | `pipeline.go:@fieldzero` | errors.As(err, &numErr) { return 0, nil } on parse.Field's two-%w wrap: every error value holds a *strconv.NumError next to doc.ErrSyntax, so As always matches. | negative | no |
| `atoi_wrapped` | `parse/parse.go:@version` | strconv.Atoi wrapped once more with %w. | positive | no |
| `bad_target_const` | `doc/doc.go:@maxline` | Negative: the position is a constant declaration, not a function or call. | negative | no |
| `call_site_at_qualifier` | `transform/transform.go:@nilqual` | Negative: the position is on the package qualifier "xerr" of xerr.WithOp(...), not on the callee name. | negative | no |
| `custom_as` | `transform/transform.go:@overlimit` | if errors.As(err, &limitErr) { return wrap(err) } where *xerr.QuotaError has an As method that fills a **LimitError. | positive | no |
| `custom_is` | `transform/transform.go:@retryable` | if errors.Is(err, xerr.ErrRetryable) { return err } where *xerr.CodeError has an Is method that matches ErrRetryable. | positive | no |
| `defer_join` | `export/export.go:@flush` | named result joined with s.Close() in a deferred closure. | positive | yes |
| `depth_1` | `-chain-depth 1 export/deliver.go:@l10` | 10 Wraps with -chain-depth 1 (space-separated flag value). | positive | no |
| `depth_2_over` | `-chain-depth=2 export/deliver.go:@l3` | 3 Wraps with -chain-depth=2: one over. | positive | no |
| `depth_3_exact` | `-chain-depth=3 export/deliver.go:@l3` | 3 Wraps (Errorf, *OpError, single-operand Join) with -chain-depth=3: the exact boundary. | positive | no |
| `depth_9_fits` | `-chain-depth=9 export/deliver.go:@l9` | 9 Wraps with -chain-depth=9. | positive | no |
| `depth_default_exact` | `export/deliver.go:@l8` | A chain of exactly 8 Wraps (Errorf, WithOp, Join, Errorf, OpError literal, Wrapf, Annotatef, Errorf) at the default -chain-depth 8. | positive | no |
| `depth_default_over` | `export/deliver.go:@l9` | 9 Wraps at the default -chain-depth 8. | positive | no |
| `depth_huge` | `-chain-depth=1000000 export/deliver.go:@l10` | 10 Wraps with a very large -chain-depth. | positive | no |
| `depth_missing_value` | `-chain-depth export/deliver.go:@l3` | -chain-depth without a value swallows the Target as its value. | negative | no |
| `depth_negative` | `-chain-depth=-1 export/deliver.go:@l3` | A negative -chain-depth is rejected. | negative | no |
| `depth_not_a_number` | `-chain-depth=deep export/deliver.go:@l3` | A non-numeric -chain-depth is rejected by flag parsing. | negative | no |
| `depth_zero` | `-chain-depth=0 export/deliver.go:@l3` | -chain-depth=0 is rejected. | negative | no |
| `dynamic_call_site` | `export/export.go:@write` | Call-site Target on s.Write(d). | positive | no |
| `dynamic_wrap` | `export/export.go:@export` | Wrap of the result of an interface call. Candidates are FileSink.Write and NetSink.Write (NullSink is never instantiated). | positive | no |
| `eq_wrapped_kept` | `pipeline.go:@strict` | err == doc.ErrNoTitle on a value that is always an *OpError: the comparison never matches. | positive | no |
| `errorf_always_nil_operand` | `parse/parse.go:@footer` | %w whose operand is always a nil error: fmt.Errorf still returns a non-nil *fmt.wrapError that wraps nothing. | positive | no |
| `errorf_concat_const` | `parse/parse.go:@cell` | Format is a concatenation of two constants (prefixCell + ": %w"), folded at compile time. | positive | no |
| `errorf_const_mixed` | `parse/parse.go:@row` | Format from a named constant, and %w mixed with %d/%s/%v. | positive | no |
| `errorf_index_star` | `parse/parse.go:@column` | Explicit indexes with * widths: "%[2]*[1]d: %[3]w (not %[4]v)" wraps argument 3, and "%*d\|%-12w\|%+v" wraps argument 3 too (the * eats one; flags/width on %w do not matter). | positive | no |
| `errorf_index_then_implicit` | `parse/parse.go:@reorder` | "%[2]w then %w, after %[1]v": the implicit %w follows the explicit index, so arguments 2 and 3 are wrapped. | positive | no |
| `errorf_nil_operand` | `parse/parse.go:@tag` | expected 2 entries: doc.ErrSyntax wrapped by fmt.Errorf "tag: %w", and an entry for the path where the %w operand is a nil error (fmt.Errorf still returns a non-nil error; errorf_always_nil_operand shows it as opaque fmt.Errorf). | positive | yes |
| `errorf_no_wrap` | `parse/parse.go:@percent` | Three errors that drop their cause: escaped %%w with %v of the Atoi error, fmt.Errorf("%s", err.Error()) and errors.New(prefix + err.Error()). | negative | no |
| `errorf_non_error_operand` | `parse/parse.go:@label` | %w with a string operand: fmt.Errorf returns a non-nil error that wraps nothing. | positive | no |
| `errorf_runtime_format` | `parse/parse.go:@section` | Format built at run time ("section "+name+": %w"). | positive | no |
| `errorf_two_w` | `parse/parse.go:@field` | Two %w in one format, one a sentinel, one a *strconv.NumError built by the local digits helper. | positive | no |
| `field_set_conditional` | `transform/transform.go:@maybe` | The wrapped field is doc.ErrEmpty in the literal and overwritten with doc.ErrTooLong on one branch. | positive | no |
| `field_set_late` | `transform/transform.go:@late` | e := &OpError{Op: ...}; e.Err = doc.ErrSyntax; return e - the wrapped field is stored after the literal. | positive | no |
| `field_set_setter` | `transform/transform.go:@setter` | The wrapped field is set through a method (SetCause). | positive | no |
| `forwarded_two_levels` | `transform/transform.go:@trace` | Tracef -> Annotatef -> fmt.Errorf(format, args...), constant format at the outermost caller. | positive | no |
| `func_value_wrap` | `export/export.go:@runhook` | Wrap of the result of a function value (the only Candidate is export.Audit). | positive | no |
| `helper_two_levels` | `transform/transform.go:@stage` | WithStage calls WithOp: a wrap helper two levels deep. | positive | no |
| `is_through_custom_unwrap` | `pipeline.go:@titlelenient` | errors.Is(err, doc.ErrNoTitle) { return nil } on transform.Title, whose only error is doc.ErrNoTitle inside an *xerr.OpError. | negative | no |
| `is_through_join_wrap` | `pipeline.go:@lenient` | errors.Is(err, doc.ErrEmpty) { return nil } on validate.Report's fmt.Errorf(Join(title, body)). | negative | yes |
| `join_all_nil` | `validate/validate.go:@none` | errors.Join of only nil operands returns nil. | negative | no |
| `join_call_site` | `validate/validate.go:@joincall` | Call-site Target on the errors.Join call itself. | positive | no |
| `join_loop_depth3` | `-chain-depth=3 validate/validate.go:@lines` | A loop accumulating err = errors.Join(err, fmt.Errorf("line %d: %w", ...)). | positive | no |
| `join_nils` | `validate/validate.go:@all` | errors.Join(nil, checkTitle(d), nil, checkBody(d)). | positive | no |
| `join_single` | `validate/validate.go:@single` | errors.Join with one operand still wraps. | positive | no |
| `join_spread` | `validate/validate.go:@collect` | errors.Join(errs...) over a slice filled with append. | positive | no |
| `join_then_wrap` | `validate/validate.go:@report` | Joined first, then wrapped. | positive | no |
| `os_open_wrapped` | `parse/parse.go:@open` | os.Open wrapped with %w. | positive | no |
| `recursive_depth2` | `-chain-depth=2 transform/transform.go:@include` | Recursive function wrapping its own result once per level. | positive | no |
| `same_origin_three_chains` | `transform/transform.go:@normalize` | One origin reaching the Target three ways. | positive | no |
| `type_assert_top_level` | `pipeline.go:@normquiet` | err.(*xerr.OpError) swallows only the top-level wrapper. | negative | no |
| `type_switch_wrapper` | `pipeline.go:@quiet` | Type switch swallowing *xerr.OpError, the only dynamic type Title returns. | negative | no |
| `unwrap_join` | `transform/transform.go:@peeljoin` | errors.Unwrap of an errors.Join result is always nil (Unwrap() []error is not consulted). | negative | yes |
| `unwrap_lazy` | `transform/transform.go:@lazy` | Unwrap that decides at run time (returns nil or the Cause field). | positive | no |
| `unwrap_multi` | `transform/transform.go:@multi` | Unwrap() []error wrapper built from a slice literal. | positive | no |
| `unwrap_one_of_two` | `transform/transform.go:@peeltwice` | errors.Unwrap of a doubly wrapped error returns the inner wrap. | positive | yes |
| `unwrap_plain` | `transform/transform.go:@peelplain` | errors.Unwrap of a sentinel that wraps nothing is nil. | negative | yes |
| `unwrap_returned` | `transform/transform.go:@peel` | errors.Unwrap(Title(d)) removes the *OpError link. | positive | yes |
| `unwrap_value_receiver` | `transform/transform.go:@field` | Wrapper with value-receiver Unwrap, returned by value. | positive | no |
| `with_op` | `transform/transform.go:@title` | Project helper WithOp(op, err) returning &OpError{Op, Err} (pointer-receiver Unwrap). | positive | no |
| `with_op_nil` | `transform/transform.go:@noop` | WithOp("noop", nil): the helper returns nil for a nil err. | negative | no |
| `with_op_nil_call` | `transform/transform.go:@nilcall` | Call-site Target on xerr.WithOp("noop", nil). | negative | no |
| `wrap_param_call` | `transform/transform.go:@anncall` | Call-site Target annotate("trim", upper(d)): the argument is substituted. | positive | no |
| `wrap_param_decl` | `transform/transform.go:@annotate` | Declaration Target of a function that wraps its parameter through Wrapf. | positive | no |
| `wrap_then_join` | `validate/validate.go:@labelled` | Wrapped first, then joined. | positive | no |
| `wrapf_helper_decl` | `xerr/helpers.go:@wrapf` | Declaration Target of the helper Wrapf(err, format, args...). | positive | no |
| `wrapper_maybe_nil_inner` | `transform/transform.go:@always` | &xerr.OpError{Err: upper(d)} is built unconditionally, so the result is non-nil even when upper returns nil. | positive | yes |
| `wrapper_nil_inner` | `transform/transform.go:@bare` | &xerr.OpError{Op: "bare"}: a wrapper whose wrapped field stays nil is still a non-nil error. | positive | no |

## Suspected bugs found

- `errorf_nil_operand`, `wrapper_maybe_nil_inner`: a Wrap whose inner value
  *may* be nil (`fmt.Errorf("...%w", maybeNil)`, `&OpError{Err: maybeNil}`)
  is still a non-nil error on the nil path; that alternative is dropped. When
  the inner value is *always* nil (`errorf_always_nil_operand`,
  `wrapper_nil_inner`) the entry is shown, so the two cases are inconsistent.
- `is_through_join_wrap`: an origin that `errors.Is` always matches through
  `fmt.Errorf(errors.Join(...))` stays in the set as "possibly handled".
- `defer_join`: Unresolved entries with no span (`-`) and a zero-width span.
- `unwrap_returned`, `unwrap_one_of_two`, `unwrap_join`, `unwrap_plain`:
  `errors.Unwrap` is not modelled; the result is always a pair of
  `unresolved(memory)` field loads chosen by type, not by the operand.

## Behaviour by design worth knowing

- Truncation at `-chain-depth` keeps the outermost links and cuts the
  innermost ones (`depth_default_over`, `depth_2_over`, `depth_1`,
  `join_loop_depth3`).
- A call-site Target must point at the callee name or later
  (`xerr./*@m*/WithOp(`), not at the package qualifier or receiver
  (`call_site_at_qualifier`).
- `errors.Join(errs...)`, a computed `Unwrap` and a field set through a
  method are `unresolved(memory)` under the wrapper (`join_spread`,
  `unwrap_lazy`, `field_set_setter`).
- `atoi_wrapped` and `os_open_wrapped` follow the standard library of the
  toolchain in use; their entries change with the Go version.

## Running

    go test ./internal/cli -run 'TestApps/wrapchain/'
