# gopls input and output conventions

Type: research
Status: resolved
Blocked by:
Map: [errtrace v1 map](../map.md)

## Question

How does the gopls CLI accept positions and symbols (`file:line:col`, `file:#offset`, symbol queries), and what output formats do commands like `gopls implementation`, `references`, `call_hierarchy` produce (text and JSON)? How does gopls load packages (go/packages modes, build tags, workspace/module discovery), and which parts could errtrace reuse directly?

## Answer

Findings: branch `research/gopls-io-conventions`, file `research/gopls-io-conventions.md` (local only, never merged).

- **Input:** gopls takes `file.go:LINE:COL` and `file.go:#OFFSET` (plus ranges), as file paths only. Lines and columns start at 1 and columns count **bytes**; offsets start at 0. There is no symbol-name form; `workspace_symbol` is a separate search.
- **Output:** results are `/abs/path.go:L:C-C` span lines that can be fed back as input. Only `definition` has `-json`. `call_hierarchy` goes one level deep and stops at the interface method. Errors print `gopls: <msg>` to stderr with exit status 2.
- **Loading:** gopls looks upward for go.work, then go.mod, and calls `go/packages` for package lists only. Build tags come from `GOFLAGS`.
- **Reuse:** `gopls/internal` can't be imported. Usable instead: `go/packages`, `astutil.PathEnclosingInterval` / `inspector.Cursor.FindByPos`, `typeutil.StaticCallee`, `objectpath`, `ssa` + `callgraph/vta`.
- **Constraint:** the newest x/tools needs Go 1.26, and go.mod says 1.24.3. Pin x/tools on purpose, or raise the Go version.
- **Implications:** use the same position format with byte columns, add real JSON output and a way to name a symbol directly, build errtrace's own call graph, and follow gopls's error and exit-status conventions.
