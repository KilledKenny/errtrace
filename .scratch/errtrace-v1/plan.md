# errtrace v1: implementation plan

Label: wayfinder:plan
Map: [errtrace v1 map](./map.md) · Glossary: [GLOSSARY.md](../../GLOSSARY.md)
Date: 2026-11-09 · Branch: `errtrace-v1` · Module: `github.com/killedKenny/errtrace` (go 1.24.3)

This plan turns the map's destination into ordered build steps. Each step is
done by a fresh **builder** agent, checked by a **reviewer** agent, and
repaired by a **fixer** agent until the reviewer approves. Every step is meant to
fit one agent session and leaves the repo green.

**How to use this document (builders, reviewers, fixers):** read
[Conventions for every step](#conventions-for-every-step), the
[Architecture](#architecture) section, and your step. Skim the
[Provisional decisions](#provisional-decisions). Use the glossary terms (Query,
Target, Error Set, Error Origin, Sentinel, Concrete Type, Opaque Error, Wrap,
Wrap Chain, Propagation Path, Candidate, Unresolved) everywhere, in code
identifiers, comments, output and tests. The research notes are on local
branches and can be read with `git show research/<name>:research/<name>.md`
(names: `prior-art`, `callgraph-algorithms`, `gopls-io-conventions`,
`error-flows-in-ssa`). Never merge or check out those branches.

---

## Contents

1. [Provisional decisions](#provisional-decisions)
2. [Architecture](#architecture)
3. [CLI surface (v1)](#cli-surface-v1)
4. [Output contract](#output-contract)
5. [Conventions for every step](#conventions-for-every-step)
6. [Steps](#steps)
7. [Coverage of the map](#coverage-of-the-map)

---

## Provisional decisions

The user chose to start implementation with tickets 05–10 still open. The
choices below are **provisional**: they are the defaults the steps build on.
Each one names what would make us revisit it. When a ticket is later resolved,
update its decision here and in the ticket, and change the affected steps.

### P05: Candidate resolution strategy (ticket 05)

- **Choice.** Use VTA the way govulncheck runs it: `cha.CallGraph(prog)` → forward
  slice from the roots → `vta.CallGraph(slice, cha)` → forward slice again on
  the VTA graph → `vta.CallGraph(slice2, vta1)` → `DeleteSyntheticNodes()`
  (`golang.org/x/vuln@v1.9.0/internal/vulncheck/utils.go:62-88`). It sits behind
  errtrace's internal `resolve.Resolver` interface, so it can be swapped. Variants
  (2) "VTA seeded with RTA" and (3) "forked VTA exposing type sets" are **not**
  built in v1. CHA is used only as VTA's initial graph and is never offered as a
  mode (the map rules out an all-implementers mode).
- **Rationale.** This variant had the best measured precision (3.2 average callees
  per dynamic site against RTA's 28.3; 3 / 5 / 1 Candidates at `err.Error()` /
  `io.Reader.Read` / a `func() error` callback). It is a production recipe, and it
  needs one public API. Its 0.6 s is small next to the 1–2 s spent loading packages.
  Errtrace needs its own value tracking for sentinels and `%w` anyway, so the
  fork's type sets would not remove any work.
- **Revisit if:** `-debug=timing` shows the call-graph phase above ~30% of the
  10 s cold budget on a medium module (then try the RTA-seeded variant, which was
  ~1.7× faster); memory blows up as in golang/go#67838; or real interface-heavy
  code shows Candidate sets that are clearly too noisy.

### P06: Output format (ticket 06)

- **Choice.** Text output is gopls-style span lines `/abs/path.go:L:C-C`
  (byte columns), each followed by an indented tree with the Origin, the Wrap
  Chain and the Propagation Path. Entries are sorted deterministically. Large
  fan-out is collapsed by **grouping** entries that pass through the same
  high-fan-out dynamic call site; `-fanout=0` turns this off. `-json` prints a
  stable, versioned schema (`"schema": "errtrace/v1"`) that never collapses
  anything. The exact grammar is in the [Output contract](#output-contract).
- **Rationale.** Span lines can be fed back into errtrace, gopls and editors
  (`gopls-io-conventions`, §2.1). A tree keeps each Origin next to its own Wrap
  Chain and path, so every entry can be checked on its own. Grouping by dynamic
  site matches where the fan-out actually comes from (one interface call with many
  Candidates).
- **Revisit if:** the first real Queries show that grouping hides information
  people need, that paths are too long to read (switch to only the hops between
  packages), or that the later LSP mode needs a different shape.

### P07: Guard filtering rules (ticket 07)

- **Choice.** Recognise `==` / `!=` (against a sentinel or `nil`, including value
  `switch` chains), `errors.Is`, `errors.As`, comma-ok type assertions and type
  switches. Apply them **per return path** using dominating branch conditions,
  and per phi edge. The body outcomes "return nil", "replace" and "wrap" fall out
  of this filtering. Cases errtrace can't decide (guards through helper functions,
  custom `Is`/`As` methods, Opaque or Unresolved values under `errors.Is`, guards
  whose effect reaches the return only through a merge that isn't a phi edge) **keep**
  the entry and mark it `possibly handled` with the guard's position and a
  reason. A guard never makes an entry disappear unless errtrace can prove it.
  The full rule table is in [S07](#s07-guard-filtering-per-return-path).
- **Rationale.** SSA has no refined copy of `err` after a check
  (`error-flows-in-ssa`, §3), so per-path filtering is the only option. Keeping
  undecidable cases follows the map's rule that the Error Set never claims more
  completeness than it has.
- **Revisit if:** helper guards (`isRetryable(err)`) turn out to be common enough
  that `possibly handled` marks become noise (then add guard summaries for
  helpers that return a boolean); or users want a flag to hide `possibly handled` entries.

### P08: Analysis architecture (ticket 08)

- **Choice.**
  - **Per-function summaries**, computed **on demand** for the functions the
    Query actually needs (the demand graph) and solved **bottom-up over SCCs**
    (Tarjan), with a fixpoint inside each SCC and an iteration cap.
  - Summaries are **symbolic in parameters and free variables**: `Param(i)` stays
    a placeholder and is substituted with the actual arguments at every call site.
    This gives per-caller precision for values that pass through a function, even
    though VTA merges all callers.
  - **Narrowing at substitution:** facts that a callee got through a dynamic call
    on one of its parameters are tagged with the Candidate. When the caller's
    argument has a complete local type set, facts from Candidates outside that
    set are dropped. This is how per-caller precision is recovered for Candidates.
  - **errtrace's own value tracking** handles Sentinel identity (globals),
    `%w` and `errors.Join` Wrap Chains, and custom `Unwrap`, because VTA tracks
    only types. The call graph only supplies Candidates.
  - **Depth cap on Wrap Chains** (default 8, `-chain-depth`) so loops that wrap
    their own error terminate; truncated chains are marked.
  - **Propagation Paths** are rebuilt from per-fact provenance with a shortest-path
    search that prefers fewer dynamic hops and visits each node once, like
    govulncheck's witness search.
  - **On-disk caching is the last, optional step** ([S14](#s14-optional-on-disk-summary-cache)):
    a whole-program fingerprint, with only complete (not limit-cut) summaries cached.
  - **Toolchain:** stay on Go 1.24.3 and pin x/tools (see P-XT).
- **Rationale.** The prior-art research shows on-demand recursion with a `seen` set
  quietly degrades to "unknown" on cycles (staticcheck), so SCCs are needed.
  On-demand summaries avoid summarising all of the stdlib. Symbolic parameters
  are the cheapest way to get back the context sensitivity that VTA lacks.
- **Revisit if:** summary sizes explode on stdlib-heavy Queries (then widen sets
  past a size cap into `unresolved(fixpoint-limit)`); the cold time misses 10 s
  because of summary work; or coarse cache invalidation makes the cache useless in
  an edit loop (then use per-package fingerprints).

### P09: Query roots (ticket 09)

- **Choice.** Roots are the `main` and `init` functions of the main packages
  among the loaded (pattern-matched) packages, plus all package `init`s. With
  `-tests`, test main packages are added (their generated `main` reaches every
  `Test*`, `Benchmark*`, `Fuzz*` and `Example*` function). If no main package is
  matched, or the Target isn't reachable from the roots, errtrace adds
  **synthetic roots**: every exported function and exported method of exported
  types in the matched non-main packages. A dynamic call on a value that comes from
  a parameter with no Candidates in scope is reported as
  `unresolved(depends-on-caller)` and names the parameter and its type (for
  example "caller-supplied io.Reader (parameter r of example.com/lib.Parse)").
  Error values that come from the Target's own parameters in a declaration Query
  are reported the same way. There is no fallback to all implementers.
- **Rationale.** VTA is only sound from whole-program roots. Rooting it at the
  queried function silently gives 0 Candidates (`callgraph-algorithms`, §6). An
  explicit "depends on the caller" entry is honest. It follows the Unresolved
  rule and does not bring back CHA.
- **Revisit if:** synthetic roots produce misleading Candidates in real libraries;
  users want a `-root` flag to name entry points; or test roots make loading too slow.

### P10: Project and binary name (ticket 10)

- **Choice.** Keep the module path `github.com/killedKenny/errtrace` for now. The
  binary name lives in **one** constant, `BinaryName = "errtrace"`, in
  `internal/cli/name.go`. All messages, usage text and the stderr prefix use it.
  The command directory is `cmd/errtrace`. A rename means changing the constant
  and the directory name (and the module path if wanted). Nothing else should
  hard-code the string `errtrace` (tests compare against the constant).
- **Rationale.** `braces.dev/errtrace` already installs a binary named `errtrace`,
  so a rename is likely before release. The map treats the tool as personal for
  now, so the rename can wait as long as it stays cheap.
- **Revisit:** before any public release or before publishing to a remote.

### P-XT: x/tools version (part of ticket 08)

- **Choice.** Pin `golang.org/x/tools v0.39.1-0.20260109155911-b69ac100ecb7` and
  keep `go 1.24.3`. Don't add a `toolchain` line.
- **Rationale.** It is the newest x/tools that declares `go 1.24.0`. All the
  research ran against it, and it has every API the plan uses
  (`go/callgraph/{cha,vta}`, `go/ssa`, `ssautil`, `astutil`, `txtar`,
  `inspector.Cursor`). v0.52.0 needs Go 1.26.
- **Revisit if:** a needed fix or API only exists in x/tools that needs Go ≥ 1.26.

---

## Architecture

### Pipeline

```
 args ──► cli ──► span.Parse / target symbol
                    │
                    ▼
               load.Load ── go/packages (LoadAllSyntax, Tests?) ──► ssautil.AllPackages(InstantiateGenerics)
                    │                                                 SetDebugMode on module packages, prog.Build()
                    ▼
            target.Resolve ── position/symbol ──► Target{Decl fn(s) | CallSite instr in fn(s)}
                    │
                    ▼
           resolve.Resolver ── roots ──► CHA → slice → VTA → slice → VTA ──► Candidates per call site
                    │
                    ▼
           summary.Solver ── demand graph from Target ──► SCCs bottom-up ──► Summary per function
                    │                 (uses flow.Analyze per function: origins, wraps, guards, defers)
                    ▼
             query.Run ── substitute at Target, narrow, apply limits ──► errset.ErrorSet + Propagation Paths
                    │
                    ▼
            render.Text / render.JSON ──► stdout
```

Phases are timed and printed with `-debug=timing` (stderr only).

### Packages

| Package | Responsibility | First step |
|---|---|---|
| `cmd/errtrace` | `main`, which only calls `cli.Main(os.Args[1:], os.Stdout, os.Stderr)` and exits with its code. | S01 |
| `internal/cli` | Flag parsing, `BinaryName`, exit codes, error messages, orchestrating the pipeline. Its `Run(ctx, Options{Args, Dir, Env, Stdout, Stderr}) int` is what tests call in-process. | S01 |
| `internal/span` | The gopls position grammar (parse) and span formatting; the conversion between `token.Pos` and byte line/column; the JSON span shape. | S01 |
| `internal/fixture` | Test-only helpers (imported only from `_test.go` files): extracting txtar fixtures into a temp module, `@marker` substitution, path masking, golden comparison with `-update`. | S01 |
| `internal/load` | `go/packages` config, SSA build, `Program` (fset, packages, `*ssa.Program`, initial packages, file lookup, span lookup for values and instructions). | S02 |
| `internal/target` | Resolving a Target from a position (S02) or a symbol name (S13). | S02 |
| `internal/errset` | The data model: interned abstract values, Origins, Wraps, Unresolved, entries, Propagation Paths, and the flattening from value trees into Error Set entries. No SSA logic. | S03 |
| `internal/flow` | Intraprocedural analysis of one `*ssa.Function`: error results, walking definitions backwards, intrinsics, guards, defers. It yields local facts that refer to `Param`, `FreeVar` and `CallRef(site, result)`. | S03 |
| `internal/fmtverb` | errtrace's own `fmt` format parser: verbs, `%w`, `%[n]w`, `%%`, flags, `*` widths. | S05 |
| `internal/resolve` | The `Resolver` interface (Candidates per call site, reachability, roots), the static implementation (S04), VTA (S06), roots (S06, S12). | S04 |
| `internal/summary` | The demand graph, Tarjan SCCs, the bottom-up solver, substitution, narrowing, provenance. | S04 |
| `internal/query` | Turns a Target into an Error Set: top-level substitution, call-site narrowing, depth and boundary limits, path search, dedup and ordering. | S03 (minimal), S04 |
| `internal/render` | The text renderer (S03, grouping in S06) and the JSON renderer (S11). | S03 |
| `internal/cache` | Optional on-disk summary cache. | S14 |

The dependency direction is `cli → {target, query, render, load, cache} → summary → {flow, resolve} → errset`.
`errset` and `span` depend on nothing internal. `fmtverb` depends on nothing.

### Data model (`internal/errset`)

All abstract values are **interned** (hash-consed) into a table and named by a
small integer `ValueID`. Set operations and fixpoint equality are then cheap and
deterministic.

| Value kind | Fields | Meaning |
|---|---|---|
| **Origin: Sentinel** | `Var *types.Var`, `Via []*types.Var` (aliases followed, S10), `Mutable bool` (S10) | A package-level error variable returned by identity (`io.EOF`). A pointer-typed global behind `MakeInterface` (`var ErrPtr = &MyErr{}`) is also a Sentinel and also records its concrete type. |
| **Origin: Concrete Type** | `Type types.Type`, `Pos`, `TypedNil bool` | A value of a named type that implements `error`, created at `Pos` (`*fs.PathError`, `ValErr`). `E` and `*E` are different Origins. A typed nil (`MakeInterface` of a nil pointer constant) sets `TypedNil`. |
| **Origin: Opaque Error** | `Constructor` (`errors.New`, `fmt.Errorf`), `Pos`, `Message string` (the constant text, or empty if not constant) | An error with no identity and no inspectable type. |
| **Wrap** | `Kind` (`errorf`, `join`, `unwrap-method`, `braces-wrap`), `Pos`, `Format string` (errorf), `Type` (for custom wrappers), `Inner []ValueID` | A node in a Wrap Chain. `Inner` has 1 entry for `%w`, a custom `Unwrap() error` and `errtrace.Wrap`, and 1..n entries for several `%w`, `errors.Join` and `Unwrap() []error`. |
| **Truncated** | `Inner ValueID` | Marks a chain that was cut at `-chain-depth`. Rendered as `[chain truncated]`. |
| **Unresolved** | `Kind`, `Pos`, `Detail string` | A place the analysis could not see through. The kinds are listed below. It is never dropped. |
| **Param** (symbolic) | `Fn`, `Index`, `PendingGuards []Guard` (S07) | "Whatever the caller passed as parameter i". It only exists inside summaries. At the top of a declaration Query it becomes `unresolved(depends-on-caller)`. |
| **FreeVar** (symbolic) | `Fn`, `Index` | A closure's captured variable. It is resolved from the `MakeClosure` bindings (S06, S08). |
| **CallerSupplied** (symbolic, S12) | `Fn`, `Index`, `Method` | A dynamic call on parameter i that had no Candidates in scope. Substituted upwards. At the top it becomes `unresolved(depends-on-caller)`. |

**Unresolved kinds** (closed set in v1; the JSON schema enumerates them):

| Kind | Raised when | Step |
|---|---|---|
| `pending` | **Temporary**, only S03: a call that isn't followed yet. S04 must remove this kind entirely. | S03→S04 |
| `no-body` | The callee has `Blocks == nil` (assembly, `//go:linkname`, cgo, no syntax loaded). | S04 |
| `no-candidates` | A dynamic call with 0 Candidates that does not come from a parameter. | S04 (all dynamic), S06 |
| `dynamic-format` | `fmt.Errorf` with a format that isn't constant and isn't a forwarded parameter. | S05 |
| `memory` | An error loaded from a struct field, map, slice or array element, a pointer that isn't a local result variable, or a channel receive. Also an `Unwrap` whose inner value can't be matched to a construction site. | S03 (basic), S10 |
| `recover` | An error derived from `recover()` (for example `r.(error)`). | S10 |
| `reflection` | A call through `reflect.Value.Call`, `CallSlice` or `Method`. | S10 |
| `depends-on-caller` | A `Param` or `CallerSupplied` value reaches the top of a Query. | S04 (params), S12 (dynamic) |
| `no-instances` | A generic declaration Target that has no instantiation in the loaded program. | S06 |
| `fixpoint-limit` | An SCC didn't converge within the iteration cap, or a set passed its size cap. | S04 |
| `depth-limit` | A call beyond `-depth` hops from the Target. | S13 |
| `boundary-limit` | A call into a package outside `-boundary`. | S13 |

**Error Set entries.** A Query yields a set of top-level values. The renderer
works on **entries**, which come from **flattening** each value tree. There is one
entry per leaf (an Origin or an Unresolved). The entry's **Wrap Chain** is the
list of Wrap nodes from that leaf outwards to the top-level value. Each entry has:

- `Leaf` (Origin or Unresolved), `Chain []Wrap` (innermost first), `Truncated bool`;
- `Path` (the Propagation Path, see below), `MorePaths int` (how many other
  distinct paths were found, shown as `[+N more paths]`);
- `PossiblyHandled []GuardNote` (S07);
- `Group` (S06: the dynamic call site the entry is grouped under, if any).

Two entries are the same when their leaf and chain are equal. Paths don't count:
duplicates are merged and `MorePaths` is increased. Entries are ordered by
`(kind rank: sentinel, concrete, opaque, unresolved; leaf span string; chain string)`.

**Propagation Path.** This is a list of hops **from the Origin up to the Target**
(the glossary's direction). Hop 0 is the instruction where the leaf value enters
the innermost function: the load of the sentinel, the composite literal, the
`errors.New` call, or the unresolved site. Each later hop is a call instruction in
a caller, up to the Target function (or to the Target call site in a call-site
Query). A hop records: the span, the function containing it, the callee (for call
hops), whether it is dynamic, and the Candidate count before and after narrowing.

**Provenance.** Every fact in a summary keeps up to 4 **sources**, ranked so that
local sources come first, then static calls, then dynamic calls:

- `Local{Instr}`: created in this function;
- `Via{Site, Callee, CalleeResult, CalleeValue}`: came from a callee's summary fact;
- `WrapOf{Instr, InnerValue, InnerSources}`: created here as a Wrap of an inner value.

Paths are rebuilt by a shortest-path search over `(function, value)` nodes. The
cost is `(number of dynamic hops, number of hops)`, compared lexicographically,
and each node is visited once (modelled on govulncheck's `witness.go:36-73`). For a
leaf inside a Wrap the search follows the branch of the tree that holds that leaf,
so the path goes from the leaf's Origin, through the Wrap site, up to the Target.

### Summaries (`internal/summary`)

- **Local facts** come from `flow.Analyze(fn)`. For each error result index there
  is a set of local values. These may refer to `CallRef{site, resultIndex}`,
  `Param(i)` and `FreeVar(i)`, together with guards and wraps applied to them.
- **The demand graph** starts at the Target functions. Analysing a function finds
  the call sites whose results reach an error result (its `CallRef`s) and the
  deferred calls that can change a result (S08). For each such site,
  `Resolver.Callees(site)` gives the callees, which are added to the demand graph.
  Functions the error results never depend on are never summarised. This keeps the
  stdlib cost bounded.
- **Solving:** Tarjan SCCs over the demand graph, in reverse topological order. A
  single function without a self-edge is evaluated once. An SCC iterates until no
  summary changes, with a cap of 64 rounds (an internal constant). Facts still
  changing after the cap become `unresolved(fixpoint-limit)`. A per-result set
  size cap (1024 values) does the same.
- **Substitution** at a call site in caller `F` with callee `G`: each fact of `G`'s
  result `r` is mapped into `F`. `Param(i)` → the abstract values of argument `i`
  in `F`, with any pending guards applied. `CallerSupplied(i)` → narrowing or
  propagation (S12). Origins, Wraps and Unresolved are kept as they are. The
  source is recorded as `Via`.
- **Narrowing (S09):** facts that `G` got through a dynamic call whose receiver or
  function value is `Param(i)` carry a tag `(i, Candidate)`. When `F`'s argument
  `i` has a **complete** local type set (every definition ends in `MakeInterface`
  of a known type, a `*ssa.Function` or a `MakeClosure`), tagged facts whose
  Candidate is not in that set are dropped.

### Candidate resolution (`internal/resolve`)

- `Resolver` is an internal interface with three methods: Candidates for a call
  instruction (static callee or dynamic Candidates, sorted by function string),
  `IsDynamic(site)`, and `Reachable(fn)`. The static resolver (S04) returns
  `StaticCallee()` or nothing. The VTA resolver (S06) is described in P05.
- Errtrace implements its own `forwardSlice`. govulncheck's is internal: it follows
  call edges plus every `*ssa.Function` that appears as an operand (about 30 lines;
  see `x/vuln@v1.9.0/internal/vulncheck/slicing.go`).
- Roots are described in P09 (S06 for main/init, S12 for tests and synthetic roots).
- A map from each call site to its sorted Candidates is built once from the
  graph's edges (`Node.Out`, `Edge.Site`).

### Positions and spans

- Input positions follow gopls: `file.go:L:C`, `file.go:#OFF`, and range forms.
  Lines and columns are 1-based, columns count **bytes**, offsets are 0-based, and
  only file paths are accepted (not URIs).
- Output spans are `/abs/path.go:L:C-C` (same line) or `/abs/path.go:L:C-L2:C2`.
- Turning SSA values into spans: call module packages' `SetDebugMode(true)` before
  `prog.Build()`, so `DebugRef` instructions map values back to `ast.Expr`s. For
  call instructions, use the `ast.CallExpr` whose `Lparen` equals
  `CallCommon.Pos()`. For stdlib or dependency code without debug info, use
  `astutil.PathEnclosingInterval` around the instruction's `Pos()` to find the
  innermost expression and use its `Pos()`/`End()`. If even that fails, print a
  point span `L:C-C`. Pitfall: an implicit `MakeInterface` has `NoPos`. Use its
  operand's position (an `Alloc` for a composite literal has `Lbrace`) or the
  enclosing `Return`'s position.

---

## CLI surface (v1)

```
errtrace [flags] <target>

<target>  file.go:LINE:COL | file.go:#OFFSET | file.go:L:C-L2:C2 (start is used)
          | import/path.Func | import/path.Type.Method | (*import/path.Type).Method   (S13)

flags:
  -json            print the Error Set as JSON (schema errtrace/v1)            (S11)
  -tags list       build tags, passed as -tags to go/packages                  (S02)
  -p patterns      comma-separated package patterns to load (default ./...)    (S02)
  -tests           also load tests and use test mains as roots                 (S12)
  -depth N         max call hops followed from the Target (0 = unlimited)      (S13)
  -boundary B      all | module | package: which callees to follow (default all) (S13)
  -chain-depth N   max Wrap Chain depth before truncation (default 8)          (S05)
  -fanout N        group entries under dynamic sites with >= N Candidates
                   (default 8, 0 = never group); text output only             (S06)
  -cache           use the on-disk summary cache                               (S14)
  -cache-dir dir   cache directory (default $UserCacheDir/<BinaryName>)        (S14)
  -debug list      hidden: comma list of target,timing,summary (stderr only)   (S02)
```

- Exactly one positional argument. Relative paths are resolved against the working
  directory, which `cli.Options.Dir` sets (tests never `os.Chdir`).
- Loading: `packages.Config{Dir, Env, BuildFlags: -tags, Tests: -tests, Mode:
  LoadAllSyntax | NeedModule}`. Patterns are `-p` (default `./...`), plus
  `file=<abs path>` for a position Target, so a file outside the patterns still
  loads. `Env` is `os.Environ()`, so `GOFLAGS`, `GOWORK`, `GOOS`, `GOARCH` and
  `GOPACKAGESDRIVER` behave as with `go build`.
- **Exit codes and errors** (as in gopls): `0` on success, even when there are
  Unresolved entries or 0 entries. `2` for usage errors, Targets that can't be
  resolved, package load or type errors, and internal failures. Messages go to
  stderr as `<BinaryName>: <msg>`, and include the span when there is one
  (`errtrace: /abs/a.go:3:1: no function declaration or call at position`). Usage
  errors also print the usage text. `-h` prints usage to stdout and exits 0.
  stdout carries only the result.

## Output contract

### Text

```
<target-span>: Error Set of <target>: <N> entries[, <U> unresolved][, <H> possibly handled]
<entry-span>: <label>[ [typed nil]][ [chain truncated]][ [+K more paths]]
	wrap chain (origin → target):
		<wrap-span>: <wrap description>
	possibly handled:
		<guard-span>: <reason>
	path (origin → target):
		<hop-span>: in <function>
		<hop-span>: in <caller>, call to <callee>[ (dynamic <method>: <k> of <n> candidates)]
<next entry ...>
```

- `<target>` is, for a declaration, the SSA function string with full import
  paths (`example.com/fx.Load`, `(*example.com/fx.T).Get`, `example.com/fx.Map`
  for a generic, whose instances appear in the path hops). For a call site it is
  `call to <callee expression> in <enclosing function>`, for example
  `call to r.Read in example.com/fx.load`. Function names elsewhere in the output
  use the same SSA string form (`example.com/fx.Map[int]` for an instance).
- `<label>` is one of: `sentinel <pkgpath>.<Name>`, `concrete <type>`,
  `opaque errors.New "<msg>"`, `opaque fmt.Errorf "<fmt>"`, `opaque` (constructor
  unknown), or `unresolved(<kind>): <detail>`.
- `<entry-span>`: for a Sentinel, its `var` identifier. For a Concrete Type, the
  expression that builds it. For an Opaque Error, the constructor call. For an
  Unresolved entry, the site.
- `<wrap description>`: `fmt.Errorf "<fmt>"`, `errors.Join`, `<type>.Unwrap`,
  `errtrace.Wrap`. The innermost Wrap comes first.
- Sections that would be empty are left out. Indentation is one tab per level.
  There is no blank line between entries. A query with 0 entries prints only the header.
- **Grouping (S06):** when ≥ `-fanout` entries have, as the dynamic hop nearest
  the Target in their path, the same call site with ≥ `-fanout` Candidates, they
  are printed together after the ungrouped entries:

  ```
  <site-span>: group of <M> entries via dynamic call to <method> (<n> candidates) in <function>
  	<entry-span>: <label> (candidate <fn>)
  	...
  ```

  Grouped entries print only their label and Candidate, not the subtree. The
  header counts still include every entry.

### JSON (S11)

A single object, two-space indented, ending with a newline:

- `schema` is `"errtrace/v1"`.
- `target`: `{kind: "declaration"|"call-site", name, span, functions: [names of instances]}`.
- `entries[]`:
  - `kind`: `sentinel`|`concrete`|`opaque`|`unresolved`;
  - `origin`: `{span, name?, type?, constructor?, message?, typedNil?}`;
  - `unresolved`: `{kind, detail}` (only when `kind` is `unresolved`);
  - `wrapChain[]`: `{kind, span, format?, type?}`, innermost first, plus `truncated: bool`;
  - `path[]`: `{span, function, callee?, dynamic?, method?, candidates?, candidatesBeforeNarrowing?}`, origin first;
  - `morePaths`;
  - `possiblyHandled[]`: `{span, reason}`.
- `stats`: `{entries, unresolved, possiblyHandled, functionsSummarised}`.

A span is gopls's `definition -json` shape: `{"uri":"file:///abs/a.go","start":{"line":L,"column":C,"offset":O},"end":{...}}`,
with byte columns. Fields that don't apply are left out, never `null`. Field order
is fixed by struct order. The schema is documented in `internal/render/schema.md`
(created in S11; it is the one doc file the plan calls for).

---

## Conventions for every step

**Definition of done (the reviewer checks all of these):**

1. `go build ./... && go vet ./... && go test ./...` passes from the repo root,
   using Go 1.24.3 with `GOTOOLCHAIN=local`.
2. `gofmt -l .` prints nothing. `go mod tidy` leaves `go.mod` and `go.sum` unchanged.
3. The step adds tests for what it adds, preferably **fixture tests** (below).
   All tests pass with `-count=2` (deterministic) and with `-race` on the
   packages the step touched.
4. Every golden change was made with `-update` and is intended. The reviewer
   reads the golden diff.
5. Nothing outside the step's "Files/packages touched" changes, apart from
   goldens that the new behaviour legitimately changes. The step's out-of-scope
   items are not built.
6. Tests use no network: the harness sets `GOPROXY=off`, `GOFLAGS=-mod=mod`,
   `GOWORK=off` and `GOTOOLCHAIN=local`. Fixtures import only the stdlib, plus
   local modules added with `replace` inside the fixture.
7. The full `go test ./...` takes under ~3 minutes on the dev machine. Fixtures
   are small, and fixture subtests use `t.Parallel()`.
8. Only `errtrace-v1` is used. No new branches, no merges from `research/*`.
9. No Go code outside the step's scope. No README or other docs unless the step says so.

**Fixture format** (`internal/fixture`, built in S01). End-to-end fixtures live in
`internal/cli/testdata/e2e/<area>/<name>.txtar` and are driven by
`internal/cli/e2e_test.go`:

```
-- args --
-fanout=0 a.go:@load
-- stdout --
<expected stdout, golden>
-- stderr --            (optional; empty if absent)
-- exit --              (optional; default 0)
-- go.mod --
module example.com/fx

go 1.24
-- a.go --
package fx
func /*@load*/Load() error { ... }
```

- `@name` in `args` is replaced by `L:C` of the byte right after the comment
  `/*@name*/` in the file named before the colon. `#@name` gives the byte offset instead.
- Output is normalised before it is compared: the temp dir becomes `$WORK`.
  Spans under `$GOROOT` become `$GOROOT/src/<path>:*`, because stdlib line
  numbers change between Go versions. The same applies inside JSON `uri` and
  `start`/`end` fields, which become `"*"`.
- `go test ./internal/cli -run E2E -update` rewrites `stdout`, `stderr` and
  `exit` in place and keeps everything else byte for byte.
- `internal/fixture` also exports `LoadProgram(t, archive)` for unit tests that
  need a `load.Program` without the CLI.

**Coding conventions.** Iterate maps only in sorted order where it affects output.
Use `ctx` for cancellation in long phases. No global mutable state (it is the
goexhauerrors data-race pitfall). Errors are wrapped with `%w` and context. Every
exported identifier in `internal/` has a doc comment that uses glossary terms.

---

## Steps

| Id | Title | Depends on |
|---|---|---|
| S01 | Module scaffold, CLI shell, span grammar and fixture harness | none |
| S02 | Package loading, SSA build and position Targets | S01 |
| S03 | Thin end-to-end slice: intraprocedural Origins and text output | S02 |
| S04 | Interprocedural summaries over SCCs and Propagation Paths | S03 |
| S05 | Wraps and Wrap Chains | S04 |
| S06 | Dynamic calls: VTA Candidates for interfaces, function values and generics | S05 |
| S07 | Guard filtering per return path | S05, S06 |
| S08 | Deferred changes to named results | S06 |
| S09 | Call-site Queries and local narrowing | S06, S07 |
| S10 | Unresolved completeness and sentinel identity | S08, S09 |
| S11 | JSON output with a stable schema | S10 |
| S12 | Query roots for libraries and tests | S11 |
| S13 | Depth and package-boundary limits, symbol-name Targets | S12 |
| S14 | Optional on-disk summary cache | S13 |

The steps are strictly ordered (do them in id order). "Depends on" names the steps
whose code the step builds on directly.

---

### S01: Module scaffold, CLI shell, span grammar and fixture harness

**Goal.** Get a buildable module with the pinned x/tools, a CLI that parses flags
and its Target argument and follows the exit-code conventions, a complete gopls
position grammar, and the fixture harness every later step uses.

**Depends on.** None.

**Files/packages touched.** `go.mod`, `go.sum`, `cmd/errtrace/main.go`,
`internal/cli/{name.go,cli.go,cli_test.go,e2e_test.go}`,
`internal/cli/testdata/e2e/cli/*.txtar`, `internal/span/{span.go,parse.go,format.go,*_test.go}`,
`internal/fixture/{fixture.go,fixture_test.go}`.

**Implementation notes.**
- `go get golang.org/x/tools@v0.39.1-0.20260109155911-b69ac100ecb7`. Keep
  `go 1.24.3` and add no toolchain line. `txtar` (used by the harness) keeps the
  require in go.mod after `go mod tidy`.
- `internal/cli/name.go` holds only `BinaryName` and its doc comment (P10). The
  usage text, the stderr prefix and the cache dir default use it.
- `cli.Main(args, stdout, stderr) int` wraps `cli.Run(ctx, Options) int`.
  `Options` carries `Args`, `Dir`, `Env`, `Stdout` and `Stderr`. Use a private
  `flag.FlagSet` with `ContinueOnError`, and send its output to the configured
  writers. Register the flags that exist so far (`-tags`, `-p`, `-debug`); later
  steps add theirs. For now, once the Target parses, `Run` prints
  `<BinaryName>: not implemented` and exits 2. S02 replaces this.
- `internal/span`:
  - The `Point` type has `Line`, `Col` (1-based, bytes) and `Offset` (0-based,
    −1 if unset). The `Span` type has `File` (absolute, cleaned) plus `Start` and
    `End` points.
  - `Parse(arg, cwd)` follows `gopls/internal/cmd/parsespan.go` and parses from
    the right: `file`, `file:L`, `file:L:C`, `file:#O`, `file:L:C#O`, and the
    range forms `-L:C`, `-C` (end column when the start had a column) and `-#O`.
    **Fix gopls's known separator bug** (`rstripSuffix`, marked "clearly wrong" at
    `parsespan.go:100-103`): reject `a.go::3`, `a.go:x:3`, `a.go:0:1`,
    `a.go:1:0`, `a.go:-1`, and empty components, each with a clear error.
    Windows drive letters (`C:\x.go:3:4`) work because parsing starts from the right.
  - `Resolve(tokFile)` fills in the missing parts with `token.File` (line start +
    col − 1 ↔ offset) and checks bounds (a column may point one past the end of
    the line, as in gopls; an offset may be at most the file size).
  - `Format(span)` prints `path:L:C-C` or `path:L:C-L2:C2` (`gopls span.go:290-343`).
  - `Of(fset, start, end token.Pos)` builds a Span.
  - The JSON span shape is `{uri,start{line,column,offset},end{...}}`. Implement
    marshalling now (S11 uses it).
- `internal/fixture`: `Run(t, path, runFunc)` reads a txtar file, writes the
  module files into `t.TempDir()`, replaces `@markers`, calls `runFunc` with
  Dir/Env/args, normalises output (Conventions), and compares it with or updates
  the sections. Use a package-level `-update` flag registered in fixture (on the
  `flag.CommandLine` of the test binary). `LoadProgram` is added in S02.

**Acceptance criteria.**
- `go build ./... && go vet ./... && go test ./...` passes. `go list -m golang.org/x/tools`
  prints the pinned version. `grep -n '^go 1.24.3$' go.mod` matches.
  `grep -rn '"errtrace"' --include=*.go . | grep -v internal/cli/name.go` finds no
  hard-coded binary name (test fixtures compare outputs, which may contain it).
- `internal/span` table tests cover: every accepted form (including a non-ASCII
  identifier line, which shows that columns count bytes: `é` is 2 columns); every
  rejected form listed above; round-trips `Parse(Format(s)) == s` for a single-line
  and a multi-line span; out-of-range line, column and offset errors.
- E2E fixtures in `internal/cli/testdata/e2e/cli/`: `no_args.txtar` (exit 2, usage
  on stderr), `two_args.txtar` (exit 2), `bad_position.txtar` (`a.go:x:3`, exit 2,
  stderr `errtrace: ...`), `help.txtar` (`-h`, usage on stdout, exit 0),
  `unknown_flag.txtar` (exit 2).
- The harness's own test proves that `-update` rewrites only the result sections
  (it updates a copy in a temp dir and diffs it), that `@marker` substitution
  produces the right `L:C` and `#OFF`, and that `$WORK`/`$GOROOT` masking works.

**Out of scope for this step.** Loading packages, SSA, any analysis, JSON Error Set
output, symbol Targets.

---

### S02: Package loading, SSA build and position Targets

**Goal.** Load the program the way the CLI will in v1, build SSA, and resolve a
position Target to a declaration or a call site, including generic instances.

**Depends on.** S01.

**Files/packages touched.** `internal/load/*`, `internal/target/*`, `internal/cli/cli.go`,
`internal/fixture/fixture.go` (adds `LoadProgram`), `internal/cli/testdata/e2e/target/*.txtar`,
`internal/target/testdata/*.txtar`.

**Implementation notes.**
- `load.Load(ctx, Config{Dir, Env, Tags, Patterns, Tests, TargetFile})`:
  - `packages.Config` uses `Mode: packages.LoadAllSyntax | packages.NeedModule`,
    with `Dir`, `Env` and `BuildFlags` set from the config.
  - Patterns are the `-p` list (default `./...`), plus `file=<abs>` when there is a
    position Target.
  - If `packages.PrintErrors` would print anything (count the errors with
    `packages.Visit`), return an error that lists the first 10 errors. The CLI
    then exits 2.
  - Build SSA with `ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)`. Before
    `prog.Build()`, call `SetDebugMode(true)` on every SSA package whose
    `packages.Package.Module` is a main module (in the workspace). Don't use
    `GlobalDebug`: it inflates the stdlib.
  - `Program` exposes the fset, the `*ssa.Program`, the packages by ID, the
    initial packages, `FileFor(absPath)` (prefer the non-test variant when a file
    is in several packages, then the lowest package ID) and
    `Span(pos)`/`SpanOfValue(v)`/`SpanOfInstr(i)`, following
    [Positions and spans](#positions-and-spans).
- `target.Resolve(prog, span.Span)` returns a `Target` with:
  - `Kind` (declaration or call site);
  - `Name`;
  - `Span` (the identifier for a declaration, the call expression for a call site);
  - `Funcs []*ssa.Function` (one, or every instance of a generic);
  - `Site` (call site only) and `SiteByFunc map[*ssa.Function]ssa.CallInstruction`
    (the matching call in each instance).

  The rules:
  - Use `astutil.PathEnclosingInterval(file, pos, pos)`.
  - If the position is on a `FuncDecl`'s name or its `func` keyword, it is a
    declaration. Look up `info.Defs[name].(*types.Func)`, then
    `prog.FuncValue(obj)`, which works for concrete methods.
  - If it is on the `func` keyword of a `FuncLit`, it is a declaration of that
    closure (`ssa.EnclosingFunction`).
  - If it is on the callee name of a `CallExpr` (the `Ident`, or the `Sel` of a
    selector) or on its `(`, it is a call site. Find the enclosing function with
    `ssa.EnclosingFunction(ssaPkg, path)`. Then find the `ssa.CallInstruction`
    whose `Common().Pos() == call.Lparen` (this works for `go`/`defer` too).
  - Anything else is an error: `no function declaration or call at position`.
  - Reject interface method declarations (`interface method has no body; query a
    call site`), conversions and builtins (`not a call to a function`), and calls
    or functions with no error result (`<name> has no error result`). The
    error-result predicate is "the result type is `error` or implements `error`"
    (`types.Implements` against `types.Universe.Lookup("error")`, checking both
    `T` and `*T` where that applies). Put the predicate in `internal/flow`
    (`IsErrorType`) so S03 reuses it.
- **Generics pitfall:** `EnclosingFunction` and `FuncValue` return the **generic
  origin**. Its body gets no call-graph edges (`callgraph-algorithms`, §4
  Generics), so it must not be analysed. Collect the instances with
  `fn.Origin() == generic`. In S02, scanning `ssautil.AllFunctions(prog)` once is
  acceptable; S06 replaces it with the reachable slice. If there are no
  instances, `Funcs` is empty; S06 turns that into `unresolved(no-instances)`.
- CLI: after `Resolve` succeeds, `-debug=target` prints
  `target: <kind> <name> funcs=[...] span=<span>` to stderr. stdout stays empty,
  and the exit code is 0. `-debug=timing` prints `timing: load=… ssa=…` to stderr
  (the harness doesn't compare timing lines: the test strips lines starting with
  `timing:`).

**Acceptance criteria.**
- `internal/target` unit tests (with `fixture.LoadProgram`) resolve:
  - a func decl name and its `func` keyword;
  - a method `(*T).M` and a value-receiver method;
  - a closure's `func` keyword;
  - a call site on the callee ident, on the selector `Sel` and on `(`;
  - a call on a `defer` and a `go` statement line;
  - a generic `Map[T]` with two instantiations (`Funcs` has two instances, sorted
    by name);
  - a call site inside a generic function (one site per instance).
- Errors (each checked): a position on whitespace; on a variable; on an interface
  method; on `len(x)`; on a call to `func() int`; on a file not in any loaded
  package.
- E2E fixtures `internal/cli/testdata/e2e/target/{decl,call_site,generic,errors}.txtar`
  use `-debug=target` and compare stderr.
- `load` has a test where a fixture with a type error exits 2 with
  `errtrace: ` and the error position on stderr.
- `go test ./...` still passes. S01 fixtures that used `not implemented` are
  updated to the new behaviour.

**Out of scope for this step.** Any error analysis, call graphs, symbol-name
Targets, `-tests` roots (only the load flag is passed through), JSON.

---

### S03: Thin end-to-end slice: intraprocedural Origins and text output

**Goal.** Get the first real answers: `errtrace a.go:@f` prints the Error Set of a
function, using only its own body, in the final text format.

**Depends on.** S02.

**Files/packages touched.** `internal/errset/*`, `internal/flow/*`, `internal/query/*` (minimal),
`internal/render/{text.go,text_test.go}`, `internal/cli/cli.go`,
`internal/cli/testdata/e2e/origins/*.txtar`.

**Implementation notes.**
- `internal/errset`: the value table (interning), the kinds from the
  [Data model](#data-model-internalerrset), entries, flattening, and ordering.
  The Unresolved kinds needed now are `pending` (temporary, see below) and `memory`.
- `flow.Analyze(fn) → LocalFacts`:
  - Find the **error results**: each result index whose type passes `IsErrorType`.
  - For each `*ssa.Return`, walk `Results[i]` backwards (`error-flows-in-ssa`, §1
    table):
    - `*ssa.Const` nil → dropped (no error).
    - `UnOp{MUL}` of a `*ssa.Global` whose type is an error type → **Sentinel**
      (`Global.Object().(*types.Var)`).
    - `MakeInterface{X}`: if X is a load from a Global → Sentinel (pointer
      sentinel, with its type recorded). If X is `Const` nil → Concrete Type with
      `TypedNil`. Otherwise → **Concrete Type** `X.Type()`, positioned at X's
      construction or at the conversion.
    - A static call to `errors.New` → **Opaque** with the constant message if
      `Args[0]` is a `*ssa.Const`.
    - `Phi` → union of its edges (with a visited set, for cycles).
    - `ChangeInterface`, `ChangeType` → transparent.
    - `Extract{Tuple: call, Index}` and a direct `*ssa.Call` →
      `CallRef{site, index}`.
    - `*ssa.Parameter` → `Param(i)`. `*ssa.FreeVar` → `FreeVar(i)`.
    - Loads through `FieldAddr`, `IndexAddr`, `Lookup`, `UnOp{ARROW}` →
      `unresolved(memory)`.
  - For a concrete-typed error result (for example `func f() *MyErr`), a value
    built from an `Alloc` complit is a Concrete Type origin. There is no
    `MakeInterface` in that case.
  - Don't special-case function names other than `errors.New` here. `fmt.Errorf`
    is handled in S05.
- `internal/query` (minimal): for a declaration Target, take the local facts of
  each function in `Target.Funcs`. Map `CallRef` → `unresolved(pending)` (detail
  `call to <callee> not followed yet`). Map `Param` and `FreeVar` →
  `unresolved(depends-on-caller)` with the parameter name. Flatten, dedup and sort.
  Path = a single hop (where the value enters the function). A call-site Target in
  S03 prints the header and one `unresolved(pending)` entry. S09 gives it real
  semantics.
- `internal/render/text.go` implements the [Text contract](#text) except
  grouping and `possibly handled`, which come later.
- `pending` exists **only** so that S03 can ship an end-to-end slice. S04 must
  delete the kind and all its goldens.

**Acceptance criteria.**
- E2E fixtures in `internal/cli/testdata/e2e/origins/`, each with a golden stdout:
  - `sentinel_direct.txtar`: `var ErrNotFound = errors.New("not found")`;
    `func Find() error { return ErrNotFound }`; query `a.go:@find` → exactly one
    entry `sentinel example.com/fx.ErrNotFound` with the entry span on the `var`
    identifier and a one-hop path at the `return` operand.
  - `stdlib_sentinel.txtar`: `return io.EOF` → `sentinel io.EOF` with a
    `$GOROOT/src/io/io.go:*` span.
  - `concrete_ptr_and_value.txtar`: `&MyErr{}` and `ValErr{}` → two distinct
    Concrete Type entries (`*example.com/fx.MyErr`, `example.com/fx.ValErr`).
  - `typed_nil.txtar`: `var p *MyErr; return p` → `concrete *example.com/fx.MyErr [typed nil]`.
  - `opaque_new.txtar`: `errors.New("boom")` → `opaque errors.New "boom"`.
  - `phi_union.txtar`: `if b { err = io.EOF } else { err = &MyErr{} }` → both entries.
  - `nil_only.txtar`: `return nil` → header `0 entries` only.
  - `concrete_result_type.txtar`: `func f() *MyErr { return &MyErr{} }` → one Concrete Type entry.
  - `pending_call.txtar`: `return g()` → one `unresolved(pending)` entry. It is
    deleted in S04.
  - `param_passthrough.txtar`: `func f(err error) error { return err }` →
    `unresolved(depends-on-caller): parameter err (error)`.
  - `field_load.txtar`: `return s.err` → `unresolved(memory)`.
- Running each fixture twice gives the same output (`-count=2`).
- `internal/errset` unit tests: interning gives the same ID for equal values;
  ordering is stable; flattening a hand-built tree gives the expected entries.

**Out of scope for this step.** Following calls, Wraps, `fmt.Errorf`, guards,
defers, dynamic calls, grouping, JSON.

---

### S04: Interprocedural summaries over SCCs and Propagation Paths

**Goal.** Follow static calls across functions, packages and the stdlib with
bottom-up summaries over SCCs, and print real Propagation Paths.

**Depends on.** S03.

**Files/packages touched.** `internal/resolve/{resolver.go,static.go,*_test.go}`,
`internal/summary/*`, `internal/query/*`, `internal/errset/*` (provenance, path),
`internal/render/text.go`, `internal/cli/cli.go`,
`internal/cli/testdata/e2e/{interproc,origins}/*.txtar`.

**Implementation notes.**
- `resolve.Resolver` interface ([Candidate resolution](#candidate-resolution-internalresolve)).
  `resolve.Static` returns `[StaticCallee()]` (this covers immediately applied
  closures as well) and nothing for dynamic sites.
- `summary.Solver`:
  - Build the demand graph from `Target.Funcs` by following `CallRef` sites
    through the Resolver.
  - Run Tarjan's SCC algorithm (iterative, not recursive, so the stdlib can't
    overflow the stack).
  - Evaluate in reverse topological order, with a fixpoint per SCC capped at 64
    rounds and 1024 values per result → `unresolved(fixpoint-limit)`.
  - Substitution and provenance follow [Summaries](#summaries-internalsummary).
  - Static callees with `Blocks == nil` → `unresolved(no-body)` with the callee
    name (for example runtime or assembly functions).
  - Dynamic sites → `unresolved(no-candidates)` with detail
    `dynamic call to <method or value>`. S06 resolves them.
  - Pitfall (prior-art §3): never recurse with a `seen` set. Cycles must go
    through the SCC fixpoint.
- `errors.New` stays an intrinsic: don't analyse its body. Add an intrinsic
  table keyed on the callee's package path and name (`errors.New` now;
  `fmt.Errorf`, `errors.Join`, `errors.Is` and `errors.As` come later). A callee
  in the intrinsic table never enters the demand graph.
- `query`: the top-level values are the union of the Target functions' result
  facts. Remaining `Param` → `depends-on-caller` (as in S03). Paths come from the
  provenance search ([Propagation Path](#data-model-internalerrset)). `MorePaths`
  counts the other distinct sources reached.
- `-debug=summary` prints the number of summarised functions and SCCs to stderr.
- Remove the `pending` kind and the `pending_call.txtar` fixture.

**Acceptance criteria.**
- New fixtures in `internal/cli/testdata/e2e/interproc/`:
  - `chain3.txtar`: `A → B → C` returns `ErrX`. Querying `A` gives one entry whose
    path has 3 hops in origin → target order (C's return, B's call to C, A's call
    to B) with the correct spans.
  - `two_paths.txtar`: `A` calls `B` and `C`, and both return `ErrX` → one entry
    `[+1 more paths]`; the path shown is the shorter or first-ranked one.
  - `param_subst.txtar`: `func id(e error) error { return e }`;
    `func A() error { return id(ErrX) }` → `sentinel ErrX` with a path through
    `id`. There is **no** depends-on-caller entry.
  - `recursion.txtar`: mutually recursive `even`/`odd`, each returning a different
    sentinel on its base case → both sentinels; terminates.
  - `self_recursion_param.txtar`: `f(n int, e error) error { if n==0 {return e}; return f(n-1, e) }`
    queried at `g() { return f(3, ErrX) }` → ErrX.
  - `cross_package.txtar`: two packages in one module; the Origin is in the
    imported package, and spans point there.
  - `stdlib_follow.txtar`: `strconv.Atoi("x")` result returned → contains
    `concrete *strconv.NumError` (Wrap Chain content comes in S05).
  - `no_body.txtar`: a function declared without a body (with a `.s` file stub in
    the fixture) → `unresolved(no-body)`.
  - `dynamic_unresolved.txtar`: `var f func() error; return f()` →
    `unresolved(no-candidates)`.
- Unit tests in `internal/summary`: SCC order on a hand-built graph; fixpoint cap
  (a synthetic non-converging transfer function hits `fixpoint-limit`); substitution
  maps `Param(0)` to the argument's values.
- `grep -rn pending internal/` finds no Unresolved kind `pending`.
- All earlier goldens pass (with `-update` diffs limited to path changes).

**Out of scope for this step.** Wraps and `fmt.Errorf`, VTA and dynamic Candidates,
guards, defers, call-site semantics, limits.

---

### S05: Wraps and Wrap Chains

**Goal.** Model `fmt.Errorf` (`%w`, several `%w`, `%[n]w`, no `%w`),
`errors.Join`, custom `Unwrap`, and bracesdev `errtrace.Wrap` as Wraps, and print
the Wrap Chain.

**Depends on.** S04.

**Files/packages touched.** `internal/fmtverb/*`, `internal/flow/*` (intrinsics),
`internal/summary/*` (forwarded-format helpers), `internal/errset/*`,
`internal/render/text.go`, `internal/cli/cli.go` (`-chain-depth`),
`internal/cli/testdata/e2e/wraps/*.txtar`.

**Implementation notes.**
- `internal/fmtverb.Parse(format)` returns the verbs, each with its argument
  index, including `%[n]w`, `%%`, flags, `*` width and precision (which use up an
  argument), and the reset that follows an explicit index. It must be a new
  implementation: x/tools' parser is internal (`internal/fmtstr/parse.go:64`).
  Table tests should be checked against `fmt.Errorf`'s runtime behaviour
  (`fmt/errors.go:22-60`).
- `fmt.Errorf` intrinsic (`error-flows-in-ssa`, §2 recipe):
  - If `Args[0]` is a `*ssa.Const` string, parse it.
  - Follow `Args[1]` (a `Slice` of an `Alloc` with comment `varargs`) to the
    `Store` at each `IndexAddr` constant index, and strip
    `ChangeInterface`/`MakeInterface` to get the operand's values.
  - 0 `%w` → **Opaque** `fmt.Errorf "<fmt>"`.
  - 1 `%w` → Wrap(errorf, *fmt.wrapError) with 1 inner.
  - ≥2 `%w` → Wrap with inner values sorted and de-duplicated by argument index
    (`*fmt.wrapErrors`).
  - An operand that isn't an error is dropped (`w.err, _ = a[i].(error)`). `%v`
    of an error does **not** wrap.
- **Forwarded formats** (`wrapf(format string, args ...any)` that calls
  `fmt.Errorf(format, args...)`): when the format is a `Param`, record the
  summary as an "Errorf-like" fact parameterised by (format param, args param).
  At substitution, a constant format argument at the caller is evaluated as above
  against the caller's varargs. A non-constant format at the top →
  `unresolved(dynamic-format)`. A format built at runtime →
  `unresolved(dynamic-format)` as the leaf, with the inner errors kept as
  "possible wraps" under it.
- `errors.Join` intrinsic: follow the varargs `[]error` array. Nil operands drop.
  The result is a Wrap(join) of the non-nil values. A spread slice (`errs...`)
  whose contents can't be followed locally → inner `unresolved(memory)`.
- **Custom `Unwrap`** (the map's open item "Wrap Chains through custom Unwrap";
  v1 handles the easy case):
  - At a Concrete Type origin built from an `Alloc` complit, look up `Unwrap`
    with signature `func() error` or `func() []error` in
    `prog.MethodSets.MethodSet(T)`.
  - If the method body returns `*(&recv.f)` (a field load), the inner value is
    whatever the construction site stored to field index `f` (match `FieldAddr`
    indices).
  - In that case the origin becomes Wrap(unwrap-method, T) whose inner value is
    that stored value. Any other `Unwrap` shape → inner `unresolved(memory)` with
    detail `<T>.Unwrap not modelled`.
  - This covers `*fs.PathError`, `*strconv.NumError` and `*os.SyscallError` built
    with complit fields.
- `braces.dev/errtrace.Wrap(x)` (matched on package path and name) → Wrap(braces-wrap) of x.
- **Chain depth cap:** loops such as `err = fmt.Errorf("…%w", err)` give an
  infinite chain (`error-flows-in-ssa`, §5, `F3Loop`). When a Wrap would make a
  tree deeper than `-chain-depth` (default 8), replace the overflow with
  `Truncated` and stop. Interning keeps the fixpoint finite.

**Acceptance criteria.**
- `internal/fmtverb` table tests: `"%w"`, `"%w and %w"`, `"%[2]w then %[1]v"`,
  `"%%w"` (no verb), `"%*d %w"` (the `%w` index is 2), `"%v %w"`, `"%[3]w %w"`
  (index reset), a malformed `"%[x]w"` (reported as a bad verb, no wrap).
- E2E fixtures in `internal/cli/testdata/e2e/wraps/`:
  - `errorf_single.txtar`: `fmt.Errorf("load: %w", ErrX)` → `sentinel ErrX` with
    a wrap chain line `fmt.Errorf "load: %w"`.
  - `errorf_multi.txtar`: two `%w` → two entries, each with the same wrap line.
  - `errorf_reorder.txtar`: `"%[2]w then %[1]v"` → only arg 2 is wrapped; arg 1
    (an error) is **not** in the set as its own entry, and the result also has no
    opaque entry (a `%w` format produces a wrapError, not an Opaque Error).
  - `errorf_nowrap.txtar`: `fmt.Errorf("bad %d", x)` → `opaque fmt.Errorf "bad %d"`.
  - `errorf_v_of_err.txtar`: `fmt.Errorf("x: %v", err)` → only the opaque entry.
  - `join.txtar`: `errors.Join(ErrA, nil, ErrB)` → two entries with the chain
    `errors.Join`.
  - `custom_unwrap.txtar`: `&wrapErr{inner: ErrX}` with a field-returning
    `Unwrap` → ErrX with the chain `*example.com/fx.wrapErr.Unwrap`.
  - `custom_unwrap_lazy.txtar`: an `Unwrap` that computes its result →
    `unresolved(memory)` under the wrapper.
  - `wrapf_forwarded.txtar`: `wrapf("y: %w", ErrX)` → ErrX wrapped, with no
    `dynamic-format`.
  - `dynamic_format.txtar`: `fmt.Errorf(os.Getenv("F"), ErrX)` →
    `unresolved(dynamic-format)`.
  - `loop_wrap.txtar`: wrapping inside a `for` loop with `-chain-depth=3` → the
    entry ends with `[chain truncated]`, and the run terminates.
  - `strconv_atoi.txtar` (stdlib oracle from go-errorlint's table, derived rather
    than hard-coded): `strconv.Atoi` → the entries include `strconv.ErrSyntax`
    and `strconv.ErrRange`, each with a chain through `*strconv.NumError.Unwrap`.
- All earlier goldens still pass, or are updated only where a call that is now
  modelled changes an entry.

**Out of scope for this step.** Guards (`errors.Is` is not filtering yet), custom
`Is`/`As`, dynamic calls, defers.

---

### S06: Dynamic calls: VTA Candidates for interfaces, function values and generics

**Goal.** Resolve interface method calls, function values (closures, method
values, callbacks) and calls inside generic instances to Candidates with
govulncheck-style VTA (P05), and collapse large fan-out when rendering.

**Depends on.** S05.

**Files/packages touched.** `internal/resolve/{vta.go,roots.go,slice.go,*_test.go}`,
`internal/summary/*`, `internal/flow/*` (FreeVar binding), `internal/target/*`
(instances from the slice), `internal/query/*`, `internal/render/text.go` (grouping),
`internal/cli/cli.go` (`-fanout`), `internal/cli/testdata/e2e/dynamic/*.txtar`.

**Implementation notes.**
- Roots (this step's part of P09):
  - `main` and `init` of every SSA main package among the initial packages
    (`ssautil.MainPackages`), plus every package's `init`.
  - If the Target functions aren't reachable from these roots, add them as extra
    roots and print `<BinaryName>: warning: target not reachable from main/init;
    parameters may lack candidates` to stderr. S12 replaces this with synthetic
    roots and `depends-on-caller`.
- `resolve.VTA`, built once per run:
  - `cha.CallGraph(prog)` → `forwardSlice(roots, cha)` → `vta.CallGraph(slice, cha)`
    → `forwardSlice(roots, vta1)` → `vta.CallGraph(slice2, vta1)` →
    `DeleteSyntheticNodes()`.
  - Build `site → sorted []*ssa.Function`.
  - `Reachable(fn)` = `fn ∈ slice2`.
  - Respect `ctx` between phases. Time each phase for `-debug=timing`.
- Pitfalls:
  - VTA needs `InstantiateGenerics`, which S02 already sets.
  - `DeleteSyntheticNodes` splices out wrappers (`$bound`, `$thunk`, pointer
    wrappers), so Candidates are real methods. For positions and names of
    synthetic functions, fall back to `fn.Object()` or `fn.Origin()`.
  - VTA doesn't model reflection or unsafe (that is S10's Unresolved).
  - `vta.CallGraph` drops call edges whose callers aren't in `funcs`, which is
    why roots matter.
  - VTA is context-insensitive: parameter Candidates mix callers. S09 narrows them.
- Summaries: at a dynamic site, the Candidates are the sorted callees. A site with
  0 Candidates → `unresolved(no-candidates)`, as before. Record the hop as dynamic,
  with the method (`(io.Reader).Read`) or the function-value expression, and the
  Candidate count.
- **Function values and closures:** a call whose value is a `MakeClosure`, a
  `*ssa.Function` or a method value is still static where `StaticCallee` handles
  it. Otherwise VTA supplies the Candidates.
  - Inside a closure, `FreeVar(i)` is resolved from the `Bindings[i]` of every
    `MakeClosure` of that function found in the reachable slice.
  - A binding that is a value (not a pointer) is substituted directly.
  - A binding that is an `Alloc` address → the union of the values stored to that
    `Alloc` in the creator function. S08 uses the same helper.
- **Generics:**
  - `Target.Funcs` for a generic declaration is now the set of instances whose
    `Origin()` is the generic function, taken from the reachable slice instead of
    `AllFunctions`.
  - No instances → `unresolved(no-instances)` with the generic's name.
  - The Error Set is the union over instances.
  - Path hops name the instance (`example.com/fx.Map[int]`), while spans come from
    the shared source positions.
  - Never analyse the generic origin's body.
- **Fan-out grouping** in `render/text.go`, as in the [Text contract](#text),
  controlled by `-fanout` (default 8).

**Acceptance criteria.**
- E2E fixtures in `internal/cli/testdata/e2e/dynamic/` (each has a `main` that
  creates the concrete values):
  - `iface_two_impls.txtar`: `type Getter interface{ Get() error }` with impls
    `A` (returns ErrA) and `B` (returns ErrB), and a third impl `C` (ErrC) that is
    never created. Querying `func use(g Getter) error { return g.Get() }` → ErrA
    and ErrB only. The path hop shows `(dynamic (example.com/fx.Getter).Get: 2 of 2 candidates)`.
  - `closure_callback.txtar`: `apply(cb func() error)` called with a closure that
    returns ErrX → ErrX through the closure.
  - `method_value.txtar`: `f := t.Load; return f()` → the Error Set of `(*T).Load`.
  - `freevar_capture.txtar`: a closure that returns a captured `err` set in the
    enclosing function → the enclosing function's assigned Origins.
  - `generic_instances.txtar`: `func Must[T any](v T, err error) (T, error)` used
    with two type arguments, and a generic `Map[T]` calling a callback → the union
    over the instances; path names include the type arguments.
  - `generic_uninstantiated.txtar`: a generic function that is never instantiated
    → `unresolved(no-instances)`.
  - `fanout.txtar`: an interface with 9 created impls, each returning a distinct
    sentinel, called through one site. With the default `-fanout` the output has
    one group block of 9 entries. With `-fanout=0` there are 9 full entries.
  - `unreachable_warning.txtar`: a Target not reachable from `main` → the warning
    on stderr, and the result still printed.
- `internal/resolve` unit test: on the `callgraph-algorithms` toy (a `main` with an
  `http.HandleFunc` closure, `f(r io.Reader)`, `apply(cb)`), `cb()` resolves to
  exactly 1 Candidate and `r.Read` to at most 5. This checks the recipe against
  the research.
- `-debug=timing` prints the `cha=`, `vta1=` and `vta2=` phases.
- S04's `dynamic_unresolved.txtar` keeps its `unresolved(no-candidates)` (a nil
  func var has no Candidates).

**Out of scope for this step.** Narrowing by local context (S09), synthetic roots
and test roots (S12), reflection (S10), guards.

---

### S07: Guard filtering per return path

**Goal.** Remove errors that are handled by `==`, `errors.Is`, `errors.As`, type
switches and comma-ok assertions, per return path, and mark the undecidable cases
`possibly handled` (P07).

**Depends on.** S05 (Wrap Chains for `Is`/`As`), S06.

**Files/packages touched.** `internal/flow/{guards.go,*_test.go}`, `internal/flow/*`,
`internal/summary/*` (pending guards on `Param`), `internal/errset/*` (GuardNote),
`internal/render/text.go`, `internal/cli/testdata/e2e/guards/*.txtar`.

**Implementation notes.**
- SSA has no refinement nodes (`error-flows-in-ssa`, §3), so:
  - For each `Return` block R and each error value `v` it returns, collect the `If`
    instructions whose block dominates R (`BasicBlock.Dominates`, walking `Idom()`).
  - For each such `If`, the guard holds with outcome **true** if `Succs[0]`
    dominates R, and **false** if `Succs[1]` dominates R. If neither dominates R,
    the guard doesn't apply on that path.
  - For a `Phi`, apply the guards that dominate the predecessor `Preds[i]` (plus
    the edge itself when the predecessor is the `If` block) to edge `i`, then
    take the union.
  - A guard applies to `v` when its tested operand is `v` itself or reaches `v`
    through `ChangeInterface` or `ChangeType`.
- Recognisers (match on SSA shape, not on syntax). `ssautil.Switches` misses
  `case io.EOF:`, so match the `BinOp` chains directly.

  | SSA shape | True outcome keeps | False outcome removes |
  |---|---|---|
  | `BinOp{EQL, v, *Global load}` (and `NEQ` with outcomes swapped); value `switch` chains | values that may be exactly that sentinel (the Sentinel with no wrapping; Unresolved stays) | the exact unwrapped Sentinel only |
  | `BinOp{EQL, v, nil}` | nothing (v is nil) | nothing |
  | `errors.Is(v, S)` call → `If` (possibly through `UnOp{NOT}`) | values whose tree contains S at any depth, plus values that may match (see below) | values whose tree **definitely** contains S |
  | `errors.As(v, &T)` (target type = the `Alloc`'s element type behind `MakeInterface`) | values whose tree contains an Origin or Wrap whose type is identical to T (concrete T) or implements T (interface T, `types.Implements`), plus may-match values | values that definitely contain a matching type |
  | `TypeAssert{CommaOk}` + `Extract #1` → `If` (type switch cases and `x, ok := v.(T)`) | values whose **top-level** dynamic type matches T (a Wrap's type is its wrapper type, such as `*fmt.wrapError`) | top-level matches |

- **May-match, so keep the value and add a `GuardNote`**:
  - a value with a custom `Is(error) bool` or `As(any) bool` method anywhere in
    its tree (method-set lookup);
  - Unresolved or Opaque values under `errors.As` with an interface target;
  - Unresolved values under any guard;
  - a guard whose tested operand is the result of a helper call (for example
    `isRetryable(err)`), noted as `guarded by call to <fn>`.

  On the outcome where the value would be removed, it is kept and marked
  `possibly handled at <span>: <reason>`.
- Guards on `Param` values: attach the guard to the `Param` fact as
  `PendingGuards` and apply it during substitution (S04's machinery).
- Outcomes inside the guarded body ("return nil", "return a replacement", "return
  a wrap of v") need no special code. They follow from filtering `v` per path and
  from S05's Wraps. Add tests that show this.

**Acceptance criteria.**
- E2E fixtures in `internal/cli/testdata/e2e/guards/`, where `source()` returns
  `io.EOF`, `fs.ErrNotExist`, `&MyErr{}` and `errors.New("x")`:
  - `eq_return_nil.txtar`: `if err == io.EOF { return nil }; return err` → no io.EOF; the others remain.
  - `neq_chain.txtar`: `if err != nil && err != io.EOF { return err }; return nil` → no io.EOF.
  - `value_switch.txtar`: `switch err { case io.EOF, fs.ErrNotExist: return nil }; return err` → only MyErr and the opaque entry.
  - `is_wrapped.txtar`: `source2()` returns `fmt.Errorf("w: %w", io.EOF)` and ErrA; `if errors.Is(err, io.EOF) { return nil }` → only ErrA (the wrapped io.EOF is removed).
  - `is_replace.txtar`: `if errors.Is(err, fs.ErrNotExist) { return ErrLocal }; return err` → ErrLocal is added, fs.ErrNotExist is removed.
  - `as_wrap.txtar`: `var pe *MyErr; if errors.As(err, &pe) { return fmt.Errorf("my: %w", err) }; return err` → MyErr appears **only** with the `my: %w` chain; the others appear unwrapped.
  - `as_iface.txtar`: `errors.As(err, &timeoutIface)` → the Concrete Types that implement it are removed on the false path; the Opaque entry is kept and marked `possibly handled`.
  - `type_switch.txtar`: `case nil:` / `case *MyErr: return nil` / `default: return err` → no MyErr.
  - `type_switch_wrapped.txtar`: a type switch on `*MyErr` when MyErr only reaches it wrapped by `fmt.Errorf` → MyErr is **kept** (a type switch only checks the top level).
  - `custom_is.txtar`: a type with `Is(target error) bool` under `errors.Is(err, io.EOF)` → kept, `possibly handled`.
  - `helper_guard.txtar`: `if isRetryable(err) { return nil }` → kept, `possibly handled at …: guarded by call to example.com/fx.isRetryable`.
  - `param_guard.txtar`: `func filter(err error) error { if err == io.EOF { return nil }; return err }` queried at `g() { return filter(io.EOF) }` → 0 entries.
  - `readatleast_oracle.txtar`: `func g() error { _, err := io.ReadAtLeast(strings.NewReader("x"), buf, 4); return err }` → contains `io.ErrShortBuffer`, `io.ErrUnexpectedEOF` and `io.EOF`. io.EOF stays because the `else if n > 0 && err == EOF` replacement only happens on one path (when n == 0 the EOF is returned). This checks that a guarded replace is path-specific and that a guard that doesn't dominate the merge removes nothing.
- Header counts include `<H> possibly handled` when H > 0.
- `internal/flow` unit tests: dominance-based guard collection on hand-picked
  functions (an `&&` chain, an `||` chain, a guard that doesn't dominate the return).

**Out of scope for this step.** Helper-guard summaries, analysing the bodies of
custom `Is`/`As` methods (always may-match in v1), flow-sensitive tracking beyond
dominance (excluded by the map).

---

### S08: Deferred changes to named results

**Goal.** Include errors that deferred functions assign to named results, in
closures, helpers and the `Recover` exit, so the Error Set covers "named results
changed in defer" (a map standing decision).

**Depends on.** S06 (FreeVar and Alloc-store helper, Candidates for deferred dynamic calls).

**Files/packages touched.** `internal/flow/{defer.go,*_test.go}`, `internal/summary/*`,
`internal/cli/testdata/e2e/defer/*.txtar`.

**Implementation notes.** Follow `error-flows-in-ssa` §4:
- **Result variables:** in a function with `Recover != nil`, results are never
  lifted (`lift.go:402-410`). Each `Return` returns a load `*R` of a result
  `Alloc` R. The "returned operand" is the last `Store` to R before `RunDefers`
  on that path (same block, or reached through predecessors).
- **Deferred effects:** collect every `*ssa.Defer` in the function.
  - Map R to the deferred callee: if the deferred value is a `MakeClosure` with R
    in `Bindings[i]`, the callee's `FreeVars[i]` is R. If R is passed as
    argument j, the callee's `Params[j]` aliases R.
  - Follow nested closures transitively (a binding passed on as a FreeVar).
  - The callee's effect is the values it `Store`s to that address. Analyse these
    with `flow` and summaries: they may be calls, Wraps of `*R`, or guarded
    (`if err != nil { err = fmt.Errorf("…%w", err) }`).
  - Value of R at return = **old' ∪ stores**, where old' is the old value
    filtered by the S07 guards that dominate the callee's exits on the paths
    with no store (so `if err != nil { err = wrap(err) }` leaves no unwrapped
    non-nil value). If every path stores, old' is empty. A Wrap of `*R` inside
    the closure wraps the old set.
  - When there are several defers, apply them in reverse program order (LIFO).
    This is exact for straight-line code and an approximation in loops and branches.
  - Deferred dynamic calls get their Candidates from the Resolver.
- **The `Recover` block** returns `*R` too. Its values are the stores made by the
  deferred functions (the zero value nil is dropped).
- **Range-over-func corner case:** a `Defer` with a `DeferStack` operand inside a
  yield closure, where the parent has no `RunDefers` (`lift.go` deletes it). Treat
  any `Defer` whose `DeferStack` is (through FreeVars) the parent's
  `defer$stack` as running at **every** parent `Return`. Don't use `RunDefers` as
  the anchor.
- Defers that never receive R (for example `defer f.Close()` with R not escaping)
  have no effect. Prove this by checking that R isn't in any binding or argument.

**Acceptance criteria.**
- E2E fixtures in `internal/cli/testdata/e2e/defer/`:
  - `closure_wrap.txtar`: `defer func(){ if err != nil { err = fmt.Errorf("D2: %w", err) } }(); return source()` → every Origin of `source()` appears **only** with the chain `fmt.Errorf "D2: %w"`, and there are no unwrapped entries: on the closure path without the store, the `err != nil` guard (S07) leaves only nil.
  - `close_overrides_nil.txtar`: the classic `defer func(){ if cerr := f.Close(); cerr != nil && err == nil { err = cerr } }()` around `os.Open` → includes the Close error's Origins and the Open errors.
  - `helper_ptr.txtar`: `defer setErr(&err)`, where `setErr` does `*p = ErrLocal` → includes ErrLocal.
  - `recover_exit.txtar`: `defer func(){ if r := recover(); r != nil { err = fmt.Errorf("panic: %v", r) } }()` → includes `opaque fmt.Errorf "panic: %v"`.
  - `defer_no_effect.txtar`: `defer f.Close()` with the result unchanged → the same set as without the defer.
  - `nested_defer_closure.txtar`: a defer closure that creates another closure assigning err → that assignment is included.
  - `multiple_defers_lifo.txtar`: two defers, each wrapping err → the chain order matches LIFO.
  - `rangefunc_defer.txtar`: a `defer` inside a `for range seq` body assigning `err = ErrX` → ErrX is included.
- No earlier golden changes, except fixtures that use `defer` (reviewers check each one).

**Out of scope for this step.** Deciding which panics are recovered (impossible
statically), and panics as errors in general (the map excludes them).

---

### S09: Call-site Queries and local narrowing

**Goal.** Give call-site Targets real semantics: report the errors **this call**
can produce, using the site's arguments and the local type of its receiver. Apply
the same narrowing at every substitution, which recovers the per-caller precision
VTA lacks (P08).

**Depends on.** S06, S07.

**Files/packages touched.** `internal/query/*`, `internal/summary/{narrow.go,*}`,
`internal/flow/{localtypes.go,*}`, `internal/errset/*` (Candidate counts on hops),
`internal/render/text.go`, `internal/cli/testdata/e2e/callsite/*.txtar`.

**Implementation notes.**
- **Call-site Query:** for each `(fn, site)` in `Target.SiteByFunc`, the result
  is the union over `Resolver.Callees(site)` of the callee's summary, substituted
  with this site's actual arguments (from `flow` in `fn`). Arguments that are
  `fn`'s own parameters → `depends-on-caller`, as for declarations. The header says
  `Error Set of call to <expr> in <fn>`. Path hop 0 … n ends at the Target site.
- **Local receiver narrowing at the Target site:** if the site is dynamic, compute
  the **local type set** of the receiver or function value in `fn`: walk its
  definitions through `ChangeInterface`, `Phi` and `Extract`. A
  `MakeInterface{X}` gives `X.Type()`; `*ssa.Function` and `MakeClosure` give the
  function. Any other definition makes the set **incomplete**. If the set is
  complete, Candidates = VTA ∩ local set. Otherwise use the VTA Candidates. Record
  `k of n candidates` on the hop.
- **Narrowing at every substitution:**
  - Facts produced through a dynamic call on `Param(i)` (the receiver or function
    value is a parameter) carry a `(paramIndex, Candidate)` tag.
  - During substitution at a caller whose argument `i` has a complete local type
    set, facts whose Candidate isn't in the set are dropped.
  - Tags are carried through `Via` provenance, so narrowing composes across levels.
  - This is sound because an argument with a complete local set can only hold
    those types at that site.
  - Narrowing must never add Candidates.

**Acceptance criteria.**
- E2E fixtures in `internal/cli/testdata/e2e/callsite/`:
  - `local_receiver.txtar`: `var r io.Reader = strings.NewReader("x"); _, err := r.Read(b)`, with the Query on `r.Read` → only `*strings.Reader.Read`'s errors (io.EOF), even though `main` elsewhere also passes a `*bufio.Reader` to an `io.Reader`. The hop shows `1 of N candidates`.
  - `param_narrowing.txtar`: `func use(g Getter) error { return g.Get() }` is called from two places, with `A{}` (ErrA) and with `B{}` (ErrB). The call-site Query on `use(A{})` → ErrA only. The declaration Query on `use` → ErrA and ErrB.
  - `narrowing_two_levels.txtar`: `outer(g)` → `inner(g)` → `g.Get()`. A call site `outer(A{})` → ErrA only.
  - `incomplete_local_set.txtar`: the receiver comes from a function result → no narrowing; all VTA Candidates are shown.
  - `callsite_args.txtar`: the Query on `wrap(ErrX)` where `wrap` wraps its parameter → ErrX with the wrap chain, and no depends-on-caller.
  - `callsite_param_arg.txtar`: the Query on `wrap(e)` inside `func f(e error)` → `unresolved(depends-on-caller): parameter e (error) of example.com/fx.f`.
  - `callsite_generic.txtar`: a call inside a generic function that is instantiated twice → the union over instances, with the instance named in the hop.
- The S03 call-site placeholder behaviour is gone. All earlier call-site goldens
  now show real sets.

**Out of scope for this step.** Narrowing on values stored in fields (excluded by
the map), narrowing based on guards after the call, symbol Targets.

---

### S10: Unresolved completeness and sentinel identity

**Goal.** Make sure every way errtrace can lose track of a value surfaces as an
explicit Unresolved entry, and make Sentinel identity correct for aliases and
mutable globals. This is the "never silently dropped" audit.

**Depends on.** S08, S09.

**Files/packages touched.** `internal/flow/*`, `internal/summary/*`, `internal/errset/*`,
`internal/cli/testdata/e2e/unresolved/*.txtar`, `internal/flow/audit_test.go`.

**Implementation notes.**
- **Audit test** (`internal/flow/audit_test.go`): load a fixture program, and for
  every reachable function with an error result, check that every SSA value kind
  that can flow into an error result is handled by a classifier. There must be no
  `default:` fall-through that returns nothing. Implement this with a table of
  handled instruction types plus a test that runs the flow walker on stdlib
  packages (`os`, `net/http`, `database/sql`) and fails on any "unclassified"
  internal sentinel. `flow` must return
  `unresolved(memory, "unclassified <instr type>")` rather than drop a value, and
  the test asserts that this detail never appears for the listed packages.
- **New and completed kinds:**
  - `recover`: a value derived from `recover()` through a `TypeAssert`.
  - `reflection`: Candidates of `reflect.Value.Call`, `CallSlice` and
    `reflect.Value.Method(...).Call`.
  - `memory`: complete the list (map `Lookup`, slice and array `IndexAddr` loads,
    a pointer deref of a non-local `Alloc`, a channel receive, a global that isn't
    error-typed but holds an error inside a struct).
  - `no-body` also covers cgo calls (`_Cfunc_*`) and `//go:linkname` functions
    without bodies.
- **Error built through a function-valued global** (`var newErr = errors.New`;
  `return newErr("x")`): VTA gives the Candidate `errors.New`. Make sure the
  intrinsic table applies to Candidates as well as to static callees.
- **Sentinel aliases** (`var ErrX = io.EOF`): scan the package `init` for
  `Store{Addr: Global ErrX, Val: load of Global io.EOF}`. Report `sentinel io.EOF`
  with `Via: [ErrX]` (rendered `sentinel io.EOF (via example.com/fx.ErrX)`), so
  that `errors.Is(err, io.EOF)` guards treat them as one.
- **Mutable sentinels:** `(*Global).Referrers()` is always nil
  (`ssa.go:1551`). Scan the reachable functions once for `Store`s whose `Addr` is
  the Global, ignoring the package `init`. If any exist, the entry is marked
  `[mutable global: assigned at <span>]`. The values stored there are **not**
  added (that is beyond v1's tracking). The mark is enough.
- Opaque Errors from dynamic `errors.New` messages (`errors.New(msg)`) stay
  Opaque, with an empty message. They are never dropped (the goexhauerrors pitfall).

**Acceptance criteria.**
- E2E fixtures in `internal/cli/testdata/e2e/unresolved/`:
  - `struct_field.txtar`: `return s.err` → `unresolved(memory)` (this replaces the
    S03 `field_load.txtar` detail, if it changed).
  - `map_lookup.txtar`: `return errs[k]` → `unresolved(memory)`.
  - `chan_recv.txtar`: `return <-errc` → `unresolved(memory)`.
  - `recover_assert.txtar`: `if e, ok := recover().(error); ok { err = e }` in a defer → `unresolved(recover)`.
  - `reflect_call.txtar`: `reflect.ValueOf(f).Call(nil)[0].Interface().(error)` → `unresolved(reflection)`.
  - `cgo_call.txtar`: only if cgo is available (`t.Skip` when `CGO_ENABLED=0`) → `unresolved(no-body)`.
  - `func_var_global.txtar`: `var newErr = errors.New; return newErr("x")` → `opaque errors.New "x"`.
  - `sentinel_alias.txtar`: `var ErrX = io.EOF; return ErrX` → `sentinel io.EOF (via example.com/fx.ErrX)`; a guard `errors.Is(err, io.EOF)` removes it.
  - `mutable_sentinel.txtar`: `ErrX` reassigned in `SetErr()` → `[mutable global: assigned at …]`.
  - `dynamic_message.txtar`: `errors.New(msg)` → `opaque errors.New` with no message.
  - `sql_row_scan_oracle.txtar` (stdlib oracle from go-errorlint): `(*sql.Row).Scan` → contains `sentinel database/sql.ErrNoRows`, plus `unresolved(memory)` for `r.err`.
- `go test ./internal/flow -run Audit` passes and walks at least `os`, `net/http` and `database/sql`.

**Out of scope for this step.** Tracking values through fields or channels
(excluded by the map), JSON, roots.

---

### S11: JSON output with a stable schema

**Goal.** `-json` prints the full Error Set in the `errtrace/v1` schema, and the
schema is documented and tested so it stays stable.

**Depends on.** S10 (all Unresolved kinds exist).

**Files/packages touched.** `internal/render/{json.go,json_test.go,schema.md}`,
`internal/span/*` (if the JSON span needs fixes), `internal/cli/cli.go`,
`internal/cli/testdata/e2e/json/*.txtar`.

**Implementation notes.**
- Implement the [JSON contract](#json-s11) with explicit structs (field order =
  struct order) and `omitempty` on optional fields. The Unresolved `kind` is a
  string from the closed enum in the [Data model](#data-model-internalerrset),
  and the renderer rejects unknown kinds in tests.
- JSON never groups by fan-out and always includes every path hop. `-fanout` is
  ignored with `-json`.
- Use `"schema": "errtrace/v1"`. Any later incompatible change must bump it; say
  this in `schema.md`.
- `schema.md` lists every field, its type, when it is present, and the
  Unresolved kind enum. It notes that `column` counts UTF-8 bytes and that
  `offset` is 0-based.
- The harness masks `$GOROOT` spans inside JSON (Conventions).

**Acceptance criteria.**
- E2E fixtures in `internal/cli/testdata/e2e/json/`: `sentinel.txtar`,
  `wrap_chain.txtar`, `dynamic_fanout.txtar` (9 Candidates: all of them are
  present, with no grouping), `unresolved_kinds.txtar` (one fixture that produces
  at least 6 different Unresolved kinds), `possibly_handled.txtar`,
  `callsite.txtar`, `empty.txtar` (`"entries": []`, not `null`).
- `json_test.go`: a round-trip test (unmarshal the output into the structs and
  marshal it again → the same bytes), a test that every Unresolved kind constant
  appears in `schema.md`, and a test that marshalling an entry with no optional
  data gives no `null`s.
- `errtrace -json` exits 0 and its output parses with `encoding/json`.
  `python3 -m json.tool` (if present) accepts it. A reviewer can run
  `go run ./cmd/errtrace -json <fixture path>` in an extracted fixture.

**Out of scope for this step.** JSON for usage errors (they stay as stderr text,
as in gopls), LSP shapes.

---

### S12: Query roots for libraries and tests

**Goal.** Implement P09 in full: `-tests` roots, synthetic roots for libraries
without `main`, and `depends-on-caller` for Candidates that come from parameters.
This replaces S06's warning.

**Depends on.** S11.

**Files/packages touched.** `internal/resolve/roots.go`, `internal/load/*` (Tests),
`internal/summary/*` (CallerSupplied), `internal/flow/*`, `internal/query/*`,
`internal/cli/cli.go` (`-tests`), `internal/cli/testdata/e2e/roots/*.txtar`.

**Implementation notes.**
- `-tests` sets `packages.Config.Tests = true`. Roots then include the `main` of
  the generated test main packages (`ssautil.MainPackages` finds them). Their
  `tests`, `benchmarks`, `fuzzTargets` and `examples` tables reference every
  `Test*` and the others as function operands, so `forwardSlice` reaches them.
  `load.FileFor` keeps preferring non-test variants unless the file is a `_test.go`.
- **Synthetic roots:** if no main package is among the initial packages, or a
  Target function isn't in the main/init slice, add as roots every exported
  package-level function and every exported method of exported named types
  (non-generic, plus instances already in the program) in the initial non-main
  packages. Recompute the slice and VTA once. Remove the S06 warning.
- **CallerSupplied:**
  - A dynamic call whose receiver or function value traces (through `flow`) to
    `Param(i)` and that has **0 Candidates** produces a symbolic
    `CallerSupplied{fn, i, method}` fact instead of `no-candidates`.
  - During substitution it maps through the caller's argument. If that argument
    is `Param(j)`, it propagates. If it is a value with a complete local type set,
    the Candidates come from that set: add these callees to the demand graph
    (re-solve the affected SCCs). Otherwise it becomes `unresolved(no-candidates)`.
  - At the top of a Query it becomes
    `unresolved(depends-on-caller): caller-supplied <type> (parameter <name> of <fn>)`.
- The demand graph has to be extended during solving for the complete-local-set
  case. The simplest correct approach is to rerun the solver from the start when
  new callees appear, with an outer cap of 3 reruns.

**Acceptance criteria.**
- E2E fixtures in `internal/cli/testdata/e2e/roots/`:
  - `library_no_main.txtar`: a library `func Parse(r io.Reader) error { _, err := r.Read(buf); if err != nil { return fmt.Errorf("parse: %w", err) }; return ErrEmpty }` →
    `sentinel ErrEmpty` and `unresolved(depends-on-caller): caller-supplied io.Reader (parameter r of example.com/lib.Parse)` with the `parse: %w` chain. There is no stderr warning.
  - `library_internal_caller.txtar`: the library also has `func ParseString(s string) error { return Parse(strings.NewReader(s)) }`. Querying `ParseString` → io.EOF through `*strings.Reader.Read` and no depends-on-caller. Querying `Parse` → still depends-on-caller.
  - `library_callback.txtar`: `func Run(cb func() error) error { return cb() }` → `depends-on-caller: caller-supplied func() error`.
  - `tests_roots.txtar`: the only `main`-like caller of a helper is a `TestX`. Without `-tests`, the helper's interface parameter is depends-on-caller. With `-tests`, the Candidates come from the test.
  - `main_still_used.txtar`: a module with a `main` → the same results as before this step (regression guard; the S06 fixtures stay unchanged except for the removed warning).
- S06's `unreachable_warning.txtar` is updated: no warning, and the result has
  depends-on-caller where it applies.

**Out of scope for this step.** A user-supplied `-root` flag, falling back to all
implementers for a parameter (ruled out).

---

### S13: Depth and package-boundary limits, symbol-name Targets

**Goal.** Add the map's "depth and package-boundary limits" and the symbol-name
form of a Target (`gopls-io-conventions`, Implication 4). Check the cold-time budget.

**Depends on.** S12.

**Files/packages touched.** `internal/query/*`, `internal/summary/*` (cut-offs),
`internal/target/{symbol.go,*_test.go}`, `internal/cli/cli.go` (`-depth`, `-boundary`),
`internal/cli/testdata/e2e/{limits,symbol}/*.txtar`.

**Implementation notes.**
- **Depth:** compute the minimum call-hop distance from the Target functions in
  the demand graph with a BFS. A callee at distance > `-depth` is not summarised.
  Its result facts become `unresolved(depth-limit)` with the detail `call to <fn>
  not followed (depth N)`. `0` means unlimited, the default.
- **Boundary:**
  - `all` (default) follows everything.
  - `module` follows only callees whose package belongs to a main (workspace)
    module (`packages.Package.Module.Main`).
  - `package` follows only callees in the Target's package.
  - Callees outside the boundary become `unresolved(boundary-limit)` naming the
    callee and its package.
  - Intrinsics (`errors.New`, `fmt.Errorf`, …) are always modelled, whatever the boundary.
- Summaries that sit above a cut-off are flagged `Limited`. S14 must not cache them.
- **Symbol-name Targets** (`target/symbol.go`):
  - Grammar: `<importpath>.<Func>`, `<importpath>.<Type>.<Method>`,
    `(*<importpath>.<Type>).<Method>`, and `(<importpath>.<Type>).<Method>`.
  - The import path is the longest prefix that names a loaded package. Split on
    the last `/`, then on dots after it.
  - If the argument doesn't parse as a position (a file that exists, with a
    `:L:C` or `#O` suffix), try the symbol form.
  - Load the package with `-p` plus the symbol's package path.
  - Look up the object in `pkg.Types.Scope()`. For methods, use the method set of
    `T` or `*T`, depending on the receiver form.
  - Generic functions get the instance handling from S06.
  - Errors: `no package <path> loaded`, `no symbol <name> in <path>`,
    `<name> is an interface method; query a call site`.
  - No fuzzy search (gopls `workspace_symbol` is a separate feature, out of scope).
- **Performance check** (not a CI gate): `-debug=timing` is complete across all
  phases. Record a measurement in the step's commit message.

**Acceptance criteria.**
- E2E fixtures in `internal/cli/testdata/e2e/limits/`:
  - `depth1.txtar`: `A → B → C(ErrX)` with `-depth=1` → `unresolved(depth-limit)`
    for the call to C, inside B's path. With `-depth=2` → ErrX.
  - `boundary_module.txtar`: the fixture's main module `example.com/fx` requires
    a local dependency module through `replace example.com/dep => ./dep` (a
    filesystem replace needs no go.sum). With `-boundary=module`, calls into
    `example.com/dep` → `unresolved(boundary-limit)`, and stdlib calls such as
    `os.Open` → `boundary-limit` too, while `fmt.Errorf` is still modelled. With
    the default `-boundary=all`, the dependency's Origins appear.
  - `boundary_package.txtar`: a call to a sibling package in the same module →
    `boundary-limit`.
- E2E fixtures in `internal/cli/testdata/e2e/symbol/`: `func.txtar`
  (`example.com/fx.Load`), `ptr_method.txtar` (`(*example.com/fx.T).Get`),
  `value_method.txtar` (`example.com/fx.T.Get`), `generic.txtar`, `stdlib.txtar`
  (`strconv.Atoi` gives the same entries as S05's oracle), and
  `errors.txtar` (unknown package, unknown symbol, interface method → exit 2
  with the gopls-style message).
- `internal/target` unit tests for parsing the symbol grammar (including import
  paths with dots, such as `gopkg.in/x.v2.Func`).
- **Manual budget check (the reviewer runs it):** from the repo root, build
  the binary into a scratch directory (`go build -o <scratch>/et ./cmd/errtrace`),
  then `time <scratch>/et -debug=timing github.com/killedKenny/errtrace/internal/load.Load`
  completes in under 10 s wall time on the dev machine with a warm Go build cache. Report the phase timings. If it misses the budget,
  record that in P05/P08's "revisit" notes instead of optimising in this step.

**Out of scope for this step.** Fuzzy symbol search, per-flag defaults in a
config file, the cache.

---

### S14: Optional on-disk summary cache

**Goal.** Make warm Queries close to instant (the map's speed target) by caching
complete summaries on disk. This is the last step and is **optional**: v1 is
usable without it, and `-cache` is off by default (P08).

**Depends on.** S13.

**Files/packages touched.** `internal/cache/*`, `internal/summary/*`
(serialisable form), `internal/query/*`, `internal/cli/cli.go` (`-cache`, `-cache-dir`),
`internal/cli/testdata/e2e/cache/*.txtar`, `internal/cache/*_test.go`.

**Implementation notes.**
- **Fingerprint:**
  - Do a metadata-only `packages.Load` (`NeedName|NeedFiles|NeedCompiledGoFiles|NeedImports|NeedDeps|NeedModule`,
    with the same patterns, tags, Tests and env).
  - Hash, in order: each package's ID, its `CompiledGoFiles` contents (sha256),
    `runtime.Version()`, GOOS and GOARCH, the build tags, the `-tests`,
    `-boundary` and `-chain-depth` values, and an `analysisVersion` constant
    (bump it whenever summary semantics change).
  - Any change invalidates the whole program cache. This is coarse but correct,
    because VTA Candidates depend on the whole program.
- **What is stored:**
  - for each function key (an `objectpath`-like string, for example
    `example.com/fx.(*T).Get` or `example.com/fx.Map[int]`, plus a closure index
    for anonymous functions), the serialised summary with its provenance
    (positions as file + offset);
  - the Target index (function key and call-site key = file + `Lparen` offset);
  - only summaries **not** marked `Limited` (S13) are written.
- **Warm path:**
  - Compute the fingerprint.
  - Resolve the Target syntactically: parse just the target file for a position
    (find the `FuncDecl` or `CallExpr`, then compute the key). For a symbol,
    compute the key from the name.
  - If every summary needed (the Target's and, transitively, those on the
    rebuilt paths) is in the cache, render without `load.Load(LoadAllSyntax)` or
    SSA.
  - Otherwise fall back to the full pipeline and then write the cache.
- **Storage:** one directory per fingerprint under `-cache-dir` (default
  `os.UserCacheDir()/<BinaryName>`). Use gob or JSON files, written to a temp file
  and renamed, so concurrent runs are safe. Old fingerprints are left alone
  (pruning is out of scope). Corrupt entries are treated as a miss.

**Acceptance criteria.**
- `internal/cache` unit tests: the fingerprint changes when a file byte changes,
  when tags change and when `analysisVersion` changes; it stays the same across
  runs with the same inputs; a corrupt file is a miss; concurrent writers don't
  corrupt each other (`-race`, 8 goroutines).
- E2E fixture `internal/cli/testdata/e2e/cache/warm.txtar`: the harness runs the
  same Query twice with `-cache -cache-dir=$WORK/cache -debug=timing`. The second
  run's stdout is byte-identical to the first's, and its stderr contains
  `timing: cache=hit` with **no** `ssa=` line. A third run, after the fixture
  edits a file through a harness hook, gives `cache=miss`.
- Output with `-cache` equals output without it for every fixture in
  `internal/cli/testdata/e2e/` (a test loops over them with the cache on, using a
  shared temp cache dir).
- A Query with `-depth` or a non-default `-boundary` still works with `-cache`,
  and writes no limited summaries.

**Out of scope for this step.** Per-package incremental invalidation, cache
pruning, sharing a cache between machines, any daemon or LSP mode.

---

## Coverage of the map

**Standing decisions → steps.**

| Map standing decision | Where |
|---|---|
| Query tool, CLI first | S01–S03, CLI surface |
| Error Set: Sentinels, Concrete Types, Opaque Errors, Wraps as chains, each with a Propagation Path | S03, S04, S05 |
| Follow calls into stdlib and dependencies, with depth and boundary limits | S04, S13 |
| Candidates: traced implementers (VTA), no CHA flag | S06, P05 |
| Function values like interfaces; generics through instantiations | S06 |
| Call-site Queries narrow Candidates with local context | S09 |
| Guards `==`, `errors.Is`, `errors.As`, type switches | S07, P07 |
| Error results include named results changed in `defer` | S08 |
| Unresolved is always explicit | S03–S06, S10, S12, S13 |
| Speed: <10 s cold, near-instant warm | S13 (check), S14 (cache) |
| gopls positions and span output; JSON; symbol form; pinned x/tools | S01, S02, S11, S13, P-XT |

**The map's "Not yet specified" items.**

- *Test corpus & acceptance criteria:* each step's fixture list, plus the stdlib
  oracles taken from go-errorlint's table (`strconv.Atoi`, `io.ReadAtLeast`,
  `(*sql.Row).Scan`). A real-world medium module (beyond the self-hosted timing
  check in S13) is still open.
- *CLI surface:* defined provisionally in [CLI surface (v1)](#cli-surface-v1).
- *Cache storage & invalidation:* S14 (whole-program fingerprint, complete
  summaries only).
- *Wrap Chains through custom `Unwrap`:* S05 handles the field-returning
  `Unwrap` with complit fields. Other shapes are `unresolved(memory)`. Custom
  `Is`/`As` are always may-match (S07).
- *Spec assembly:* this plan stands in for the spec until tickets 05–10 are
  resolved for good.

**The map's Out of scope, respected.** No linter-style checks. No LSP or gopls
integration. No all-implementers (CHA) mode (CHA is only VTA's internal initial
graph). No flow-sensitive tracking beyond dominance-based guards. Panics are never
modelled as errors (the `Recover` block is handled only for values that defers
assign). Errors in struct fields and channels are reported as `unresolved(memory)`,
never tracked.
