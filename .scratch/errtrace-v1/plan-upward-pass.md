# Upward pass and Membership Queries: implementation plan

Label: wayfinder:plan
Map: [errtrace v1 map](./map.md) · Glossary: [GLOSSARY.md](../../GLOSSARY.md) · Base plan: [plan.md](./plan.md)
Date: 2026-11-13 · Base commit: `bc175a3`

## Goal

Answer "can this Target return error X?" with a verdict that stays useful when
the Error Set has Unresolved entries.

Today errtrace works **downward**: from the Target, through summaries, to Error
Origins. It is precise but gives up at holes (Unresolved). This plan adds an
**upward** pass: from every place that produces X, push a "may hold X" mark
forward through the program. The upward pass never gives up; when unsure it
keeps the mark. So:

- downward = lower bound (what it finds is witnessed by a Propagation Path),
- upward = upper bound (what it does not reach cannot hold X).

| Downward finds X | Upward reaches the Target | Verdict |
|---|---|---|
| yes, not possibly handled | (not needed) | **yes** |
| only possibly-handled entries | (not needed) | **maybe** (possibly handled) |
| no | no | **no**, whatever holes the Error Set has |
| no | yes, and at least one Unresolved entry is a Carrier | **maybe**, listing only the Carriers |
| no | yes, but no Unresolved entry is a Carrier | **no** (the downward pass was complete where X could arrive) |

## New glossary terms (add to GLOSSARY.md)

- **Membership Query**: a Query that asks whether one given error (a Sentinel
  or a Concrete Type) is in the Target's Error Set.
- **Verdict**: the answer of a Membership Query: yes, no or maybe.
- **Mark**: the upward pass's fact "this value or memory location may hold X".
- **Carrier**: an Unresolved entry whose place is marked, so X could come
  through it.

Use these terms in identifiers, comments, output and tests.

## CLI surface

```
errtrace -is <pkgpath>.<Name> <target>     # X is a Sentinel, e.g. -is io.EOF
errtrace -as '<type>' <target>             # X is a Concrete Type, e.g. -as '*io/fs.PathError'
```

- `-is` and `-as` are mutually exclusive. Without either, behaviour and output
  are byte-for-byte what they are today (all existing fixtures stay unchanged).
- The type of `-as` is written with the full package path (`*io/fs.PathError`,
  `example.com/m.MyErr`). Aliases resolve as the rest of errtrace does
  (`flow`'s `unalias`): `*os.PathError` means `*io/fs.PathError`.
- X not found in the loaded program (no such variable or type, or the variable
  is not a package-level variable of an error type): usage-style error, exit 2,
  with a message that names what was looked up.
- Exit codes in Membership mode: `0` yes, `1` no, `3` maybe, `2` errors (as
  today). Document them in the usage text.

## Output (text)

First line, in the span-line style of the existing header:

```
<target-span>: <X> in Error Set of <target>: yes
<target-span>: <X> in Error Set of <target>: no
<target-span>: <X> in Error Set of <target>: maybe: <K> of <N> unresolved could carry it
<target-span>: <X> in Error Set of <target>: maybe: possibly handled
```

Then:

- **yes**: the matching entries, rendered exactly as today's entries (wrap
  chain, possibly handled, path).
- **maybe (carriers)**: each Carrier rendered as today's Unresolved entry, plus
  one extra section that shows how the Mark got there:

  ```
  	marked via (origin of X → here):
  		<span>: <step description>
  ```

  Carriers are ordered by Propagation Path length (nearest the Target first),
  then by the existing order.
- **maybe (possibly handled)**: the matching entries.
- **no**: just the header line. If the Target was marked but no entry is a
  Carrier, add a second line saying so (`\tX reaches the target only through
  values the Error Set resolved without it`), so the user can tell the two
  kinds of "no" apart.

`-fanout` grouping does not apply in Membership mode. Pin the exact format in
fixtures and document it in the `render` doc comment, as `Text` does.

## Architecture

New package `internal/mark` (the upward pass). It must not depend on `summary`
or `query`; it depends on `ssa`, `resolve`, `flow` (for `Intrinsic`, the
Sentinel canonicalisation in `flow/sentinels.go`, and `fmtverb` use) and
`errset` only if needed.

```go
package mark

// Subject is the error X of a Membership Query.
type Subject struct { Sentinel *ssa.Global; Type types.Type } // exactly one set

// Result is the outcome of the upward pass.
type Result struct{ ... }
func Run(ctx context.Context, prog *ssa.Program, res Callees, subj Subject) (*Result, error)
func (r *Result) Marked(v ssa.Value) bool
func (r *Result) ResultMarked(fn *ssa.Function, index int) bool
func (r *Result) Trace(v ssa.Value) []Step   // origin of X → v, first-found reason chain
```

`query` gets a `Membership(...)` entry point (or `Options.Subject`) that runs
the existing downward Query unchanged, runs `mark.Run`, and combines them into
a Verdict with the matching entries or the Carriers. `cli` only parses the
flags, resolves X, and picks the renderer and exit code. `render` gets a
`Membership` text function.

### Seeds

- **Sentinel X**: every load (`*ssa.UnOp` with `Op == token.MUL` on the
  `*ssa.Global`) of X in any function with a body. A load used only in a
  comparison never propagates, so it needs no special case. Aliases
  (`var ErrY = ErrX`) fall out of the Global location rule below; also check
  what `flow.Sentinels`' `Canon` does and stay consistent with it.
- **Concrete Type X = T**: every SSA value whose static type is identical to T
  (after `unalias`) is marked. In practice: seed at every `MakeInterface` whose
  operand has type T, and treat any value of static type T as marked when it is
  asked about (a Target may have a result of static type `*T`).

Scan **all** functions with bodies (`ssautil.AllFunctions`), not only the ones
VTA calls reachable: the upward pass must be an upper bound.

### Propagation rules (forward, value → its users)

Marks live on SSA values and on abstract memory **locations**.

| Instruction using a marked value | Effect |
|---|---|
| `Phi`, `ChangeInterface`, `MakeInterface`, `ChangeType`, `Convert`, `Extract`, `Slice` | result marked |
| `TypeAssert` | result marked unless the asserted type provably cannot hold X |
| `Store` to an `Alloc` | location = that Alloc; all loads of it marked (include closures that capture it) |
| `Store` through `FieldAddr`, or a `Field`/composite-literal field | location = (named struct type, field index), **program-wide** |
| `Store` to a `Global` | location = that Global |
| `Store` through `IndexAddr`, `MapUpdate`, `Send`, append to a slice, store through any other pointer | location keyed by the container's/pointee's type (type-based aliasing) |
| `MakeClosure` binding | the closure's `FreeVar` marked |
| call argument | see "Calls" |
| `Return` | see "Calls" |
| `Panic`, comparison, anything else | nothing |

Loads (`UnOp MUL`, `Field`, `Index`, `Lookup`, `UnOp ARROW`, `Select`, `Next`
on a map range) from a marked location are marked.

Only propagate into values whose static type can hold X (an interface X's
dynamic type implements, T itself, or a container/struct of such). This keeps
the pass from flooding through `any` plumbing. Measure, and report in the
commit message, how many functions end up marked for `-is io.EOF` on
`internal/load/load.go:84:6` in this repo.

### Wraps

- `fmt.Errorf`: result marked if a marked operand is at a `%w` verb (use
  `internal/fmtverb`); if the format is not constant, marked if any operand is
  marked. Do not descend into the bodies of intrinsics (`flow.Intrinsic`).
- `errors.Join`, `errtrace.Wrap`: result marked if any operand is.
- **Custom wrapper types**: when a marked value is stored into a field of a
  struct type S where S or *S implements `error` and has an `Unwrap`, `Is` or
  `As` method, S becomes a *wrapper of X*: every value of static type S or *S
  is treated as marked from then on (as a Concrete Type seed would be).
- **Unwrapping**: `errors.Unwrap(marked)` and any call of a method named
  `Unwrap` on a marked receiver give a marked result.
- `errors.As(err, target)` with `err` marked: the pointee location of `target`
  is marked (the real body uses reflection, which the pass cannot see).

### Calls (summary-based, so helpers do not leak Marks to every caller)

A naive pass marks the result of `func wrap(err error) error` at every call
site as soon as one caller passes X. Avoid that with tagged Marks:

- A Mark carries a set of tags: `Free` (true in any calling context) or
  `Param(i)` (true when parameter i of the enclosing function holds X).
- Seeds are `Free`.
- When a call site passes a marked argument i to Candidate `f`, activate the
  link (site, i) and, once per (f, i), seed `f`'s parameter i with `Param(i)`.
- When a `Return` of `f` is marked `Free`, mark the result at **every** call
  site of `f` as `Free`. When it is marked `Param(i)`, mark the result only at
  call sites with an activated link (site, i), with the tags the argument has
  in the caller.
- A `Param(i)` Mark stored to a memory location marks the location `Free`
  (memory is context-insensitive by design; a `Param(i)` seed only exists
  because some caller really passed X).
- Captured variables (`FreeVar`) are treated as memory: `Free`.

Callees of a call site:

- `resolve.Resolver.Callees(site)` when it returns any.
- Otherwise (a dynamic call with no Candidates, or a call in a function VTA
  did not analyse) fall back to the **CHA** callees of the site. Expose this
  from `resolve.VTA` (it already holds the CHA graph) behind a small interface
  that `mark` takes; do not change `Resolver`'s meaning for the downward pass.
  This fallback is what makes "no" hold in the presence of `no-candidates`
  Unresolved entries.
- A callee without a body: result marked if any argument is marked and the
  result type can hold X.

`go` and `defer` calls propagate arguments like calls. A deferred closure that
assigns a named result is covered by the Alloc/closure rules; add a fixture.

### Verdict (in `query`)

1. Run the downward Query as today.
2. An entry **matches** X if its leaf is the Sentinel X (same canonical
   variable) or a Concrete Type identical to T. A matching entry with no
   `PossiblyHandled` notes → **yes**. Only possibly-handled matches → **maybe
   (possibly handled)**.
3. Otherwise run the upward pass. If no error result of the Target functions
   (for a call-site Target: the call's result value) is marked → **no**.
4. Otherwise each Unresolved entry is a Carrier if its place is marked:
   - `Site` is an `ssa.Value` → `Marked(site)`;
   - `Site` is a call instruction → the call's value is marked;
   - `depends-on-caller` → the `Param`/`FreeVar` is marked (a `Param` seed
     exists only if some caller passes X; say so in the trace);
   - anything that cannot be mapped to a markable value (`fixpoint-limit`,
     `no-instances`, a truncated chain) → Carrier (conservative).
   Any Carriers → **maybe** with them. None → **no** (with the second line).

A skipped upward pass (verdict already yes) is fine and saves time.

## Steps

Each step leaves `go build ./... && go vet ./... && go test ./...` green.

- **U1. `internal/mark`: intraprocedural core.** Seeds, value propagation,
  Alloc/Global/field/type-keyed locations, wraps. Unit tests on txtar testdata
  in the style of `internal/flow/testdata` (table of "function → marked
  results").
- **U2. Calls.** Tagged Marks, links, Candidates with CHA fallback, bodiless
  callees, closures, `go`/`defer`. Tests must include: the shared-helper case
  (`wrap(err)` called with X by one caller and with something else by another;
  only the first caller's result is marked), recursion, mutual recursion, an
  interface call, a callback.
- **U3. Trace.** Record the first reason of every Mark; `Trace` returns the
  steps from a seed. Deterministic (stable iteration orders everywhere; never
  range over a map to decide which reason wins).
- **U4. `query.Membership` and the Verdict.** Matching, Carriers, ordering.
- **U5. CLI and render.** Flags, resolving X, exit codes, usage text, text
  output, `-debug=timing` line `timing: mark=<d>`. E2E fixtures under
  `internal/cli/testdata/e2e/membership/`, at least: yes (direct), yes (through
  `%w`), no (clean), no (Target has an unrelated hole, the key case), maybe (X
  stored in a struct field and loaded elsewhere), maybe (possibly handled),
  no with the second line (guard removes X), Concrete Type via `-as`, alias
  Sentinel, X not found (exit 2), `-is` with `-as` (usage error).
- **U6. Glossary and docs.** GLOSSARY.md terms; package doc comments; a short
  "Membership Queries" section appended to [plan.md](./plan.md)'s CLI surface.

## Acceptance

- All existing tests and fixtures pass unchanged.
- From the repo root, `errtrace -is io.EOF internal/load/load.go:84:6` and
  `errtrace -is io.ErrNoProgress internal/query/query.go:58:6` finish in under
  10 s wall with a warm build cache, give the same output on repeated runs, and
  the builder states each verdict and why it is right in the final report.
- A hand-checked "no despite holes" example on real code (not a fixture) is
  given in the final report.

## Known limits (state them in the `mark` package doc)

- Reflection, `unsafe`, cgo and assembly can move X where the pass cannot see.
- A type with a custom `Is` method can match X without holding it.
- Guards are ignored upward by design; the downward pass decides them.
- Type-keyed locations are coarse: expect false "maybe", never false "no".

## Conventions

Follow "Conventions for every step" in [plan.md](./plan.md) (read it). In
short: glossary terms everywhere, doc comments on every exported identifier,
deterministic output, txtar fixtures, no new dependencies beyond
`golang.org/x/tools`. Do not edit files under `.scratch/` other than the U6
addition. Commit on the worktree branch, one commit per step, message
`U0n: <what>`; never push.
