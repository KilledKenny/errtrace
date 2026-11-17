# Closing the cheap holes: implementation plan

Label: wayfinder:plan
Map: [errtrace v1 map](./map.md) · Glossary: [GLOSSARY.md](../../GLOSSARY.md) · Base plan: [plan.md](./plan.md)
Date: 2026-11-13 · Base commit: `bc175a3`

## Goal

Cut the number of `unresolved(memory)` entries in real Error Sets by resolving
the few patterns that cause most of them, without ever dropping an entry that
is really a hole.

Baseline, measured at `bc175a3` from the repo root:

```
errtrace internal/load/load.go:84:6    → 180 entries, 56 unresolved (50 memory, 6 no-candidates)
errtrace internal/query/query.go:58:6  → 2 entries, 1 unresolved (memory: value of TypeAssert not modelled)
```

Most frequent details in the first one:

| Detail | Count |
|---|---|
| `load of escaping variable err` | 11 |
| `load of field <f> of <wrapper type>` (`*json.SemanticError.Err`, `*jsontext.decodeBuffer.peekErr`, `*exec.wrappedError.err`, `*fmt.wrapError.err`, `*jsontext.ioError.err`, `*strconv.NumError.Err`, `*exec.Error.Err`, `*os.SyscallError.Err`, …) | ~35 |
| `value of TypeAssert not modelled` | few, but trivial |

**Rule for the whole plan:** a hole may be replaced by the values that can be
there only if the analysis sees **every** way the place can be written. If it
sees only some, it adds the values it found **and keeps** an Unresolved entry
for the rest, with a detail that says what was not seen. Never trade a hole
for a silently incomplete Error Set.

## Steps

Each step leaves `go build ./... && go vet ./... && go test ./...` green, adds
fixtures, and reports the two baseline numbers before and after.

### H1. `TypeAssert`

Where: `internal/flow/analyze.go` (the walk's fallthrough that emits
`value of TypeAssert not modelled`, and `extractDetail` for the comma-ok form).

- `x.(T)` and `v, ok := x.(T)`: walk the operand `x`; the result carries the
  operand's values.
- Filter where it is certain: a Sentinel or Concrete Type whose dynamic type
  cannot satisfy the asserted type is dropped (concrete asserted type: types
  must be identical; interface asserted type: the dynamic type must implement
  it). Symbolic values (Param, FreeVar, CallRef) and Unresolved values pass
  through unfiltered; if the existing Guarded machinery (see
  `internal/flow/guards.go`, type-switch guards) can express the filter for
  them, use it, otherwise leave them unfiltered and say so in a comment.
- Check how type switches are lowered (they are `TypeAssert` chains) and make
  sure this does not double-count or contradict the existing type-switch guard
  handling. Add fixtures for: plain assert, comma-ok, assert to a concrete
  type that filters, assert to an interface, type switch.

### H2. Escaping local variables

Where: `(*analyzer).local` in `internal/flow/analyze.go`, which gives up on
`alloc.Heap` (`load of escaping variable …`) and on `whose address escapes`.

A heap Alloc is usually a variable captured by a closure (`err` assigned in a
`defer func() { ... }()` or a callback) or a named result with defers.

- Collect every `Store` to the Alloc: in the function itself and in every
  closure that captures it (follow `MakeClosure` bindings to the closure's
  `FreeVar`, recursively for nested closures). The existing
  `internal/flow/freevar.go` already does the mirror image (a closure reading a
  captured variable looks at the creating function's stores); reuse its
  machinery rather than writing a second one.
- The load's values are the join of the stored values. A store inside a
  closure is evaluated in the closure (its Params, FreeVars and CallRefs are
  the closure's own); see how `Captured` sources and `ArgFn` carry provenance
  across the function boundary in `internal/summary` and extend that, so the
  Propagation Path shows the hop into the closure.
- The Alloc stays a hole (found values **plus** an Unresolved with detail
  `variable <name> may be written through an escaped pointer`) when its
  address goes anywhere else: passed to a call, stored in memory, converted,
  sent, or captured by a closure that itself escapes in a way the analysis
  cannot follow to its stores. Passing `&err` to a function is the common case
  that must stay a hole.
- Guards: the existing analysis applies guards per return path at the point of
  use. A value stored in a closure has no path relation to the load; do not
  apply the closure's path guards to it, and do not let the load's guards be
  lost. Add a fixture that pins the behaviour.
- Fixtures: closure assigns captured `err` and the function returns it;
  deferred closure assigns a named result; two closures; nested closure;
  `&err` passed to a call (stays Unresolved, plus found values); goroutine
  closure.

### H3. Struct fields: program-wide field locations

Where: the field-load cases of the walk in `internal/flow/analyze.go` (emit
`load of field …`), and `internal/summary` (solver, eval).

Today a load of `p.f` is resolved only when `p` is a composite literal built
in the same function (`constructed` in `internal/flow/wrap.go`). Everything
else is a hole. Model each field as one program-wide location:

- **Location** = (named struct type, field index); embedded structs are their
  own type's fields. Its value is the join of every value stored to that field
  anywhere in the program.
- **Index of writers**, built once per Query, lazily, over every function with
  a body that the Resolver calls reachable (plus the Target functions): for
  each location, its `Store`s through `FieldAddr`, and the field operands of
  composite literals (they are `Store`s through `FieldAddr` of the literal's
  Alloc in SSA; verify).
- **A new symbolic value** in `errset` (say `FieldRef`: type, field index,
  load site) that `flow` emits for a field load it cannot resolve locally, as
  `CallRef` stands for a call result. The summary solver resolves a `FieldRef`
  by evaluating each writer's stored value in the writer's function
  (`flow.ValueFacts`) and then resolving those facts as it resolves any
  function's local facts.
- **Demand graph**: a field location is a node, like a function. A function
  that loads the field depends on the location; the location depends on its
  writer functions' stored values (and through them on their callees). SCCs
  and the capped fixpoint must cover cycles through locations
  (`internal/summary/scc.go`, `solver.go`). The caps that exist
  (`fixpoint-limit`) apply unchanged.
- **Writer's symbolic values.** A stored value that is a `Param` or `FreeVar`
  of the writer (the constructor pattern, `func NewE(err error) *E { return
  &E{Err: err} }`) cannot be substituted: the location has no call context.
  Resolve it by joining the arguments of **every** call site of the writer
  (callers found by scanning the reachable functions' call sites through the
  Resolver; build that reverse index once, lazily). Follow at most 3 levels of
  callers; beyond that, or if the writer has a caller the analysis cannot see
  (it is a root, its address is taken as a function value with no resolved
  call site, it is exported from a package and unreachable code may call it:
  use Resolver reachability, not export status), keep an Unresolved with
  detail `field <f> of <T> written from parameter <p> of <fn>`.
- **Stays a hole** (found values plus an Unresolved, detail says why) when the
  field can be written in ways the index does not see:
  - a whole-struct store (`*p = v`, or a `Store` of a struct value containing
    the field) whose value is not a composite literal the walk can open;
  - the struct, or a pointer to the field (`&p.f`), is passed to `reflect`,
    `unsafe`, `encoding/json`-style decoders (any call taking it as `any`/an
    interface in a bodiless or reflective callee: use a simple, conservative
    rule and document it), or cgo;
  - the type is a type parameter or the field belongs to an anonymous struct
    (out of scope: keep today's behaviour).
- **Propagation Path** of an entry that came through a field: hop 0 is where
  the value enters the writer function, then the writer's store, then the
  load, then the usual calls up to the Target. Extend `summary.Source`
  (a new `SourceKind`, alongside `Captured`) and `summary/path.go` so the path
  reads sensibly in the text output; pin it in a fixture.
- **Interaction with Wraps built by `constructed`**: a locally built literal of
  a type with a modelled `Unwrap` is already a Wrap of its field's values.
  Keep that path as is (it is more precise than the location). The location is
  for loads from pointers that were not built locally: receivers, parameters,
  results of type asserts and calls.
- **Zero value**: a field no writer stores to is nil: the load contributes
  nothing (no entry), as a nil constant does today.
- Fixtures: constructor in another function; method `Unwrap` reading the
  receiver's field, reached through `errors.Unwrap`; manual unwrap after a
  type assert (`if e, ok := err.(*E); ok { return e.Err }`, which also needs
  H1); two writers; a writer in another package; a writer storing its
  parameter with two callers; a recursive type (field holds a value built
  from the same field: fixpoint); a whole-struct store (stays a hole plus found
  values); a field written through an escaped `&p.f`.

### H4. Measure and report

- Re-run the two baseline commands and report entries / unresolved / the
  detail histogram, before and after each of H1–H3.
- `time errtrace -debug=timing internal/load/load.go:84:6` must stay under
  10 s wall with a warm build cache. If H3 misses it, say so in the report and
  keep the step (correctness first), but list where the time goes.
- For three entries that used to be Unresolved in `load.Load` and are now
  resolved, hand-check against the source that the values shown are right and
  that nothing was lost, and include the check in the final report.

## Not in this plan

Map, slice, array and channel elements; loads through arbitrary pointers;
globals other than Sentinels. They keep today's Unresolved entries.

## Conventions

Follow "Conventions for every step" in [plan.md](./plan.md) (read it, and its
Architecture section). In short: glossary terms everywhere, doc comments on
every exported identifier, deterministic output (never let map iteration
order reach the output), txtar fixtures, no new dependencies. Existing
fixtures whose expected output changes because a hole is now resolved are
updated with `-update` **after checking each diff by hand**; list every
changed fixture and why in the commit message. Do not edit files under
`.scratch/`. Do not touch `internal/cli` flags or add new output sections
(another branch is adding `-is`/`-as` there). Commit on the worktree branch,
one commit per step, message `H0n: <what>`; never push.
