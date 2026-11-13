# Glossary

**Query**: A single request to errtrace: "what errors can this Target produce?" Contrast with *linting*, which scans a whole codebase for problems; errtrace is a query tool, not a linter.

**Target**: The thing a Query points at: either a function/method declaration, or a specific call site.

**Error Set**: The full answer to a Query: every Error Origin that can reach the Target's error result, each with its Propagation Path.

**Error Origin**: The point where an error value is first created. One of:
- **Sentinel**: a package-level error variable returned by identity (`io.EOF`).
- **Concrete Type**: a value of a named type implementing `error` (`*fs.PathError`).
- **Opaque Error**: an error with no identity or inspectable type (`errors.New`, `fmt.Errorf` without `%w`).

**Wrap**: Creating a new error that keeps an inner error reachable via `errors.Is`/`errors.As` (e.g. `fmt.Errorf("...: %w", err)`). A Wrap is not an Error Origin; it adds a link to a **Wrap Chain** ending at an Origin.

**Propagation Path**: The chain of calls an error travels through, from its Error Origin up to the Target.

**Unresolved**: An explicit entry in an Error Set marking a place the analysis could not see through (reflection, cgo, no Candidate found). Shown rather than silently dropped, so the Error Set never claims more completeness than it has.

**Candidate**: A concrete function that may run where the code calls through an interface method (or other dynamic call). Resolving a dynamic call means finding its Candidates.

**Membership Query**: A Query that asks whether one given error (a Sentinel or a Concrete Type) is in the Target's Error Set, instead of asking for the whole set (`-is io.EOF`, `-as '*io/fs.PathError'`).

**Verdict**: The answer of a Membership Query: yes (an entry of the Error Set is that error, with its Propagation Path as witness), no, or maybe (the error is only in possibly handled entries, or it could come through an Unresolved entry).

**Mark**: The upward pass's fact "this value or memory location may hold X", where X is the error of a Membership Query. Marks are pushed forward from every place that produces X; they are an upper bound: what carries no Mark cannot hold X.

**Carrier**: An Unresolved entry whose place carries a Mark, so that X could come through it. A maybe Verdict lists only the Carriers.
