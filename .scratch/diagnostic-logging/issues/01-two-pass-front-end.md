# 01 — Replace the runtime pre-scan with a two-pass front end

**What to build:** A permissive parse followed by a runtime-aware validation
pass, so no flag is read twice and every diagnosis happens after the launcher
knows what it is diagnosing. Behavior-preserving: this ticket changes structure
and nothing a user can observe.

`cli.ScanRuntime` exists because the runtime had to be chosen before `Parse`
ran. Both of its documented justifications are now gone or vestigial:

- *"Parse's error messages embed the program name."* Dead. `cli.go:164` calls
  `progName` "the runtime-specific program name," but `runtime.go:178-182`
  defines `ProgName` as the single name the launcher installs under and both
  profiles set `Name: ProgName` (`apple.go:32`, `docker.go:40`). Ticket 11 of
  the go-launcher effort killed the second name; the comment was left stale.
- *"`--help` prints the selected runtime's own literal text."* `run.go:58-61`
  prints `prof.Help` **after** `cli.Parse` returns, so it can read
  `cfg.ContainerRuntime` instead of a pre-scan.

`run.go:66-68` already concedes the point: validation uses "the real parse rather
than the pre-scan, so a disagreement between the two could only mis-select, never
mis-report." The pre-scan is vestigial.

The new order is `host.Probe()` → `cli.Parse` (syntactic, permissive, program
name is the constant) → select the runtime from `cfg.ContainerRuntime` →
`--help` → validation pass.

**Pass 1 must defer reporting, not just validation.** This is the part that is
easy to half-do. If `Parse` keeps producing the messages at `cli.go:179`,
`:407`, `:420-428`, `:445-450` and returning early, it still runs before ticket
02's logger exists and stays the one un-debuggable component — the blind spot
moves rather than closes. Pass 1 records what it saw; pass 2 owns every
diagnosis.

Ordering that must survive: `--help` still wins over an invalid runtime
selection, exactly as `run.go:63-68` describes.

**Blocked by:** None

**Status:** resolved

- [x] `cli.ScanRuntime` is deleted, along with `TestScanRuntime`,
      `TestScanRuntimeAgreesWithParse`, and `TestScanRuntimeSkipsBuildContextValue`.
- [x] No flag value is derived twice anywhere in the front end.
- [x] `cli.Parse` is permissive: it records what it saw and reports nothing.
- [x] Every diagnosis that `Parse` used to emit is produced by the validation
      pass, with the same text and the same exit codes.
- [x] `--help` still wins over an invalid `--container-runtime` value, and still
      prints the selected runtime's own help text.
- [x] `cli.go:164`'s stale "runtime-specific program name" comment is corrected.
- [x] **The golden suite produces a zero diff** across all three trees with no
      `-update`: exit codes, `runtime-args`, `stdout`, `stderr`, and `filesystem`
      are byte-identical. This is the ticket's proof of correctness and only
      holds while nothing else changes.
- [x] `make quality` passes.

## Comments

### Change Record — 2026-07-31

Implemented the two-pass front end on branch
`diagnostic-logging-two-pass-front-end` from fixed point `ac0a983`.

Technical decisions:

- `cli.Parse` is now a silent recording pass; exported `cli.Validate` owns
  syntax and semantic diagnostics.
- The merged grammar preserves the old pre-scan's asymmetric handling of a
  malformed separate `--container-runtime`, including help-profile selection
  and `--` boundary behavior.
- `runWith` now orders the front end as
  `Probe → Parse → Select → Help → CLI validation → runtime validation`.
- ADR-0005 records why the front end must be silent before the diagnostic
  stream is configured.

Changed code and tests:

- `internal/cli/cli.go` and `internal/cli/cli_test.go`
- `cmd/claude-contained/run.go` and
  `cmd/claude-contained/selection_test.go`
- `internal/runtime/runtime.go`
- `docs/adr/0005-diagnostic-stream.md`

Completed structurally:

- Removed `cli.ScanRuntime`, `valueTakingFlags`, and the three obsolete scanner
  tests.
- No front-end flag value is derived by a second scan.
- Parse-time failures are deferred and rendered by validation.
- Added exact diagnostic, ordering, runtime-help, scanner-boundary, and
  no-runtime-interaction regression coverage.
- Golden fixtures are unchanged from the fixed point.

Verification performed:

- `git diff --check` passed.
- Fixed-point golden-file diff passed with no changes.
- Independent Standards and Spec review found no static implementation defect
  or scope creep.

Verification completed outside the sandbox:

- Required Go, `gofmt`, ShellCheck, and golangci-lint versions matched.
- Formatting, focused tests, the golden suite, `make quality`,
  `make test-shell`, and `git diff --check` passed.
- Golden fixtures remained unchanged from `ac0a983` across all three runtime
  trees.
- All 13 built-binary manual cases passed with exact stdout, stderr, exit-code,
  runtime-marker, and filesystem checks.
- The isolated verification harness was removed.
- `make fmt` formatted the already-modified `internal/cli/cli.go` and
  `internal/cli/cli_test.go`; all other pre-existing worktree changes were
  preserved.
