# stdlibio

A small file / CSV / JSON-lines import tool. `stdlibio <config> <dir>` reads a
JSON configuration file, validates the scalars in it (port, row limit, strict
flag, "since" date, upload endpoint), walks the input directory, imports every
data file (magic check, line scan, CSV or JSON-lines rows, re-encode, copy) and
finally runs a set of background "diagnostics".

The theme of this application is **following errors into the standard
library** (strconv, os, io, bufio, encoding/csv, encoding/json, time, net/url,
context, path/filepath, sync) and the **documented out-of-scope constructs**
(channels, fields, maps, slices, run-time globals, panic/recover, reflection,
goroutines, atomics, unsafe, `any` round trips, funcs in context values).
Stdlib spans are normalised to `$GOROOT/src/<file>:*`; the *sets* still depend
on the toolchain's stdlib (recorded with the repository's Go 1.27 toolchain:
`strconv` forwards to `internal/strconv`, `encoding/json` is the v2 engine).

## Packages

| Package | Content |
|---|---|
| `.` (`main.go`) | `main`, `run`, `settings`, `importAll`, `importFile`, `diagnostics`; reaches every package below |
| `config` | `Port`/`Explain` (strconv.Atoi), `Limit` (ParseInt + errors.Is ErrRange), `Strict` (ParseBool), `Since` (time.Parse + errors.As), `Endpoint` (url.Parse), `Decode` (json.Unmarshal + errors.As), `ReadRaw`/`Load` (os.ReadFile + os.IsNotExist) |
| `source` | `Exists`/`Missing`/`Require`/`Optional` (os.Stat, fs.ErrNotExist guards, os.ErrNotExist alias), `Cleanup` (os.Remove), `Peek` (os.Open, defer Close), `Save`/`Durability` (errors.Join / %w with a nil-only operand), `Slurp` (named result + deferred Close), `Magic`/`Header` (io.ReadFull / ReadAtLeast), `Lines` (bufio.Scanner), `FirstLine`/`CountLines` (bufio.Reader, io.EOF loop) |
| `source/flush_default.go`, `flush_durable.go` | build-tag variants of `flush` (`-tags durable`) |
| `source/legacy.go` | deliberately broken, only built with `-tags legacy` (load-error query) |
| `source/source_test.go` | a test helper (tests are not loaded) |
| `stream` | local `io.Reader`/`io.Writer` implementations (`Limited`, `Chunks`, `Sink`) and `Pump` = `io.Copy` |
| `records` | `ReadCSV` (encoding/csv, custom `*RowError` wrapper), `ReadJSONLines` (json.Decoder, io.EOF), `Encode` |
| `walk` | `Find`/`Has` (filepath.WalkDir callbacks, fs.SkipDir/SkipAll), `Root` (sync.Once + run-time global), `Pattern`/`Include` (sync.OnceValues) |
| `jobs` | context: `Check` (ctx.Err), `Why` (context.Cause), `Wait`, `Stop` (context sentinels), `Notify` (func stored in a context value), `Run` |
| `murky` | the out-of-scope constructs, one function each |
| `tools/` | a nested module (`example.com/stdlibio-tools`), i.e. a Target outside the main module |

`go build ./... && go vet ./...` are clean (also with `-tags durable`); only
`-tags legacy` fails to type-check, on purpose.

## Queries

"positive" = origins that must be in the Error Set; "negative" = origins that
must be absent, or a Target / flag that must be rejected. The expectation of
each query is in the leading comment of its archive.

| Query | Args | Checks | Kind | Suspected bug |
|---|---|---|---|---|
| `atoi_package_qualifier` | `config/config.go:@atoi` | Negative: the position is on the package qualifier "strconv" of strconv.Atoi(s), not on the callee name or the "(" | negative (rejected, exit 2) | no |
| `check_ctx_err_call_site` | `jobs/jobs.go:@ctxerr` | Call site of the interface method ctx.Err() | positive | no |
| `cleanup_remove_isnotexist` | `source/source.go:@cleanup` | Cleanup returns the os.Remove error unless os.IsNotExist(err) | positive | no |
| `close_in_deferred_closure` | `source/source.go:@close` | Call-site Target f.Close() inside the deferred closure of Slurp; the enclosing function is the closure (Slurp$1) | positive | no |
| `countlines_eof_loop` | `source/source.go:@countlines` | The io.EOF loop idiom: "if err == io.EOF { break }" then "return n, nil"; other errors are wrapped with "source: line %d: %w" | positive + negative | no |
| `csv_read_call_site` | `records/records.go:@csvread` | Call site of (*csv.Reader).Read before the guards: csv.ErrFieldCount (wrapped in *csv.ParseError) must now be present, together with ErrBareQuote, ErrQuote and errInva... | positive | no |
| `decode_as_syntaxerror` | `config/config.go:@decode` | Decode keeps the json.Unmarshal error (wrapped with %w) only when errors.As finds a *json.SyntaxError, and otherwise replaces it by errors.New "config: malformed docum... | positive + negative | no |
| `describe_no_error_result` | `config/config.go:@describe` | Describe returns only a string | negative (rejected, exit 2) | no |
| `diagnostics_call_site_ungrouped` | `-fanout 100 main.go:@diagcall` | Call site diagnostics() in run with -fanout 100: the same 13 leaves as diagnostics_fanout_group, but ungrouped, each with its full path through the dynamic call "check... | positive | no |
| `diagnostics_fanout_group` | `main.go:@diagnostics` | diagnostics calls 12 function values from a slice in a loop | positive | no |
| `durability_errorf_nil_operand` | `source/source.go:@durability` | Durability returns fmt.Errorf("source: flush %s: %w", f.Name(), flush(f)); in the default build flush only returns nil. fmt.Errorf never returns nil, so the function a... | positive | **yes** |
| `endpoint_url_parse` | `config/config.go:@endpoint` | Endpoint returns the sentinel config.ErrNoEndpoint unwrapped, or the url.Parse error under fmt.Errorf "config: endpoint: %w". url.Parse wraps everything in *url.Error,... | positive | no |
| `errors_new_call_site` | `main.go:@usage` | Call-site Target on errors.New itself (an intrinsic, not followed into the stdlib): exactly 1 entry, opaque errors.New "usage: stdlibio <config> <dir>", whose span is ... | positive | no |
| `exists_is_notexist_guard` | `-fanout 0 source/source.go:@exists` | Exists returns nil when errors.Is(err, fs.ErrNotExist) and otherwise wraps the os.Stat error with %w | positive + negative | no |
| `explain_type_switch_field` | `config/config.go:@explain` | Explain type-switches on the strconv.Atoi error: for *strconv.NumError it returns the field e.Err, the default branch returns err | positive + negative | no |
| `find_callback_literal` | `walk/walk.go:@findfn` | Declaration Target on the "func" keyword of the function literal passed to WalkDir (walk.Find$1) | positive | no |
| `find_walkdir_callback` | `walk/walk.go:@find` | Find returns filepath.WalkDir's result; the callback returns its err argument, fs.SkipDir or walk.ErrTooMany | positive + negative | no |
| `firstline_readstring_call_site` | `source/source.go:@readstring` | Call site of (*bufio.Reader).ReadString on a reader over strings.Reader | positive | no |
| `flush_call_site_durable` | `-tags durable source/source.go:@flushcall` | Call site flush(f) inside the errors.Join arguments, with -tags durable selecting flush_durable.go: the (*os.File).Sync origins (os.ErrInvalid for a nil file, poll and... | positive | no |
| `flush_default_never_fails` | `source/flush_default.go:@flush` | The default-build flush only returns nil: the header with "0 entries" and nothing else, exit 0. (Reachable from main through source.Save, so no warning.) | positive + negative | no |
| `flush_durable_without_tag` | `source/flush_durable.go:@flush` | Negative: the Target file is excluded by its //go:build durable constraint and no -tags is given | negative (rejected, exit 2) | no |
| `has_walkdir_skipall` | `walk/walk.go:@has` | Has passes WalkDir a callback that only returns fs.SkipAll or nil | positive + negative | **yes** |
| `header_readatleast_eof_filtered` | `source/source.go:@header` | Header calls io.ReadAtLeast on a local *strings.Reader and returns nil for err == io.EOF. Expected exactly 2 entries: io.ErrUnexpectedEOF and io.ErrShortBuffer ((*stri... | positive + negative | **yes** |
| `include_oncevalues` | `walk/walk.go:@patterncall` | Call site of the function value returned by sync.OnceValues | positive | no |
| `inner_read_interface_call_site` | `stream/stream.go:@innerread` | Call-site Target on the interface method call l.R.Read(p) | positive + negative | no |
| `json_flag_rejected` | `-json main.go:@run` | Negative: -json is not implemented yet (plan S11) | negative (rejected, exit 2) | no |
| `legacy_tag_type_error` | `-tags legacy source/source.go:@require` | Negative: -tags legacy pulls in source/legacy.go, which deliberately does not type-check (undefined: decodeLegacy) | negative (rejected, exit 2) | no |
| `limit_errrange_guard` | `config/config.go:@limit` | Limit returns nil when errors.Is(err, strconv.ErrRange) holds, otherwise the ParseInt error unchanged. strconv.ErrRange (wrapped in *strconv.NumError) must NOT be in t... | positive + negative | no |
| `limited_read_method` | `stream/stream.go:@limread` | Declaration Target on a method that implements io.Reader | positive + negative | no |
| `lines_scanner_err` | `source/source.go:@lines` | Lines returns bufio.Scanner.Err(), which loads the scanner's err field (and hides io.EOF) | positive | no |
| `magic_call_site_strings_reader` | `main.go:@magicmem` | call site source.Magic(strings.NewReader(doc)) in importFile: the argument's type set is exactly *strings.Reader, whose Read returns only io.EOF. Expected exactly 3 en... | positive + negative | **yes** |
| `magic_decl_readfull_switch` | `source/source.go:@magic` | Magic maps io.EOF -> ErrEmpty and io.ErrUnexpectedEOF -> ErrBadMagic with a value switch on the io.ReadFull result and returns the rest | positive + negative | no |
| `main_run_call_site` | `main.go:@runcall` | The one deliberately large query: call site run(os.Args[1:]) in main, i.e. everything the program can report | positive + negative | no |
| `method_value_not_a_call` | `main.go:@methodvalue` | Negative: the position is on the method value stage.Err inside the slice literal; it is neither a declaration nor a call | negative (rejected, exit 2) | no |
| `missing_returns_guarded` | `-fanout 0 source/source.go:@missing` | The complement of Exists | positive | no |
| `murky_any_across_call` | `murky/murky.go:@boxed` | Like murky_any_round_trip, but the boxed value (fmt.Errorf "murky: boxed: %w" around ErrWorker) crosses box(v any) any before the comma-ok assertion | positive | no |
| `murky_any_round_trip` | `murky/murky.go:@roundtrip` | Empty-interface round trip in one function: var boxed any = ErrPlugin; return boxed.(error) | positive | no |
| `murky_atomic_value` | `murky/murky.go:@health` | Out of scope by design: the error is loaded from a sync/atomic.Value as *error and dereferenced | positive | no |
| `murky_channel_receive` | `murky/murky.go:@fromchannel` | Out of scope by design: the error is received from a channel (a goroutine sends murky.ErrWorker) | positive | no |
| `murky_errgroup_wait` | `murky/murky.go:@parallel` | Errors from goroutines through an errgroup-like helper | positive | no |
| `murky_map_lookup` | `murky/murky.go:@frommap` | Out of scope by design: the error is looked up in a local map literal | positive | no |
| `murky_recover_to_error` | `murky/murky.go:@guarded` | Guarded converts a panic into its named result inside a deferred closure: err = r.(error) or err = fmt.Errorf("murky: panic: %v", r) | positive | **yes** |
| `murky_reflect_call` | `murky/murky.go:@reflective` | Out of scope by design: plugin is invoked through reflect.Value.Call and the result asserted back to error | positive | no |
| `murky_runtime_global` | `murky/murky.go:@last` | Last returns the package-level variable lastErr, which has no initialiser and is assigned at run time by Remember(err) (called from main with importFile's error) | positive | **yes** |
| `murky_slice_element` | `murky/murky.go:@fromslice` | Out of scope by design: field loads appended to a slice, then errs[0] is returned (or nil) | positive | no |
| `murky_struct_field` | `murky/murky.go:@stageerr` | Out of scope by design | positive | no |
| `murky_unsafe_pointer` | `murky/murky.go:@reinterpret` | The error is read back through unsafe.Pointer(&err) | positive | no |
| `nested_module_target` | `tools/gen/main.go:@generate` | Target outside the main module: tools/ is a nested module (example.com/stdlibio-tools) that ./... does not match | positive | no |
| `nonexistent_file` | `nosuch/file.go:3:1` | (minor, misleading message) the Target file does not exist | negative (rejected, exit 2) | **yes** |
| `notify_hook_from_context_value` | `jobs/jobs.go:@notify` | Notify calls a func stored in a context value: ctx.Value(hookKey{}).(Hook) | positive | no |
| `open_call_site` | `source/source.go:@open` | Call site directly on os.Open: everything it returns is &PathError{Op: "open", Err: e} where e is assigned inside the closure passed to ignoringEINTR. Expected: entrie... | positive | no |
| `optional_alias_guard` | `-fanout 0 source/source.go:@optional` | Optional drops Require's error when errors.Is(err, fs.ErrNotExist) | positive + negative | no |
| `panic_builtin_call` | `murky/murky.go:@panic` | Negative: call-site position on the builtin panic(ErrStage) | negative (rejected, exit 2) | no |
| `parsebool_call_site` | `config/config.go:@parsebool` | Call-site Target directly on a stdlib call | positive | no |
| `peek_open_magic` | `source/source.go:@peek` | Peek returns the os.Open error unwrapped, or Magic(f)'s result; the deferred f.Close() result is discarded and must contribute nothing (no os.ErrClosed from Close) | positive + negative | no |
| `port_chain_depth_1` | `-chain-depth 1 config/config.go:@port` | Same Target as port_wrap_numerror with -chain-depth 1: the chains are two links deep (NumError.Unwrap, fmt.Errorf), so every entry must be marked [chain truncated]; th... | positive | no |
| `port_wrap_numerror` | `config/config.go:@port` | Port wraps the result of strconv.Atoi with %w. strconv.ErrSyntax and strconv.ErrRange must both be present with the two-link chain *strconv.NumError.Unwrap -> fmt.Erro... | positive | no |
| `pump_io_copy` | `stream/stream.go:@pump` | Pump is io.Copy(dst, src) with parameters supplied by two callers | positive + negative | no |
| `readcsv_fieldcount_tolerated` | `records/records.go:@readcsv` | ReadCSV breaks on io.EOF, skips rows on errors.Is(err, csv.ErrFieldCount) and wraps the rest in *RowError (custom Unwrap) | positive + negative | no |
| `readfull_call_site` | `source/source.go:@readfull` | Call site of io.ReadFull(r, buf[:]) before Magic's switch: io.EOF, io.ErrUnexpectedEOF and io.ErrShortBuffer must all be present (the ReadAtLeast oracle), together wit... | positive | no |
| `readjson_decoder_eof` | `records/records.go:@readjson` | ReadJSONLines returns nil on err == io.EOF and wraps everything else with "records: json: %w" | positive + negative | no |
| `readraw_isnotexist_helper` | `config/config.go:@readraw` | ReadRaw swallows the os.ReadFile error when the helper guard os.IsNotExist(err) holds and returns it unwrapped otherwise | positive | no |
| `remove_call_site_fanout0` | `-fanout 0 source/source.go:@remove` | Call site of os.Remove with -fanout 0: the same origins as Cleanup but ungrouped (no "group of N entries" header although os.ignoringEINTR has many Candidates) and wit... | positive | no |
| `require_os_errnotexist` | `source/source.go:@require` | Require passes Exists' errors through and adds "return os.ErrNotExist". os.ErrNotExist is an alias variable for fs.ErrNotExist: one sentinel entry for it (named os.Err... | positive | no |
| `root_once_runtime_global` | `walk/walk.go:@root` | Root returns the package variable rootErr, which is nil at init and assigned at run time inside a sync.Once callback (ErrNoRoot, or filepath.Abs's error) | positive | **yes** |
| `run_step_call_site` | `jobs/jobs.go:@stepcall` | Call site of the parameter function step(ctx) in Run; its only Candidate is main's closure, which returns importAll's errors | positive + negative | no |
| `save_join_durable` | `-tags durable source/source.go:@save` | same Target with -tags durable (flush is the f.Sync variant) | positive | **yes** |
| `save_join_nil_operand` | `source/source.go:@save` | Save returns errors.Join(werr, flush(f), f.Close()) | positive | **yes** |
| `since_as_parseerror` | `config/config.go:@since` | time.Parse only ever returns *time.ParseError | positive + negative | no |
| `slurp_named_result_defer_close` | `source/source.go:@slurp` | Slurp has a named result err that a deferred closure overwrites with fmt.Errorf("source: close: %w", cerr) | positive | **yes** |
| `sprintf_call_no_error_result` | `config/config.go:@sprintf` | Negative: call-site Target on fmt.Sprintf, a stdlib call without error result | negative (rejected, exit 2) | no |
| `stop_context_sentinels` | `jobs/jobs.go:@stop` | Stop returns the stdlib sentinels directly from a tagless switch | positive | no |
| `strict_pattern_config_only` | `-p ./config config/config.go:@strict` | -p ./config loads only the config package, so there is no main root: the "not reachable from main/init" warning is expected on stderr, exit 0. The set is the same as p... | positive | no |
| `test_file_target` | `source/source_test.go:@failing` | Negative: the Target is a helper in a _test.go file | negative (rejected, exit 2) | no |
| `transfer_pump_call_site` | `stream/stream.go:@pumpcall` | call site Pump(sink, src) in Transfer, where dst is a *Sink and src a *Limited over *Chunks | positive + negative | **yes** |
| `wait_deadline_guard` | `jobs/jobs.go:@wait` | Wait wraps ctx.Err() with "jobs: wait: %w" unless errors.Is(err, context.DeadlineExceeded). ctx.Err() is an Unresolved load inside package context, which no guard can ... | positive + negative | no |
| `why_context_cause` | `jobs/jobs.go:@why` | Why returns context.Cause(ctx) | positive | no |

## Suspected bugs found

Listed most severe first; details and the expected result are in each query.

1. **A nil-only operand erases a multi-operand wrap** (`save_join_nil_operand`,
   `durability_errorf_nil_operand`): `errors.Join(a, f(), c)` where `f` can only
   return nil yields none of `a`'s or `c`'s origins, and
   `fmt.Errorf("...%w", f())` yields "0 entries" although fmt.Errorf never
   returns nil. Looks like operand sets are combined as a product.
2. **errors.Join of three stdlib-sized operands hits the 1024-value cap**
   (`save_join_durable`): `unresolved(fixpoint-limit)` and apparently missing
   combinations; same root cause as 1 (product instead of union).
3. **Named result captured by a deferred closure** (`slurp_named_result_defer_close`,
   `murky_recover_to_error`): every origin and wrap of the function is replaced
   by `unresolved(memory): load of escaping variable err`, and one such entry has
   the span `-` instead of a position (plan step S08/S10 not implemented yet).
4. **Run-time assigned package variables are reported as plain sentinels**
   (`root_once_runtime_global`, `murky_runtime_global`): no Unresolved entry and
   no "mutable global" mark (plan S10).
5. **Candidates are not narrowed by the arguments at the call**
   (`header_readatleast_eof_filtered`, `magic_call_site_strings_reader`,
   `transfer_pump_call_site`, `has_walkdir_skipall`): errors of another caller's
   reader / writer / callback show up (plan P08 "narrowing at substitution", S09).
6. **Misleading message for a missing file** (`nonexistent_file`).

Known, accepted imprecision (not flagged): with the Go 1.27 stdlib,
`strconv.toError` switches over `internal/strconv` constants, so every strconv
query also lists bare `internal/strconv.Error` values and e.g. `ErrRange` for
`ParseBool`; grouped listings (`-fanout`) do not show wrap chains or
"possibly handled" details, which is why some queries use `-fanout 0`.

## Running

    go test ./internal/cli -run 'TestApps/stdlibio/'
