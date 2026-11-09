# errtrace v1

Label: wayfinder:map
Tickets: [issues/](./issues/)

## Destination

An implementation-ready spec for **errtrace v1**: a CLI query tool that takes a gopls-style Target (declaration or call site) and reports its full Error Set (Origins, Wrap Chains, Propagation Paths), resolving interface methods, function values, and generics to Candidates recursively, including into the stdlib and dependencies.

## Notes

- Domain: Go static analysis. Vocabulary lives in [GLOSSARY.md](../../GLOSSARY.md); use its terms (Query, Target, Error Set, Error Origin, Wrap, Propagation Path, Candidate, Unresolved).
- Skills: `grilling` + `domain-modeling` for grilling tickets; `research` for research tickets; `prototype` for prototype tickets.
- Standing decisions from charting (2026-11-09):
  - Query tool, not a linter. CLI first, LSP later. Personal tool, possibly open source later; accuracy matters more than speed.
  - Error Set covers Sentinels, Concrete Types, Opaque Errors, and Wraps (as chains); each Origin comes with its Propagation Path.
  - Follow calls into the stdlib and dependencies, with depth and package-boundary limits.
  - Candidate resolution: "created implementers" (RTA-like) or "traced implementers" (VTA-like), whichever works without too much complexity. **No** all-implementers (CHA) flag.
  - Function values (closures, callbacks, method values) are resolved the same way as interfaces. Generics are resolved through their instantiations.
  - Call-site Queries narrow Candidates using local context.
  - Remove errors handled by common guard patterns (`==`, `errors.Is`, `errors.As`, type switches).
  - Error results: any result of type `error` or implementing `error`, plus named results changed in `defer`.
  - Anything that can't be resolved is shown as an explicit **Unresolved** entry, never dropped.
  - Speed target: under ~10s cold on a medium module, near-instant warm through cached summaries.
- Tracker: local markdown (`.scratch/`). No GitHub remote yet; move it to Issues if one is added.
- Implementation plan: [plan.md](./plan.md) (steps S01–S14, also listed in [plan-steps.json](./plan-steps.json)); tickets 05–10 were decided **provisionally** there (see its "Provisional decisions" section) and remain open until confirmed.

## Decisions so far

- [gopls input and output conventions](issues/03-gopls-io-conventions.md): match gopls positions (`file:L:C`, byte columns, `#offset`) and span-line output; add real JSON and a symbol-name form; build on go/packages + x/tools (pin it, since the newest needs Go 1.26)
- [How error flows look in SSA](issues/04-error-flows-in-ssa.md): Origins, Wraps, defers and generics are all recoverable from SSA; guards must be applied per return path (SSA has no refined copy of `err`); errtrace needs its own `%w` parser; formats built at runtime → Unresolved
- [Call-graph algorithms in golang.org/x/tools for resolving Candidates](issues/02-callgraph-algorithms.md): VTA (traced implementers), not RTA (RTA is barely better than CHA); runs fast enough; must be whole-program from roots (can't be scoped to one function); go/pointer is out; VTA tracks types only, so sentinel values need errtrace's own tracking
- [Prior art for computing Go error sets](issues/01-prior-art.md): no existing tool answers a Query with paths; goexhauerrors is closest (linter, SSA, no paths, drops Opaque Errors); govulncheck is the reference for whole-program VTA + shortest paths; the name clashes with braces.dev/errtrace

## Not yet specified

- **Test corpus & acceptance criteria**: which real-world code (and hand-written fixtures) the spec uses to define "correct" Error Sets; depends on the architecture and output format.
- **CLI surface**: commands, flags (depth, package boundary, format), and exit codes, once the output format and input conventions are settled.
- **Cache storage & invalidation**: may become its own ticket once the architecture ticket decides what is cached.
- **Wrap Chains through custom `Unwrap`**: whether types with custom `Unwrap`/`Is`/`As` methods need extra modelling beyond what the SSA research turns up.
- **Spec assembly**: the final pass that turns all the decisions into the handoff spec.

## Out of scope

- Linter-style checks (e.g. "caller doesn't handle ErrX"): a later effort built on the same engine.
- LSP server / gopls integration: after the CLI.
- All-implementers (CHA) resolution mode: ruled out at charting.
- Full flow-sensitive error-value tracking beyond the common guard patterns.
- Panics, errors stored in struct fields or sent over channels.
