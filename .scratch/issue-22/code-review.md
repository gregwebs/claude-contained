# Code Review — Issue #22: Positional container command

Branch `issue-22-implementation`, reviewed as uncommitted working-tree changes vs `main`.

## Verdict: **SHIP** (one optional nit)

The diff implements #22 as the plan/brief specify. Every acceptance criterion in the plan
(sections 1, 6, 7, 9) is met, the grammar is correct including the tricky `-R`/`-a`
optional-value edges, the golden regeneration is scoped exactly to cases 02–07 and 09
across all three trees, and the new tests genuinely assert the behavior rather than
rubber-stamping it. I found **0 blocking issues**. Tests substantiate the claims: `go build`,
`go vet`, `gofmt` clean; `internal/cli` + `internal/plan` pass; `TestArtifact|Diagnostic`
pass; `internal/imagescript -run ToolEnv` passes; `TestGolden` fails **only** the 15
documented sandbox-only worktree-lock subtests (41/52/54/55/56 × 3 trees) — cases 02–07
and 09 all pass. These worktree failures reproduce on `main` and are unrelated to this diff.

---

## Blocking

None.

---

## Should-fix

None.

---

## Nits

### N1. `-t`/`--tool` records a `value` that is never rendered (dead store)
`internal/cli/cli.go:439` — the `syntaxToolRemoved` failure is recorded as
`syntaxFailure{kind: syntaxToolRemoved, value: next}`, but the `tool-flag-removed` render
arm (`cli.go:604-607`) prints only fixed strings and never reads `failure.value`. The stored
`next` is therefore dead. Harmless, and the plan (§4.3) explicitly left echoing the value as
optional, so this is purely cosmetic. Suggested fix: either drop `value: next` for clarity, or
(if you prefer to echo it) add the tool name to the hint. Leaving it is also fine.

---

## Notes verified (not findings)

These were checked adversarially and are correct / acceptable, recorded so a re-review need
not re-derive them:

- **§9 `-R` decision is consistent.** `-R npm test` → `-R` consumes `npm` as the mode,
  `test` becomes the command, and the new `rebuild-with-command` conflict fires naming
  `--rebuild=MODE` (`cli.go:667-671`; asserted in `TestCommandConflictsWithNoWhereToRun`).
  Crucially, the unknown-rebuild-mode check lives at runtime in `rebuild.go:96`, *after*
  `ValidateContext`, so the conflict check wins for `-R npm test`. `-R npm` alone (no trailing
  command) leaves `Command` empty, skips the conflict, and falls through to the existing
  unknown-mode rejection — identical to `-R nonsense` (golden 59, unchanged). Golden 59 and
  `TestParsePreservesOptionalValueConsumption` both hold.

- **Grammar is correct.** First non-dash token terminates flag parsing (`cli.go:234-236`,
  `Command = args[i:]`); `--` boundary uses `args[i+1:]` and flags a dash-leading first token
  as `command-starts-with-flag` (`cli.go:216-224`); the old `syntaxPositional` default arm is
  deleted and now unreachable. No off-by-one: `args[i:]` includes the current token,
  `args[i+1:]` skips the `--`. Flags-before-command vs command-owns-its-flags, `-- npm test`
  ≡ `npm test`, and empty command are all pinned in `TestFirstPositionalTerminatesFlagParsing`.

- **The four command-conflict checks** (`shell`/`rebuild`/`zellij --attach`/`attach`) are placed
  after the syntax-failure and Zellij checks, each gated on `len(cfg.Command) > 0`, each returns
  `ExitUsage` with a distinct `validation_kind`. No silent discard. The documented wart —
  `--zellij --attach npm test` *without* `--` reports `zellij-attach-name` instead of
  `zellij-attach-with-command` because `-a` grabs `npm` as its name first — is a real but
  acceptable consequence of keeping `-a`'s bare-value consumption (plan §4.6); it still errors
  at exit 2, so criterion #7 (never a silent discard) holds. Called out in the test comment.

- **Golden regen scoped correctly.** `git diff --stat` shows only 02–07 and 09 × 3 trees (21
  files). 02 drops its trailing `claude` operand (ends at `claude-contained:latest`). 07 and 09
  are now exit-2 usage errors with `runtime-args (empty)` and filesystem manifests collapsed to
  just the pre-existing `HOME`/`PROJ` fixture dirs. 03–06 carry their program name as a
  positional operand.

- **Default command / fallback.** `Dockerfile:181` → `CMD ["/usr/local/bin/shell-run"]`;
  `image/tool-env.sh:31-38` empty-argv now `set -- /usr/local/bin/shell-run` instead of
  `exit 2`. `TestToolEnvEmptyArgvFallsBackToShellRun` reaches the guard via `--directory`
  consuming both args (`shift 2` → `$# -eq 0`) and asserts no "command required" and a
  shell-run exec attempt.

- **Diagnostics.** `command_source`/`command_len` replace `tool`/`shell` in both `run.go:104-105`
  and `internal/plan/diagnostic.go:28-43`; the shared `cli.CommandSource(cfg)` helper is defined
  once in `cli.go:527-537` and used in both places (no duplication — DRY satisfied). Grepped: the
  raw command token is never logged, only `len(cfg.Command)`. `TestDiagnosticSummaryOmitsEnvironmentAndCommandValues`
  asserts the new anchors.

- **Closed set fully removed from the run path.** `ToolError`, `toolCommand`,
  `newContainerCommand`, the `containerCommand` struct, `addExtraMount`, `diagnosticToolName`,
  and the `Unknown tool:`/`Supported tools:` arm are all deleted. All remaining `--add-dir`
  strings in the diff are deletions. `-m` no longer injects anything
  (`TestExtraMountDoesNotInjectAddDir` is a strong migration anchor: `claude … --model sonnet`
  + `-m /data` yields exactly that command and still mounts `/data`).

- **No scope leakage.** `internal/attach/attach.go` change is only two stale doc-comment fixes;
  `cmd/claude-contained/attach.go` is only the sanctioned transitional `Tool:"claude"`/`Yolo:false`
  shim with a `#23` pointer. No #23 (attach capability) or #39 (`--add-dir` user config) behavior
  leaked in. No new config-file plumbing.

- **Test quality.** New/rewritten tests assert real behavior (exact stderr, exact `Command`
  slices, mount presence, absence of `--add-dir`). `command_test.go` was genuinely rewritten to
  the new function signature rather than stubbed. `TestHostForwardNoticePrecedesTheOtherNotices`
  was correctly re-anchored to the node_modules-overlay notice after the vibe+yolo warning was
  deleted.

- **Docs.** `USAGE.md`, `help_contained.txt`, `help_docked.txt` describe the new grammar, drop
  `-t`/`-y`, rewrite `-s` to the escape-hatch meaning, drop the `--add-dir` line, and add a
  migration note about already-built images keeping `CMD ["claude"]`. ADR-0009 gains the accepted-
  limitation framing for Zellij + the `-s` inconsistency. The help-text `artifact_test` passes
  verbatim.

---

## Followup resolution (orchestrator)

- **Nit (cli.go:440 dead store `value: next` on `syntaxToolRemoved`)** — RESOLVED. The unread `value: next` was removed; the render arm for `tool-flag-removed` never consumed it and the plan marked echoing it optional. `go test ./internal/cli` remains green after the change.

No blocking or should-fix findings were raised, so no implementer followup cycle was required.
