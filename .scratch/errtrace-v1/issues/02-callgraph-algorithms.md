# Call-graph algorithms in golang.org/x/tools for resolving Candidates

Type: research
Status: resolved
Blocked by:
Map: [errtrace v1 map](../map.md)

## Question

For `golang.org/x/tools/go/callgraph` (static, CHA, RTA, VTA) and the status of `go/pointer`: how precise is each at resolving interface-method calls and function values (closures, method values, callbacks) to Candidates; is it sound; how does it treat generics, reflection, and unsafe; what does it cost in time and memory on a program that pulls in a large slice of the stdlib; and can it be scoped to one call site or one entry point instead of the whole program? The goal is to decide between "created implementers" (RTA-like) and "traced implementers" (VTA-like) without adding an all-implementers mode.

## Answer

Findings: branch `research/callgraph-algorithms`, file `research/callgraph-algorithms.md` (local only, never merged). Benchmarked on x/tools v0.39.1-0.20260109155911-b69ac100ecb7 / Go 1.24.3.

- **Use VTA ("traced implementers"), not RTA.** On a program importing net/http, RTA was barely better than CHA: 139 / 99 / 45 Candidates at `err.Error()` / `io.Reader.Read` / a `func() error` callback, against 3 / 5 / 1 for VTA. Both are sound only for whole programs and only if reflection and unsafe are ignored.
- **Cost is fine:** VTA took 0.35–0.6 s on 171 packages / 15.3k functions. Peak memory was ~600 MB, mostly from package loading and building SSA.
- **Scoping to one function or call site doesn't work:** rooting at the queried function silently resolves parameters to 0 Candidates. Build one graph from main/init and walk it from the Query. A library with no `main` needs an explicit "depends on the caller" result.
- **go/pointer is out** (deleted in 2023, golang/go#59676; it crashes on Go 1.24 aliases).
- **Generics:** calls resolve inside instances; uninstantiated bodies get no edges, so errtrace must walk instances.
- **Limits:** VTA tracks *types*, not values, so every `errors.New` sentinel looks like the same `*errors.errorString` and errtrace needs its own value tracking for sentinels and `%w`. VTA merges all callers of a function, so a parameter's Candidates mix call paths. VTA's type sets at return values are unexported; reading them means forking the package.
- **Prototype shortlist:** (1) VTA the way govulncheck runs it (CHA → reachable slice from main/init → VTA twice); (2) VTA seeded with RTA's graph; (3) a forked VTA that exposes the type sets at error return values.
