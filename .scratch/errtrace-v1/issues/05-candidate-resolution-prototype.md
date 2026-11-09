# Compare candidate resolution strategies on real code

Type: prototype
Status: open
Blocked by: 02
Map: [errtrace v1 map](../map.md)

## Question

The call-graph research ruled out RTA ("created implementers"): it was barely better than CHA. Compare its three VTA variants on a real medium-size module with interface-heavy call sites (e.g. `io.Reader.Read`, `http.Handler`, a callback API), looking at Candidate sets, runtime, and how much code each needs: (1) VTA the way govulncheck runs it (CHA → reachable slice → VTA twice); (2) VTA seeded with RTA's graph; (3) a forked VTA exposing the type sets at error return values. Which variant does v1 use?
