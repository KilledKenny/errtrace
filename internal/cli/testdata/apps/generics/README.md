# generics

A generic collections and data-pipeline library with a demo `main`. It is the
test application for errtrace's handling of **generics**: instantiation,
constraints, generic types and methods, generic error types, callbacks,
iterators and aliases.

`main` runs five steps (validation, pipeline, caching, repository, failures);
every helper in the root package is reachable from it, so Targets are reachable
from the roots unless a query says otherwise. Module: `example.com/generics`,
`go 1.24`, stdlib only.

## Packages

| Package | Contents |
|---|---|
| `.` (main) | `main.go` (the steps), `validation.go`, `pipeline.go`, `caching.go`, `failures.go`, `handling.go`: small non-generic callers, one situation each. Aliases `Scores = cache.Cache[string,int]` and the generic alias `Pair[K,V] = cache.Entry[K,V]`. |
| `errs` | Sentinels; generic error type `TypedErr[T]` and its generic alias `Failure[T]`; sentinels by instantiation `ErrIntRange` / `ErrStrRange`; generic wrapper `Wrapped[C]` with `Unwrap` and `Wrap[C]`; `Tagged[T]` (embeds `error`, no `Unwrap`); `Lift[E error]`, `AsType[E error]`, `Drop[E error]`, `Equal[T comparable]`, `Skip[T comparable]`, constructor `NewErr[T]` returning `*TypedErr[T]`. |
| `validate` | Method constraint `Validator` with `UserID`, `Name`, `*Email`; `Check[T Validator]`, `All`, never-instantiated `Unused`; pointer-constraint constructor `New[T any, P Ptr[T]]`; type set `Number` (`~int \| ~int64 \| ~float64`) with `Positive` and `InRange` (returns `*TypedErr[N]`, uses `min`/`max`). |
| `seq` | `Map`, `Filter`, `Reduce`, `Each`, `First`, `Retry`, `Keys`; range-over-func `Parse` / `Collect` over `iter.Seq2[T, error]`; generic tree `Node[T]` with the mutually recursive `Walk` / `walkKids`. |
| `cache` | `Cache[K comparable, V any]` (`Get`, `Put`, `GetOrLoad`, `Flush` with `clear`, uncalled `Evict`), `GetOr`, the non-generic interface `Flusher`, and `TTL[K,V]`, which embeds `*Cache[K,V]`. |
| `model` | Entities `User` and `Tag`, whose `Validate` methods call the generic helpers. |
| `repo` | `Repo[ID comparable, T Entity[ID]]` built on `cache`, `validate` and `seq`; `SaveAll` passes the bound method `r.Save` to `seq.Each`. |
| `result` | `Result[T]` (error in a struct field), `Option[T]` with a non-error `Unwrap`, never-instantiated `Either[L,R]`. |

## Running

    go test ./internal/cli -run 'TestApps/generics/'
    grep -l 'SUSPECTED BUG' internal/cli/testdata/apps/generics/queries/*.txtar

A call-site marker sits on the callee name (`validate./*@inferred*/Check(id)`)
or on its `(`; the package qualifier is not accepted (`qualifier_position`).

## Suspected bugs, by cause

Each query's comment has the details; several queries share one cause.

1. **Generic-instance hop collapsed in paths** (`check_explicit_call` and the
   other "path only" queries): when an origin reaches the Target through a call
   made *inside* a generic instance, the instance's hop disappears and the outer
   call site is labelled "call to" the inner callee. Non-generic code keeps
   both hops. The Error Set is unaffected.
2. **`Unwrap` of an instantiated generic type is "not modelled"**
   (`deep_wrap_decl`, `deep_wrap_chain_depth`, `wrap_generic_decl`,
   `getorload_alias_call`, `save_user_call`, `save_tags_call`, `save_decl`,
   `each_decl`): the wrapped origins are replaced by `unresolved(memory)`.
   The non-generic equivalent is modelled.
3. **Call-site Target inside a generic function fails** (`check_inner_call`):
   exit 2 "no SSA call instruction" when the generic is also called from
   another generic body with a type parameter as type argument.
4. **No narrowing of callback Candidates at a call site** (`funcval_call`):
   another call's closure leaks in through the shared instance. The plan's
   narrowing at substitution (S09) is not built yet; not specific to generics.
5. **Range-over-func** (`iter_collect_decl`, `iter_range_direct_decl`,
   `iter_collect_generic_decl`): only nameless "load of escaping variable "
   Unresolved entries at zero-width spans.
6. **Pointer-typed sentinel as a guard operand** (`is_generic_sentinel_decl`,
   `eq_generic_sentinel_decl`): `errors.Is` says "target that is not a
   sentinel"; `==` is ignored without a note. A variable declared with the
   explicit type `error` is handled.
7. **Concrete pointer re-converted to error loses the Sentinel**
   (`type_switch_generic_decl`, `lift_ptr_sentinel_call`): a type-switch
   binding, or a parameter of concrete pointer type, converted back to `error`
   becomes a fresh Concrete Type origin at the conversion.
8. Low confidence: `==` between two parameters is ignored at substitution
   (`skip_plain_sentinel_call`, `skip_comparable_call`);
   `astype_helper_guard_decl` (helper returning `(E, bool)` leaves no
   possibly-handled note).

Recorded limits that are not flagged: `skip_decl` (a result typed by a type
parameter is "no error result" for a declaration Target, per the documented
predicate), `lift_typed_nil_decl` (no `[typed nil]` mark through a call),
`result_field_call` (struct fields are `unresolved(memory)`),
`is_through_instances_decl` (constants are not tracked, so Retry's wrap of a
possibly nil error keeps the entry as possibly handled).

## Queries

"+/-": positive = the listed origins must be in the set; negative = an origin
must be absent, the set is empty, or the Target is rejected.

| Query | Target (args) | What it checks | +/- | Suspected bug |
|---|---|---|---|---|
| `alias_put_call` | `caching.go:@aliasput` | Method call through the alias Scores = cache.Cache[string,int], arguments from the generic alias Pair[K,V] | positive | no |
| `all_decl` | `validate/validate.go:@all` | Declaration of All[T], instantiated explicitly with UserID and by inference with Name; it calls Check(v) generically | positive | yes |
| `all_inner_call` | `validate/validate.go:@allcheck` | Call site Check(v) inside the generic body of All: the Target spans the instances All[UserID] and All[Name] | positive | yes |
| `as_alias_target_decl` | `handling.go:@onlyints` | errors.As whose target is named through the generic alias *errs.Failure[string] | positive | no |
| `as_generic_type_decl` | `failures.go:@onlystrings` | errors.As with a *TypedErr[int] target over classify, which returns fresh *TypedErr[int]/*TypedErr[string] | positive | no |
| `as_generic_wrapper_decl` | `handling.go:@lazyquiet` | errors.As with a *Wrapped[string] target: the generic wrapper itself matches, 0 entries | negative | no |
| `as_other_instantiation_decl` | `handling.go:@notfloats` | errors.As with *TypedErr[float64], an instantiation never returned: nothing removed, no notes | negative | no |
| `astype_helper_guard_decl` | `failures.go:@viaastype` | Guard through the generic helper AsType[*TypedErr[string]] returning (E, bool) | positive | yes |
| `builtin_min_position` | `validate/validate.go:@min` | the position is on the builtin min inside the generic InRange: not a call to a function | negative | no |
| `check_decl` | `validate/validate.go:@check` | Declaration of Check[T Validator], instantiated with UserID, Name, *Email, Validator, model.User and model.Tag | positive | yes |
| `check_explicit_call` | `validation.go:@explicit` | Call site of the explicit instance Check[validate.Name]: must narrow to Name.Validate | positive | yes |
| `check_iface_call` | `validation.go:@iface` | Check instantiated with the interface type Validator itself: v.Validate() is a dynamic call in that instance | positive | yes |
| `check_inferred_call` | `validation.go:@inferred` | Call site of the inferred instance Check[UserID]: must narrow to UserID.Validate | positive | yes |
| `check_inner_call` | `validate/validate.go:@checkcall` | Call site v.Validate() inside the generic body of Check (static in five instances, dynamic in Check[Validator]) | positive | yes |
| `classify_decl` | `failures.go:@classify` | Baseline: generic error types as origins and sentinels by instantiation | positive | no |
| `deep_wrap_chain_depth` | `-chain-depth=2 failures.go:@deepwrap` | Same Target with -chain-depth=2: the 3-link chain does not fit | positive | yes |
| `deep_wrap_decl` | `failures.go:@deepwrap` | Two instantiations of the generic wrapper around a %w wrap | positive | yes |
| `drop_as_typeparam_call` | `failures.go:@dropints` | Drop[*TypedErr[int]] guards with errors.As on a variable of the type parameter E | positive | no |
| `drop_decl` | `errs/errs.go:@drop` | Declaration of Drop[E error] (single instance Drop[*TypedErr[int]]): returns its parameter unless errors.As matches E | positive | no |
| `each_decl` | `seq/seq.go:@each` | Declaration of Each[T]: instances Each[Name], Each[UserID] and Each[model.Tag] (bound generic method value r.Save from repo.SaveAll) | positive | yes |
| `each_explicit_inst_callback` | `pipeline.go:@eachname` | An explicit generic instantiation, validate.Check[validate.Name], passed as the func(T) error callback of Each | positive | yes |
| `each_inferred_inst_callback` | `pipeline.go:@eachid` | An inferred generic instantiation (validate.Check, T=UserID from Each's parameter type) passed as callback | positive | yes |
| `either_right_decl` | `result/result.go:@right` | Method of a generic type that is never instantiated | negative | no |
| `eq_generic_sentinel_decl` | `failures.go:@eqrange` | `err == errs.ErrIntRange` against a sentinel-by-instantiation (pointer variable of type *TypedErr[int]) | positive | yes |
| `equal_helper_guard_decl` | `caching.go:@viaequal` | ErrNotFound is handled through the generic helper errs.Equal[error] (a == on a comparable type parameter) | negative | no |
| `evict_uncalled_decl` | `cache/cache.go:@evict` | Generic method that no code calls; instances exist only because *Cache values are converted to Flusher | negative | no |
| `find_retry_decl` | `caching.go:@finduser` | Retry[User] runs a closure over (*Repo[UserID,User]).Find (range-over-int loop, error kept in a local) | positive | yes |
| `flush_iface_call` | `caching.go:@flush` | Generic methods called through the non-generic interface Flusher (3 Candidates, one contributing nothing) | positive | no |
| `flush_iface_fanout` | `-fanout=1 caching.go:@flush` | Same call site with -fanout=1: the entry through the 3-Candidate dynamic call is grouped | positive | no |
| `funcval_call` | `pipeline.go:@funcval` | Call through a generic function value conv := seq.Map[int, string] | positive | yes |
| `getor_eq_guard_call` | `caching.go:@getor` | GetOr[K comparable, V] handles ErrNotFound with == inside the generic body and returns nil | negative | no |
| `getorload_alias_call` | `caching.go:@lazy` | GetOrLoad replaces ErrNotFound (== guard) by the loader's error, wrapped by the generic Wrap[string] | positive | yes |
| `iface_method_position` | `validate/validate.go:@ifacemethod` | the position is on the method of the constraint interface Validator, which has no body | negative | no |
| `inrange_call` | `validation.go:@rangeid` | Call site of InRange[UserID]: must narrow to that instance | positive | no |
| `inrange_decl` | `validate/validate.go:@inrange` | InRange[N] returns &TypedErr[N]: a generic error type instantiated with the type parameter (min/max builtins are noise) | positive | no |
| `is_generic_sentinel_decl` | `failures.go:@notrange` | errors.Is against the sentinel-by-instantiation ErrIntRange | positive | yes |
| `is_through_instances_decl` | `handling.go:@quietfind` | errors.Is over findUser: Retry wraps a possibly nil `last`, so ErrNotFound is kept, possibly handled | positive | no |
| `is_wrapped_instance_decl` | `handling.go:@missingok` | errors.Is sees ErrNotFound through the %w wrap of the generic method (*Repo).Find: 0 entries | negative | no |
| `iter_collect_decl` | `pipeline.go:@collectports` | Range-over-func: Collect ranges over the iter.Seq2[int, error] built by Parse(src, parsePort) and returns the yielded error | positive | yes |
| `iter_collect_generic_decl` | `seq/iter.go:@collect` | Declaration of the generic Collect[T] (one instance, Collect[int]); the error comes out of the iterator parameter | positive | yes |
| `iter_range_direct_decl` | `pipeline.go:@firstbad` | Range-over-func directly in the Target: for _, err := range seq.Parse(src, parsePort) { return err } | positive | yes |
| `keys_no_error_call` | `main.go:@keys` | call site of seq.Keys, a generic function that returns no error | negative | no |
| `lift_decl` | `errs/errs.go:@lift` | Declaration of Lift[E error]: its only error is the parameter e (instances Lift[*TypedErr[int]] and Lift[error]) | positive | no |
| `lift_ptr_call` | `failures.go:@liftptr` | Type parameter constrained by error: Lift[*TypedErr[int]](e) converts E to error | positive | no |
| `lift_ptr_sentinel_call` | `handling.go:@liftrange` | Lift[*TypedErr[int]] called with the sentinel ErrIntRange: the sentinel identity must survive | positive | yes |
| `lift_sentinel_call` | `failures.go:@liftsent` | Lift instantiated with E = error and a sentinel argument | positive | no |
| `lift_typed_nil_decl` | `failures.go:@liftnil` | Lift of a nil *TypedErr[int] variable: the result is a non-nil error holding a nil pointer | positive | no |
| `limits_decl` | `validation.go:@limits` | Non-generic caller of three type-set instances (Positive[int], InRange[UserID], InRange[float64]) | positive | no |
| `lparen_ptr_instance_call` | `validation.go:@lparen` | Call-site Target given by the "(" of an inferred instantiation, Check[*Email] (pointer-receiver method) | positive | yes |
| `map_callback_call` | `seq/seq.go:@mapcb` | Call site f(x) of the callback parameter inside the generic body of Map: spans all instances | positive | no |
| `map_closure_call` | `pipeline.go:@maplen` | Call site of Map[Name,int] with a closure wrapping Name.Validate | positive | yes |
| `map_decl` | `seq/seq.go:@map` | Declaration of Map[T, U], instantiated as Map[string,int] (parsePort), Map[Name,int] (closure) and Map[int,string] (two closures) | positive | no |
| `map_named_func_call` | `pipeline.go:@mapport` | Call site of Map[string,int] with the named function parsePort as callback | positive | yes |
| `method_expr_call` | `handling.go:@methodexpr` | Call through a method expression of an aliased instantiation, (*Scores).Get(c, k) | positive | no |
| `method_value_call` | `caching.go:@methodval` | Call through a bound method value get := c.Get of *Cache[UserID,Name] | positive | no |
| `nested_first_filter` | `pipeline.go:@nested` | Nested generic calls First(Filter(...)): Filter's (slice, error) results are First's arguments | positive | yes |
| `new_ptr_call` | `validation.go:@ptr` | Pointer-constraint pattern New[T any, P Ptr[T]] instantiated as New[Email, *Email]; P(v).Validate() is (*Email).Validate | positive | yes |
| `newerr_decl` | `errs/errs.go:@newerr` | Declaration of the generic constructor NewErr[T], whose result type is the concrete *TypedErr[T] | positive | no |
| `option_unwrap_call` | `caching.go:@optunwrap` | Option[Name].Unwrap is a method named Unwrap on a non-error generic type (not a Wrap) | positive | no |
| `parse_no_error_result` | `seq/iter.go:@parse` | Parse returns iter.Seq2[T, error]; it has no error result of its own | negative | no |
| `pattern_restricted_decl` | `-p ./seq seq/seq.go:@map` | Only ./seq is loaded (-p), so main and its instantiations of Map are not in the program | negative | no |
| `positive_call` | `validation.go:@posint` | Type-set constraint (~int \| ~int64 \| ~float64): call site of Positive[int] | positive | no |
| `promoted_put_call` | `caching.go:@promoted` | Method promoted from the embedded generic *Cache[K,V] through TTL[string,int] | positive | no |
| `put_decl` | `-debug=target cache/cache.go:@put` | Declaration of the generic method (*Cache[K,V]).Put | positive | no |
| `qualifier_position` | `validation.go:@qualifier` | the position is on the package qualifier of validate.Check(e), the first byte of the call expression | negative | no |
| `rejected_decl` | `handling.go:@rejected` | Caller converting the *TypedErr[int] result of NewErr[int] to error | positive | no |
| `result_field_call` | `caching.go:@resget` | Result[int] carries the error of parsePort in a struct field and Get reads it back | negative | no |
| `save_decl` | `repo/repo.go:@save` | Declaration of the generic method (*Repo[ID,T]).Save with two instances (User and Tag) | positive | yes |
| `save_tags_call` | `caching.go:@savetags` | SaveAll[string,Tag] passes the bound generic method r.Save to seq.Each | positive | yes |
| `save_user_call` | `caching.go:@saveuser` | Generics across packages: (*Repo[UserID,User]).Save -> Check[User] -> User.Validate -> Check[UserID]/Check[Name], then Put under Wrap[UserID] | positive | yes |
| `skip_comparable_call` | `failures.go:@skiprange` | Skip[T comparable] instantiated with T = error: result type T is an error result, v == skip against ErrIntRange | positive | yes |
| `skip_decl` | `errs/errs.go:@skip` | Declaration of Skip[T comparable], whose result type is the type parameter (only instance Skip[error]) | negative | no |
| `skip_plain_sentinel_call` | `failures.go:@skipmissing` | Skip with inferred T = error and the errors.New sentinel ErrNotFound: must be empty | negative | yes |
| `tagged_no_unwrap_decl` | `caching.go:@tag` | errs.Tag wraps the cache error in Tagged[string], a generic struct embedding error with no Unwrap | negative | no |
| `total_decl` | `pipeline.go:@total` | Reduce[int,int] with a closure calling Positive[int] | positive | yes |
| `tparam_position` | `validate/validate.go:@tparam` | the position is on a type parameter name, which is neither a function name nor a call | negative | no |
| `ttl_flush_decl` | `cache/ttl.go:@ttlflush` | (*TTL[K,V]).Flush swallows ErrEmpty of the embedded generic Flush with errors.Is: 0 entries | negative | no |
| `ttl_get_call` | `caching.go:@ttlget` | TTL[string,int].Get shadows the embedded Get and delegates to it | positive | yes |
| `type_switch_generic_decl` | `failures.go:@describe` | Type switch over the instantiations: *TypedErr[string] is wrapped, *TypedErr[int] returns nil | positive | yes |
| `typearg_position` | `validation.go:@typearg` | the position is on the explicit type argument of Check[validate.Name](n) | negative | no |
| `typedecl_position` | `errs/errs.go:@typedecl` | the position is on the name of the generic type TypedErr, not on a function or call | negative | no |
| `unused_decl` | `validate/validate.go:@unused` | Declaration of a generic function that is never instantiated | negative | no |
| `unused_inner_call` | `validate/validate.go:@unusedcall` | Call site inside a never-instantiated generic function: unresolved(no-instances) | negative | no |
| `walk_recursive_call` | `-chain-depth=2 pipeline.go:@walk` | Generic mutual recursion Walk[Name] <-> walkKids[Name], wrapping with "kid %d: %w" on each level | positive | yes |
| `wrap_generic_decl` | `errs/errs.go:@wrap` | Declaration of the generic wrapper Wrap[C] (instances for int, string and UserID contexts) | positive | yes |
