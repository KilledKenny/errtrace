# Prior art for computing Go error sets

Type: research
Status: resolved
Blocked by:
Map: [errtrace v1 map](../map.md)

## Question

What existing tools, libraries, or papers already compute "which errors can this Go function return" (or something close)? For each: what it reports (sentinels, concrete types, wrap chains), how it handles interfaces and function values, what analysis it builds on, and whether it's reusable or shows a pitfall to avoid. Include at least: errcheck, go-errorlint, staticcheck, gopls, any "exhaustive errors"/"error set" analyzers, and the existing `bracesdev/errtrace` (name clash; what it does).

## Answer

Findings: branch `research/prior-art`, file `research/prior-art.md` (local only, never merged). The tools' source was read at pinned versions via `go mod download`; not every cited line number was spot-checked.

- **No existing tool answers a Query for one chosen function with paths.** The closest is **goexhauerrors** (YuitoSato): a linter that builds per-function error sets from SSA (sentinels, custom types, `%w`, closures, interface calls). It drops Opaque Errors, values in struct or map fields, and unexported errors across packages, reports no paths, and its global store for cross-package implementations has a data race its README admits to. **go-serum-analyzer** works on the syntax tree and stops at package boundaries.
- **errcheck** does no resolution. **go-errorlint** keeps a hand-written stdlib table of which functions return which sentinel, matches interface methods by name, and rejects function values. **staticcheck** treats interface calls, closures and mutual recursion as unknown. **gopls** has no error query.
- **What to reuse:** `go/ssa` and the CHA and VTA call graphs. **govulncheck** is the reference for running them whole-program, stdlib included, and for finding a shortest call path per finding.
- **Name clash:** `braces.dev/errtrace` is a runtime library plus a source rewriter that inserts `errtrace.Wrap`, and it installs a binary named `errtrace`. Rename before release.
- **Implications:** build a whole-program query tool, not a go/analysis pass. Compute summaries over cycles in the call graph. Report Opaque and Unresolved Origins explicitly. Treat `%w`, `errors.Join`, custom `Unwrap` and `errtrace.Wrap` as wraps. Use go-errorlint's stdlib table as test cases.
