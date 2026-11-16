# Test applications

Each directory here is a **test application**: a small but realistic Go module
(stdlib only, kept as ordinary files so it builds and vets on its own) plus a
set of Queries against it. `TestApps` in `internal/cli/apps_test.go` runs
every Query in-process.

Unlike the single-purpose fixtures in `../e2e`, an application combines many
features in one program, so Queries exercise their interactions.

## Applications

| App | Queries | Focus |
|---|---|---|
| `smoke` | 1 | harness check |
| `kvstore` | 88 | Error Origins and propagation across layers |
| `wrapchain` | 79 | Wraps and Wrap Chains, `-chain-depth` |
| `guards` | 80 | guard filtering, holding and not holding |
| `plugins` | 88 | dynamic calls, Candidates, `-fanout` |
| `generics` | 85 | generic functions, types and instantiations |
| `stdlibio` | 85 | following errors into the standard library; Unresolved constructs |
| `targets` | 198 | CLI surface, Target resolution, package loading |
| `bank` | 78 | larger realistic app: feature interactions |

## Layout

    <app>/
      go.mod            module example.com/<app>, no dependencies
      README.md         what the app models and a table of its queries
      ...               Go packages
      queries/
        <name>.txtar    one Query: comment, args, stdout, stderr, exit

A query archive holds only the sections `args`, `stdout`, `stderr` (default
empty) and `exit` (default 0). Its leading comment says what the Query checks
and what the correct Error Set is. In `args`, `file.go:@name` becomes the
`file.go:L:C` of the byte right after the comment `/*@name*/` in that file,
and `file.go:#@name` its `file.go:#OFFSET`; file paths are relative to the
application directory. Each marker name is unique within its file.

Markers sit directly before the token they point at (`func /*@load*/load(`),
which gofmt would separate; the Go files are therefore not gofmt-clean on the
marker lines.

## Running

    go test ./internal/cli -run 'TestApps/<app>/'          # one application
    go test ./internal/cli -run 'TestApps/<app>/<name>$'   # one query
    go test ./internal/cli -run TestApps -update           # rewrite results

## Suspected bugs

The result sections always record what errtrace prints today, so the suite
passes. When that output is believed to be wrong, the query's comment starts
with `SUSPECTED BUG:` and describes the correct result:

    grep -rl 'SUSPECTED BUG' internal/cli/testdata/apps

The confirmed findings are collected in [FINDINGS.md](FINDINGS.md).
