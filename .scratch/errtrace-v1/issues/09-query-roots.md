# Where whole-program analysis starts for a Query

Type: grilling
Status: open
Blocked by:
Map: [errtrace v1 map](../map.md)

## Question

VTA only gives correct Candidates when it runs on the whole program from its roots. Rooting it at the queried function silently resolves parameters to 0 Candidates (see "Call-graph algorithms in golang.org/x/tools for resolving Candidates"). So what are the roots for a Query? Options: `main`/`init` of chosen commands; all exported functions of a library (treating their parameters as "any implementer"); test mains; or a user-supplied entry point. And how does a Query on a library with no `main` report a parameter whose Candidates depend on the caller: as Unresolved, as a "depends on the caller" entry, or as a fallback to all implementers just for that parameter?
