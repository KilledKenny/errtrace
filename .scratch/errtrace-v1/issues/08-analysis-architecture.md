# Analysis architecture: summaries, recursion, caching

Type: grilling
Status: open
Blocked by: 02, 04, 05, 09
Map: [errtrace v1 map](../map.md)

## Question

How is the analysis structured end to end? Per-function error summaries computed bottom-up over call-graph SCCs (how recursion and cycles are handled), how interface and function-value Candidates are resolved recursively inside summaries, how call-site Queries narrow using local context, where Unresolved entries come from, and what is cached (and how it's invalidated) to meet the ~10s cold / near-instant warm target. It must also cover errtrace's own value tracking for sentinels and `%w` chains (VTA tracks only types), how per-caller precision is recovered (VTA merges all callers of a function), and whether to pin x/tools for Go 1.24 or move to Go 1.26.
