# targets

A small multi-binary module built to exercise errtrace's **CLI surface, Target
resolution and package loading** rather than the analysis itself. The code is
deliberately simple (sentinels, one concrete error type, a couple of `%w`
wraps, one interface with two implementations) so that every Error Set is
small and easy to predict; the variety is in *where the Target points* and
*how the command is invoked*.

It models a record store with two commands: `ingest` reads records from a
disk store and normalises them, `report` prints totals from an in-memory
snapshot.

## Packages

| Path | What it is for |
| --- | --- |
| `cmd/ingest` | `package main`, uses the disk Backend. Holds most call-site shapes: selector X/Sel/paren, chained calls, `defer`/`go`, function-typed variable, bound method value, immediately invoked literal, conversion, builtin, generic call with an explicit type argument, call as argument of a call, `init()`. |
| `cmd/report` | Second `package main` (also has a `run`), uses the mem Backend and the build-tagged `platform` package; generic call with inferred type argument. |
| `internal/store` | `Store` (embedded `base`, pointer and value receivers), the `Backend` interface, `mem` implementation, `Batch` (error kept in a struct field). In-package and external `_test.go` files. |
| `internal/store/disk` | Nested package: `Disk` Backend, concrete error type `*DiskError`, a function whose result type is the concrete type. |
| `internal/codec` | Generic `Decode[T]`, a function literal held in a package var (`Trim`), a CRLF file (`crlf.go`), a generated file with a `//line` directive (`gen.go`), identifiers with multi-byte runes and a line with tabs and multi-byte comment text before the call (`unicode.go`). |
| `internal/platform` | Build tags: `limits_default.go` (`!fast`), `limits_fast.go` (`fast`), `audit.go` (`fast && audit`). `platform.go` has no markers and is addressed with literal positions (it is 8 lines / 180 bytes; `Probe` is at 5:6 = offset 128). |
| `_broken/typeerr`, `_broken/syntaxerr` | Deliberately broken packages. The leading underscore keeps them out of `./...`, `go build ./...` and `go vet ./...`; queries reach them through `-p` or by pointing the Target at them. |
| `tools/lintcfg` | A nested module (own `go.mod`): a Go file inside the tree but outside `example.com/targets`. |
| `assets/schema.txt` | A non-Go file. |

Marker lines are not gofmt-clean on purpose. Do not reformat `crlf.go`
(CRLF line endings) or `platform.go` (queries use literal offsets).

## Parity sets

Each query is its own archive, so "same query phrased differently gives
identical output" cannot be asserted by the harness. These groups were
compared by hand (identical stdout sections) when the results were recorded,
and must be re-compared after an `-update`:

- Probe declaration: `pos_line_col`, `pos_offset`, `pos_range`,
  `pos_line_col_offset`, `pos_dot_slash`, `pos_dotdot`, `pos_line_only`,
  `pos_range_short_end`, `pos_offset_range`, `pos_line_range`,
  `pos_leading_zeros`, `cli_double_dash`, `cli_debug_summary`, `p_empty_list`,
  `p_type_error_excluded`, `p_import_path`, `tags_empty`
- `(*Store).Get` declaration: `decl_name`, `decl_func_keyword`
- `s.Get(key)` call: `call_selector_sel`, `call_lparen`, `cli_debug_target`
- `fetch`: `decl_main_pkg_ingest`, `chain_depth_exact`, `chain_depth_large`,
  `p_two_patterns`
- `s.be.Read`: `call_interface_method`, `fanout_above_boundary`, `fanout_zero`
- `Decode`: `decl_generic`, `cli_debug_target_generic`
- `Größe`: `unicode_decl`, `unicode_decl_mid_rune`
- `-tags fast`: `tags_change_callee`, `tags_unknown`
- usage text: `cli_help_h`, `cli_help_long`, `cli_help_after_bad_flag_value`

## Suspected bugs

| Queries | Summary |
| --- | --- |
| `call_go`, `call_main_run`, `decl_run_ingest` (reference: `decl_warm_paths`) | `[+N more paths]` does not count paths that merge inside a callee: a call-site Target shows no marker where the declaration Target of the same callee shows one, and a declaration shows +1 where there are two more paths. |
| `p_wildcard_matches_nothing`, `p_malformed` | A `-p` wildcard pattern that matches no package is silently dropped (go list's "matched no packages" warning is swallowed); the query runs on the target file's package alone and exits 0. |
| `pos_column_out_of_range` | On the last line of a newline-terminated file one extra column is accepted (8:3 on a 1-byte line) and echoed back as a position. |
| `file_nested_module` | A file of a nested module is analysed as the ad-hoc package `command-line-arguments` (low confidence). |
| `file_nonexistent` | A missing file (also a directory, a non-Go file) gets the generic "file is not in any loaded package" (low confidence, message only). |
| `p_no_go_files` | go list errors are printed with a stray `-: ` position placeholder (low confidence, cosmetic). |

## Not covered

- An absolute path to an *existing* file, and an existing file outside the
  module tree: the harness runs each query in a fresh temporary directory and
  args cannot refer to it (`file_abs_nonexistent`, `file_parent_outside` and
  `file_nested_module` are the closest).
- Arguments containing spaces or an empty-string argument (args are split on
  white space).
- `-debug=timing` (nondeterministic output).

## Queries

"negative" means the query is expected to fail (exit 2) or to be rejected.

| Query | Args | What it checks | Kind | Suspected bug |
| --- | --- | --- | --- | --- |
| body_return_keyword | `internal/store/store.go:@get_return` | Position on a "return" keyword whose operands contain a call (fmt.Errorf). | negative | no |
| body_statement | `internal/store/store.go:@get_stmt` | Position inside the body of Get on "s.hits++", which is not a call. | negative | no |
| body_string_literal | `internal/store/store.go:@get_string` | Position on the format string argument inside fmt.Errorf(...). | negative | no |
| call_argument_ident | `cmd/ingest/main.go:@arg_ident` | Position on the plain argument "key" of s.Get(key). | negative | no |
| call_bound_method_var | `cmd/ingest/main.go:@bound` | Call-site target ping(), a local function-typed variable holding the method value s.Ping. | positive | no |
| call_builtin | `cmd/ingest/main.go:@builtin` | Position on the builtin call len(args). | negative | no |
| call_chain_first | `cmd/ingest/main.go:@chain_new` | Chained call: position on NewBatch (first link, returns *Batch). | negative | no |
| call_chain_last | `cmd/ingest/main.go:@chain_commit` | Chained call: position on Commit (last link, returns error). | positive | no |
| call_chain_middle | `cmd/ingest/main.go:@chain_add` | Chained call: position on Add (middle link, returns *Batch). | negative | no |
| call_chain_pkg_qualifier | `cmd/ingest/main.go:@chain_pkg` | Chained call store.NewBatch(s).Add(...).Commit(): position on the package qualifier "store". | negative | no |
| call_concrete_error_type | `internal/store/disk/disk.go:@validate_call` | Call-site target: Validate(root) inside Open, result type *DiskError. | positive | no |
| call_conversion | `cmd/ingest/main.go:@conv` | Position on the conversion kind(len(raw)): T(x) is not a call. | negative | no |
| call_defer | `cmd/ingest/main.go:@defer_call` | Call-site target in a defer statement: defer s.Close(). | positive | no |
| call_embedded_field_method | `internal/store/store.go:@embedded` | Call-site target s.Check() inside Get: method reached through the embedded field base. | positive | no |
| call_func_param_in_generic | `internal/codec/codec.go:@parse_call` | Call-site target parse(raw): call to a function-typed parameter inside the generic Decode. | positive | no |
| call_func_var | `cmd/ingest/main.go:@fnvar` | Call-site target hook(key): call to a package-level function-typed variable. | positive | no |
| call_generic_bracket | `cmd/ingest/main.go:@decode_bracket` | Position on the "[" of codec.Decode[string](...). | negative | no |
| call_generic_explicit | `cmd/ingest/main.go:@decode_call` | Call-site target on Decode in the explicit instantiation codec.Decode[string](raw, toText). | positive | no |
| call_generic_inferred | `cmd/report/main.go:@decode_inferred` | Call-site target codec.Decode(raw, codec.ParseInt) with an inferred type argument, in cmd/report. | positive | no |
| call_generic_type_arg | `cmd/ingest/main.go:@decode_targ` | Position on the type argument "string" of codec.Decode[string](...). | negative | no |
| call_go | `cmd/ingest/main.go:@go_call` | Call-site target in a go statement: go warm(s). | positive | yes |
| call_in_init | `cmd/ingest/main.go:@init_call` | Call-site target inside init(): codec.Register("text"), marker on the package qualifier "codec". | negative | no |
| call_in_init_sel | `cmd/ingest/main.go:@init_sel` | Call-site target inside init(): codec.Register("text"), marker on the Sel "Register" of the package-qualified call (init itself has no error result, the call does). | positive | no |
| call_inner_arg_call | `cmd/ingest/main.go:@inner` | Position on "s" of the inner argument call s.Ping() (selector X). | negative | no |
| call_inner_arg_sel | `cmd/ingest/main.go:@inner_sel` | Position on the Sel "Ping" of the inner call in annotate(key, s.Ping()): the innermost call wins. | positive | no |
| call_interface_method | `internal/store/store.go:@iface_call` | Call-site target s.be.Read(key): interface method call with 2 Candidates (default -fanout=8, so not grouped). | positive | no |
| call_lparen | `cmd/ingest/main.go:@lparen` | Call-site target on the opening paren of s.Get(key). | positive | no |
| call_main_run | `cmd/ingest/main.go:@main_call` | Call-site target run(os.Args[1:]) inside func main (main has no error result, the call does); plain identifier callee in package main. | positive | yes |
| call_no_error_result | `cmd/ingest/main.go:@hits_call` | Call-site target s.Hits(), which returns int. | negative | no |
| call_outer_with_call_arg | `cmd/ingest/main.go:@outer` | Call-site target annotate(key, s.Ping()): outer call whose argument is itself a call. | positive | no |
| call_pkg_func_var | `cmd/report/main.go:@trim_call` | Call-site target codec.Trim(raw) in cmd/report: the Sel of a package-qualified function-typed VARIABLE (not a func declaration). | positive | no |
| call_probe_default | `cmd/report/main.go:@probe_call` | Call-site platform.Probe() in cmd/report with default tags; marker on Sel of a package-qualified call. | positive | no |
| call_promoted_method | `cmd/ingest/main.go:@promoted` | Call-site target s.Check(): method promoted from the embedded base. | positive | no |
| call_report_get_all_binaries | `cmd/report/main.go:@get_call` | Call-site s.Get("total") in cmd/report with the default ./... (both binaries loaded). | positive | no |
| call_rparen | `cmd/ingest/main.go:@rparen` | Position on the closing paren of s.Get(key). | negative | no |
| call_selector_sel | `cmd/ingest/main.go:@sel_sel` | Call-site target on the selector Sel "Get" of s.Get(key) in fetch. | positive | no |
| call_selector_x | `cmd/ingest/main.go:@sel_x` | Position on the receiver expression "s" (selector X) of s.Get(key). | negative | no |
| chain_depth_exact | `-chain-depth=2 cmd/ingest/main.go:@fetch` | -chain-depth equal to the longest chain (2). | positive | no |
| chain_depth_large | `-chain-depth=1000000 cmd/ingest/main.go:@fetch` | Very large -chain-depth (far above the longest chain). | positive | no |
| chain_depth_negative | `-chain-depth=-1 cmd/ingest/main.go:@fetch` | Negative -chain-depth. | negative | no |
| chain_depth_not_a_number | `-chain-depth=deep cmd/ingest/main.go:@fetch` | Non-numeric -chain-depth. | negative | no |
| chain_depth_one | `-chain-depth=1 cmd/ingest/main.go:@fetch` | -chain-depth=1 at the lower boundary on fetch, whose longest Wrap Chain has 2 links. | positive | no |
| chain_depth_zero | `-chain-depth=0 cmd/ingest/main.go:@fetch` | Invalid -chain-depth below the minimum. | negative | no |
| cli_bool_style_flag | `-tags internal/platform/platform.go:5:6` | -tags swallows the target as its value, leaving no positional argument. | negative | no |
| cli_dash_h_after_terminator | `-- -h` | After "--" a -h is a positional argument, i.e. the Target file name. | negative | no |
| cli_debug_both | `-debug=summary,target internal/store/store.go:@ping` | Both safe debug modes, in reverse order of how they print. | positive | no |
| cli_debug_invalid | `-debug=targets internal/platform/platform.go:5:6` | Invalid debug mode (plural). | negative | no |
| cli_debug_summary | `-debug=summary internal/platform/platform.go:5:6` | Hidden -debug=summary. | positive | no |
| cli_debug_target | `-debug=target cmd/ingest/main.go:@sel_sel` | Hidden -debug=target on a call-site target. | positive | no |
| cli_debug_target_generic | `-debug target internal/codec/codec.go:@decode` | Hidden -debug target on the generic declaration. | positive | no |
| cli_debug_trailing_comma | `-debug=target, internal/platform/platform.go:5:6` | Debug list with a trailing comma, i.e. an empty mode (unlike -p and -tags, empty elements are not skipped). | negative | no |
| cli_double_dash | `-fanout=2 -- internal/platform/platform.go:5:6` | The "--" terminator before the target. | positive | no |
| cli_flag_after_target | `internal/platform/platform.go:5:6 -fanout=2` | A flag after the target: Go flag parsing stops at the first positional argument, so it counts as a second target. | negative | no |
| cli_help_after_bad_flag_value | `-chain-depth=0 -help` | An invalid flag value followed by -help: help is detected during parsing, before value validation. | positive | no |
| cli_help_h | `-h` | -h prints usage on stdout and exits 0; hidden -debug flag is not listed. | positive | no |
| cli_help_long | `--help internal/platform/platform.go:5:6` | --help together with a valid target: help wins, usage on stdout, exit 0, no query runs. | positive | no |
| cli_no_args | `(none)` | No arguments at all. | negative | no |
| cli_two_targets | `internal/platform/platform.go:5:6 internal/store/store.go:@ping` | Two targets. | negative | no |
| cli_unknown_flag | `-json internal/platform/platform.go:5:6` | Unknown flag. | negative | no |
| closure_literal_call_paren | `cmd/ingest/main.go:@iife_paren` | Position on the call paren of the immediately invoked literal. | positive | no |
| closure_literal_func_keyword | `cmd/ingest/main.go:@iife_kw` | Position on the "func" keyword of an immediately invoked literal func() error {...}(). | positive | no |
| crlf_call_func_var | `internal/codec/crlf.go:@trim_call` | Call-site target in the CRLF file: Trim(raw), a call through a package-level function variable. | positive | no |
| crlf_decl | `internal/codec/crlf.go:@checksum` | Declaration target in a file with CRLF line endings: codec.Checksum calls the func-valued var Trim. | positive | no |
| crlf_line_end | `internal/codec/crlf.go:1:15` | CRLF file, line 1 is "package codec\r\n": column 14 is the \r, column 15 the \n position (line has 14 bytes incl. \r). | negative | no |
| decl_closing_brace | `internal/store/store.go:@get_close` | Position on the closing brace of Get. | negative | no |
| decl_concrete_error_type | `internal/store/disk/disk.go:@validate` | Declaration target on disk.Validate, whose only result is the concrete type *DiskError. | positive | no |
| decl_concrete_to_interface | `internal/store/disk/disk.go:@open` | Declaration target disk.Open: returns the *DiskError result of Validate converted to error after an e != nil check. | positive | no |
| decl_func_keyword | `internal/store/store.go:@get_kw` | Declaration target on the "func" keyword of (*Store).Get, at column 1 of the line. | positive | no |
| decl_generic | `internal/codec/codec.go:@decode` | Declaration target on the generic function codec.Decode[T]. | positive | no |
| decl_generic_no_instance | `-p ./internal/codec internal/codec/codec.go:@decode` | Generic declaration with only its own package loaded: no main, so Decode has no instance at all. | positive | no |
| decl_generic_one_binary | `-p ./cmd/report internal/codec/codec.go:@decode` | Same generic declaration, but only cmd/report is loaded, so the only instance is Decode[int]. | positive | no |
| decl_init | `cmd/ingest/main.go:@init` | Declaration target on func init() of cmd/ingest. | negative | no |
| decl_main | `cmd/ingest/main.go:@main` | Declaration target on func main() of cmd/ingest. | negative | no |
| decl_main_pkg_ingest | `cmd/ingest/main.go:@fetch` | Declaration target in package main of cmd/ingest: fetch wraps (*Store).Get. | positive | no |
| decl_main_pkg_report | `-p ./cmd/report cmd/report/main.go:@run` | Declaration target run() in package main of cmd/report (cmd/ingest also has a run), loading only ./cmd/report. | positive | no |
| decl_name | `internal/store/store.go:@get` | Declaration target on the method name of (*Store).Get (pointer receiver). | positive | no |
| decl_no_error_pointer_result | `internal/store/store.go:@new` | Declaration target store.New, which returns *Store (a pointer type that does not implement error). | negative | no |
| decl_no_error_result | `internal/store/store.go:@hits` | Declaration target on (Store).Hits, which returns int. | negative | no |
| decl_param | `internal/store/store.go:@get_param` | Position on the parameter name "key" of Get. | negative | no |
| decl_promoted_method | `internal/store/store.go:@check` | Declaration target on (*base).Check, the method promoted into Store. | positive | no |
| decl_receiver | `internal/store/store.go:@get_recv` | Position on the receiver name "s" of Get: not the name nor the func keyword. | negative | no |
| decl_result_type | `internal/store/store.go:@get_result` | Position on the "error" result type in the signature of Get. | negative | no |
| decl_run_ingest | `cmd/ingest/main.go:@run` | Declaration target run of cmd/ingest (package main; cmd/report has a run too), default ./... . | positive | yes |
| decl_type_param | `internal/codec/codec.go:@decode_tparam` | Position on the type parameter T in Decode[T any]. | negative | no |
| decl_value_receiver | `internal/store/store.go:@ping` | Declaration target: method with a value receiver, (Store).Ping. | positive | no |
| decl_warm_paths | `cmd/ingest/main.go:@warm` | Declaration target warm, the callee of call_go. | positive | no |
| fanout_above_boundary | `-fanout 3 internal/store/store.go:@iface_call` | -fanout one above the number of Candidates. | positive | no |
| fanout_at_boundary | `-fanout=2 internal/store/store.go:@iface_call` | -fanout equal to the number of Candidates (2) of s.be.Read. | positive | no |
| fanout_missing_value | `-fanout` | Flag without its value and no target. | negative | no |
| fanout_negative | `-fanout=-1 internal/store/store.go:@get` | Invalid -fanout. | negative | no |
| fanout_not_a_number | `-fanout=many internal/store/store.go:@get` | Non-numeric -fanout. | negative | no |
| fanout_one | `--fanout=1 internal/store/store.go:@embedded` | -fanout=1 (with the --flag spelling) on a static call s.Check(). | positive | no |
| fanout_overflow | `-fanout=99999999999999999999 internal/store/store.go:@iface_call` | -fanout value that overflows int. | negative | no |
| fanout_zero | `-fanout=0 internal/store/store.go:@iface_call` | -fanout=0 means never group. | positive | no |
| file_abs_nonexistent | `/nonexistent/errtrace-targets/main.go:1:1` | Absolute path to a file that does not exist (absolute paths are used as given, not joined to the cwd). | negative | no |
| file_build_tag_excluded | `internal/platform/limits_fast.go:@maxbatch` | File excluded by its build constraint (//go:build fast), queried without -tags. | negative | no |
| file_build_tag_included | `-tags fast internal/platform/limits_fast.go:@maxbatch` | Same file with -tags fast (one tag). | positive | no |
| file_build_tag_now_excluded | `-tags=fast internal/platform/limits_default.go:@maxbatch` | The //go:build !fast file queried with -tags=fast: it is now the excluded one. | negative | no |
| file_directory | `cmd/ingest` | A directory instead of a file. | negative | no |
| file_directory_pos | `internal/platform:5:6` | A package directory with a line:col. | negative | no |
| file_go_mod | `go.mod:1:1` | Go.mod as the target file. | negative | no |
| file_nested_module | `tools/lintcfg/cfg.go:@load` | A Go file inside the app tree that belongs to a nested module (tools/lintcfg has its own go.mod), i.e. outside example.com/targets. | positive | yes |
| file_non_go | `assets/schema.txt:1:1` | A non-Go file. | negative | no |
| file_nonexistent | `internal/store/missing.go:3:1` | A Go file that does not exist, in an existing package directory. | negative | yes |
| file_nonexistent_dir | `internal/nowhere/x.go:1:1` | A file in a directory that does not exist. | negative | no |
| file_parent_outside | `../../../../../../../../../../../../../../nonexistent-errtrace-targets/main.go:1:1` | Relative path that climbs out of the module (and up to the file system root) through ../ and does not exist. | negative | no |
| funclit_var_func_keyword | `internal/codec/codec.go:@trim_lit` | Declaration target on the "func" keyword of the function literal assigned to the package var codec.Trim. | positive | no |
| funclit_var_name | `internal/codec/codec.go:@trim_var` | Position on the variable name Trim in "var Trim = func...". | negative | no |
| iface_method_decl | `internal/store/store.go:@iface_read` | Position on the method name Read inside the Backend interface type. | negative | no |
| line_directive_decl | `internal/codec/gen.go:@format` | Declaration target below a "//line codec.tmpl:10" directive in a generated file (DO NOT EDIT header). | positive | no |
| line_directive_virtual_file | `internal/codec/codec.tmpl:12:6` | Position given in the virtual file named by the //line directive (codec.tmpl does not exist on disk). | negative | no |
| method_value | `cmd/ingest/main.go:@mval` | Position on Ping in the method value "ping := s.Ping" (no call). | negative | no |
| p_changes_candidates | `-p ./cmd/report internal/store/store.go:@iface_call` | Same interface call, but only cmd/report (mem Backend) is loaded with -p. | positive | no |
| p_empty_list | `-p , internal/platform/platform.go:5:6` | -p with only a comma: the list is empty, which falls back to ./... . | positive | no |
| p_import_path | `-p example.com/targets/cmd/report,example.com/targets/internal/platform internal/platform/platform.go:5:6` | -p given as import paths rather than ./ directories. | positive | no |
| p_malformed | `-p ./cmd/...ingest/[ internal/platform/platform.go:5:6` | -p with a garbled pattern "./cmd/...ingest/[" that matches no package. | negative (but accepted today) | yes |
| p_matches_nothing | `-p ./nope/... internal/platform/platform.go:5:6` | -p directory wildcard whose root directory does not exist. | negative | no |
| p_nested_module_pattern | `-p ./tools/lintcfg internal/platform/platform.go:5:6` | -p naming the directory of the nested module tools/lintcfg (not part of example.com/targets). | negative | no |
| p_no_go_files | `-p ./assets internal/platform/platform.go:5:6` | -p naming a directory without Go files. | negative | yes |
| p_nonexistent_dir | `-p ./cmd/missing internal/platform/platform.go:5:6` | -p naming a directory that does not exist. | negative | no |
| p_not_matching_target | `-p ./cmd/report internal/store/disk/disk.go:@read` | Target file in a package (internal/store/disk) that the -p pattern does not match and cmd/report does not import. | positive | no |
| p_one_pattern | `-p ./cmd/ingest cmd/ingest/main.go:@fetch` | -p with one pattern: only cmd/ingest and its dependencies. | positive | no |
| p_syntax_error_included | `-p ./cmd/report,./_broken/syntaxerr internal/platform/platform.go:5:6` | A package with syntax errors explicitly included next to a healthy one, Target elsewhere. | negative | no |
| p_two_patterns | `-p ./cmd/ingest,./cmd/report cmd/ingest/main.go:@fetch` | -p with two comma-separated patterns covering both binaries. | positive | no |
| p_type_error_excluded | `-p ./cmd/...,./internal/... internal/platform/platform.go:5:6` | Explicit patterns that exclude _broken: the type-error package must not disturb the Query. | positive | no |
| p_type_error_included | `-p ./...,./_broken/typeerr internal/platform/platform.go:5:6` | The isolated type-error package is explicitly included while the Target is elsewhere. | negative | no |
| p_wildcard_matches_nothing | `-p ./cmd/zzz... internal/platform/platform.go:5:6` | -p with a well-formed wildcard pattern that matches no package (go list prints: warning: "./cmd/zzz..." matched no packages). | negative (but accepted today) | yes |
| pos_blank_line | `internal/platform/platform.go:3:1` | Position on the blank line between the package clause and the doc comment. | negative | no |
| pos_call_ident | `internal/platform/platform.go:6:12` | Literal position on the callee identifier MaxBatch (6:12). | positive | no |
| pos_column_huge | `internal/platform/platform.go:5:9999` | Very large column on the declaration line. | negative | no |
| pos_column_one_past_end | `internal/platform/platform.go:5:21` | Column one past the last byte of line 5 ("func Probe() error {" has 20 bytes, so 5:21 is the newline position). | negative | no |
| pos_column_out_of_range | `internal/platform/platform.go:8:3` | Column beyond the end of the LAST line. Line 8 is "}" + newline, so it has 1 byte; 8:2 (the newline position) is the last valid column. | negative | yes |
| pos_column_two_past_end | `internal/platform/platform.go:5:22` | Column two past the last byte of line 5 (20 bytes): the first rejected column. | negative | no |
| pos_column_zero | `internal/platform/platform.go:5:0` | Column 0. | negative | no |
| pos_comment | `internal/platform/platform.go:4:10` | Position inside the doc comment of Probe (on the word Probe). | negative | no |
| pos_dot_slash | `./internal/platform/platform.go:5:6` | Relative path with a leading ./ . | positive | no |
| pos_dotdot | `cmd/../internal/platform/../platform/platform.go:5:6` | Relative path with ../ segments that stay inside the module. | positive | no |
| pos_file_end_offset | `internal/platform/platform.go:#180` | Offset equal to the file size (EOF, one past the final newline): documented as accepted. | negative | no |
| pos_file_only | `internal/platform/platform.go` | No position at all: defaults to 1:1, the first byte of the file (a comment). | negative | no |
| pos_file_start_offset | `internal/platform/platform.go:#0` | Offset 0, the start of the file (inside the package doc comment). | negative | no |
| pos_file_uri | `file://internal/platform/platform.go:5:6` | A file: URI instead of a path (the grammar accepts only file paths). | negative | no |
| pos_last_line_end | `internal/platform/platform.go:8:2` | Column one past the last byte of the last line (8:2, just after "}"). | negative | no |
| pos_last_line_start | `internal/platform/platform.go:8:1` | Position on the closing brace of the last declaration, on the last line of the file (8:1). | negative | no |
| pos_leading_zeros | `internal/platform/platform.go:05:006` | Line and column with leading zeros (05:006). | positive | no |
| pos_line_col | `internal/platform/platform.go:5:6` | Literal LINE:COL on the name Probe (5:6). | positive | no |
| pos_line_col_offset | `internal/platform/platform.go:5:6#128` | Combined LINE:COL#OFFSET form with a matching offset. | positive | no |
| pos_line_only | `internal/platform/platform.go:5` | LINE only: column defaults to 1, which is the "func" keyword of Probe. | positive | no |
| pos_line_out_of_range | `internal/platform/platform.go:9:1` | Line 9 of an 8-line file. | negative | no |
| pos_line_range | `internal/platform/platform.go:5-7` | Range of lines only, 5-7: the start is line 5 column 1, the "func" keyword of Probe. | positive | no |
| pos_line_zero | `internal/platform/platform.go:0:1` | Line 0. | negative | no |
| pos_missing_file | `:5:6` | Position without a file name. | negative | no |
| pos_missing_range_end | `internal/platform/platform.go:5:6-` | Range with a dangling "-". | negative | no |
| pos_negative_column | `internal/platform/platform.go:5:-6` | Negative column: "-" is the range separator, so 5:-6 parses as start "5:" and end "6". | negative | no |
| pos_negative_line | `internal/platform/platform.go:-5:6` | Negative line number. | negative | no |
| pos_not_a_number | `internal/platform/platform.go:five:6` | Non-numeric line. | negative | no |
| pos_offset | `internal/platform/platform.go:#128` | File:#OFFSET form, byte offset 128 = 5:6. | positive | no |
| pos_offset_mismatch | `internal/platform/platform.go:5:6#129` | Combined form whose offset contradicts the line:col. | negative | no |
| pos_offset_out_of_range | `internal/platform/platform.go:#181` | Offset one past EOF. | negative | no |
| pos_offset_range | `internal/platform/platform.go:#128-#133` | Range in offset form #O-#O2: the start (#128 = 5:6) is used. | positive | no |
| pos_package_clause | `internal/platform/platform.go:2:9` | Position on the package name in the package clause. | negative | no |
| pos_range | `internal/platform/platform.go:5:6-7:12` | Range form L:C-L2:C2: only the start (5:6) is used. | positive | no |
| pos_range_end_before_start | `internal/platform/platform.go:7:1-5:6` | Range whose end precedes its start. | negative | no |
| pos_range_end_out_of_range | `internal/platform/platform.go:5:6-99:1` | Range with a valid start but an end line beyond EOF. The usage says only the start is used. | negative | no |
| pos_range_short_end | `internal/platform/platform.go:5:6-11` | Range form L:C-C2 (end column on the same line). | positive | no |
| pos_too_many_parts | `internal/platform/platform.go:5:6:7` | Three numeric components. | negative | no |
| pos_trailing_colon | `internal/platform/platform.go:5:` | Trailing colon with a missing column. | negative | no |
| pos_whitespace | `internal/platform/platform.go:6:1` | Position on the leading tab of the line "_, err := MaxBatch()". | negative | no |
| return_operand_err | `cmd/ingest/main.go:@warm_ret` | Position on the identifier err in "return err" of warm (a plain error variable, after the errors.Is guard). | negative | no |
| return_operand_ident | `internal/codec/gen.go:@format_ret` | Position on the sentinel identifier ErrUnknownFormat in a return statement. | negative | no |
| tags_change_callee | `-tags fast cmd/report/main.go:@probe_call` | Call-site platform.Probe() in cmd/report under -tags fast. | positive | no |
| tags_empty | `-tags= -p= internal/platform/platform.go:5:6` | Empty -tags= and empty -p= values: both fall back to the defaults. | positive | no |
| tags_one_of_two | `-tags audit internal/platform/audit.go:@audit` | Only one of the two required tags. | negative | no |
| tags_repeated_flag | `-tags fast -tags audit internal/platform/audit.go:@audit` | The -tags flag given twice: the standard flag package keeps the last value only, so "fast" is dropped. | negative | no |
| tags_two | `-tags fast,audit internal/platform/audit.go:@audit` | Two comma-separated tags: audit.go needs fast && audit. | positive | no |
| tags_unknown | `-tags nosuchtag,,fast cmd/report/main.go:@probe_call` | An unknown tag plus an empty list element plus fast. | positive | no |
| target_in_syntax_error_pkg | `_broken/syntaxerr/syntaxerr.go:@half` | Target inside a file with a syntax error. | negative | no |
| target_in_type_error_pkg | `_broken/typeerr/typeerr.go:@broken` | Target inside the package that fails to type-check (pulled in through the target file, not -p). | negative | no |
| test_file_call | `internal/store/store_test.go:@test_call` | Call-site target inside a Test function of an in-package _test.go file. | negative | no |
| test_file_external | `internal/store/store_ext_test.go:@roundtrip` | Declaration target in an external test package (package store_test). | negative | no |
| test_file_func | `internal/store/store_test.go:@seed` | Declaration target in an in-package _test.go file (helper seed, package store). | negative | no |
| type_in_field | `internal/store/store.go:@field_type` | Position on the type name Backend used as a struct field type. | negative | no |
| type_name | `internal/store/store.go:@backend_type` | Position on the type name Backend in its type declaration. | negative | no |
| unicode_call_after_tabs | `internal/codec/unicode.go:@uni_call` | Call-site target Größe(raw) on a line that starts with two tabs and a comment with multi-byte runes. | positive | no |
| unicode_decl | `internal/codec/unicode.go:@groesse` | Declaration target on the identifier Größe (2 multi-byte runes, 7 bytes). | positive | no |
| unicode_decl_mid_rune | `internal/codec/unicode.go:4:21` | Literal position 4:21 is the second byte of "ö" inside Größe (byte columns). | positive | no |
| unicode_decl_past_name | `internal/codec/unicode.go:4:25` | Literal position 4:25: the "(" right after Größe (the identifier ends at byte column 24). Counting runes instead of bytes would land elsewhere. | negative | no |
