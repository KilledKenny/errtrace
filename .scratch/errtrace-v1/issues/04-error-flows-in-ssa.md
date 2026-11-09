# How error flows look in SSA

Type: research
Status: resolved
Blocked by:
Map: [errtrace v1 map](../map.md)

## Question

In `golang.org/x/tools/go/ssa`, how do the following appear, and how could an analysis recognise each one? (1) returning a sentinel or a constructed error; (2) `fmt.Errorf` with `%w` (including multiple `%w`), `errors.Join`, and custom `Unwrap() error` / `Unwrap() []error`; (3) guards: `err == X`, `errors.Is`, `errors.As`, and type switches that return nil or replace the error; (4) named results changed in a deferred closure; (5) error values through phi nodes, interface conversions, and generic instantiations. Note what is easy, hard, or impossible to recover.

## Answer

Findings: branch `research/error-flows-in-ssa`, file `research/error-flows-in-ssa.md` (local only, never merged). Sources: x/tools v0.39.1-0.20260109155911-b69ac100ecb7 (the newest version that builds with Go 1.24) and Go 1.24.3 stdlib, plus about 60 SSA-dumped examples.

- **Origins are easy:** a sentinel is a load from a `Global`. A constructed error is an `Alloc` followed by `MakeInterface`, which carries the concrete type. `errors.New` / `fmt.Errorf` are matched by callee.
- **Wraps:** `fmt.Errorf` and `errors.Join` arguments can be traced back through their array. A constant format string is recoverable, but errtrace needs its **own `%w` / `%[n]w` parser** because x/tools' parser is internal. A non-constant or forwarded format can only be resolved one level up at the caller; a format built at runtime can't be resolved (→ Unresolved). Custom `Unwrap()` methods are found through the method set.
- **Guards:** SSA has no refined copy of `err` after a check, so guards must be applied **per return path** using dominating branch conditions. `case nil` shows up as `== nil`. `ssautil.Switches` misses `case io.EOF:`. Getting `errors.Is` right needs the wrap chains plus custom `Is`/`As` methods.
- **Deferred closures:** in a function with `defer`, every return stores, runs the defers, and reloads, so the common closure pattern is easy. `defer helper(&err)` needs analysis of the helper. Quirk: a defer inside a range-over-func loop body leaves no `rundefers` in the enclosing function.
- **Value flow:** phi nodes are easy; loops need a cap on wrap-chain depth. Interface-to-interface conversions are transparent. Generics are easy with `InstantiateGenerics`, using `Origin()` to map back to source. Values through struct fields, channels or maps are hard to impossible (consistent with what is out of scope).
