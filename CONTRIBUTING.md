# Overview

This project is a single Go launcher, `claude-contained`, that drives either Apple Containers or Docker through a container-runtime seam. The bash-launcher port this codebase started as is complete and both scripts are deleted; see `docs/adr/0004-go-launcher-rewrite.md` for how the rewrite proved itself before that deletion. Changes should preserve behavior across both runtimes.

# Critical Invariants

- Runtime-conditional behavior lives in `internal/runtime` Profiles (the default DNS resolver, the "runtime is not running" prompt, the `-H` capability notice, the help text); nothing above that seam branches on which runtime is selected.
- In the Go launcher, container-runtime-specific commands belong only in `internal/runtime`.
- The Dockerfile must stay below Apple Containers' 16k file limit. Put scripts and long configuration in `image/` and copy them into the image.
- Preserve the privilege drop and fail-closed sandbox setup. The sandbox runtime must not run as root or use a policy writable by the contained agent.
- Preserve worktree-lock cleanup and signal behavior. Use `signal.Notify`, not `signal.Ignore`, in the Go launcher.
- Every production shell script should state its purpose at the top of the file.

Run `make quality` for the standard contributor gate. See [CONTRIBUTING.md](CONTRIBUTING.md#quality-checks) for required tool versions and the broader test matrix.

# Quality Checks

Run `make quality` for the standard contributor gate.

## Development Setup

The local quality gate requires:

- ShellCheck `0.11.0`
- golangci-lint `2.12.2` from github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
- A Go toolchain compatible with `go.mod`

Verify tool versions match CI with:

```bash
shellcheck --version
golangci-lint version
make check-tools
```

## Details

Run the fast aggregate gate:

```bash
make quality
```

It includes these non-mutating checks:

```bash
make fmt-check
make vet
make test
make lint-go
make lint-shell
```

The formatter rewrites Go files and remains a separate command:

```bash
make fmt
```

The slower runtime suites are deliberately excluded from the fast static and unit baseline:

```bash
make test-shell
```

GitHub Actions runs `make quality` for pull requests and pushes to `main`.

### Golden tests

`cmd/claude-contained`'s golden suite calls `runWith` in-process with the host
platform injected -- **not** a subprocess against the built binary -- and drives
it against a stubbed container runtime across three configurations:
`apple-darwin`, `docker-darwin`, `docker-linux`. It asserts the full observable
result against committed data under `cmd/claude-contained/testdata/golden/`:
runtime argv, stdout, stderr, exit status, and a filesystem manifest.

Injecting the platform is what makes all three configurations reachable from
either host. A subprocess reads the real `GOOS`, and Apple Containers is
unselectable off macOS, so a subprocess suite could never cover `apple-darwin`
on CI at all. See [ADR-0004](docs/adr/0004-go-launcher-rewrite.md) before
changing this. The compiled-binary black-box suite below is what exercises the
shipped binary end to end.

- `go test ./cmd/claude-contained -run TestGolden` runs it; it is part of
  `make test` / `make quality`.
- `go test ./cmd/claude-contained -run TestGolden -update` regenerates the
  goldens for cases whose behavior changed and prints a diff of what it
  wrote. It refuses to write a file for a case that trips either liveness
  guard (empty on every axis, or a runtime-args declaration mismatched
  against what was actually captured) -- an empty or mis-declared golden is
  unwritable by construction.
- **A changed golden in a pull request is a behavior change**, not
  formatting. Explain it in the commit message the same way you would
  explain a change to the code path it covers.
- Goldens are normalized so a fixture's own temp-directory path, timestamps,
  and machine-specific values never appear literally. The tokens are
  `<PROJ>`, `<HOME>`, `<ROOT>`, `<PHASH>`, `<TIME>`, `<TOKEN>`, `<PID>`,
  `<EPOCH>`, `<UID>`, `<GID>`, `<ARCH>`, and `<LMODE>` (a symlink's own
  permission bits, which Linux and macOS report differently and which no
  production code path ever sets or reads). A new volatile field is one new
  named substitution, not a per-case exception.

### Compiled-binary black-box tests

`cmd/claude-contained/artifact_test.go` drives the **built launcher artifact**
as a real subprocess, covering only what an in-process call cannot prove: that
the shipped binary embeds and emits the help text verbatim, selects its runtime
from `argv[0]`, propagates a real child exit status, and gives its foreground
child the correct signal disposition. Exact CLI error text and the two-pass
selection grammar stay in the in-process suites (`internal/cli`,
`selection_test.go`); they are not re-proven here. See
[ADR-0008](docs/adr/0008-go-owned-test-migration.md).

The shared harness is `internal/blackbox`. It builds the launcher once per test
process into a temporary directory (never a pre-existing `bin/`), creates the
`-docked` symlink beside it, and models external runtimes with a stub that is
the test binary re-executed under a runtime name — enabled only in a launcher
subprocess via `BLACKBOX_STUB_SPEC`, recording each call's argv structurally.
Signal and exit tests synchronize on a ready marker and a FIFO release rather
than a sleep, so a launcher that mishandled a signal hangs and trips a hang
guard instead of passing on a lucky timing. It is part of `make test` /
`make quality` and needs no running container runtime (though it does build git
worktree fixtures, so `git` must be present).

## Repository Architecture

- **Go launcher**: `cmd/claude-contained` is the entry point. `internal/cli` parses flags, `internal/host` inspects and prepares host state, `internal/plan` builds a resumable execution plan, and `internal/runtime` is the container-runtime seam.
- **Container image**: `Dockerfile` assembles the image. Production helpers and configuration live under `image/`; each script documents its purpose at the top of the file.
- **Tests**: Go unit tests live beside their packages. Golden tests in `cmd/claude-contained` call `runWith` in-process against a stubbed container runtime and assert the full observable result (runtime argv, stdout, stderr, exit status, filesystem manifest) against `testdata/golden/` -- see "Golden tests" above. The built binary itself is under test in `cmd/claude-contained/artifact_test.go`, the compiled-binary black-box suite, via the `internal/blackbox` harness -- see "Compiled-binary black-box tests" above. Shell suites under `tests/` still exercise the shipped build outputs for areas not yet migrated to Go (`make test-shell` runs every `tests/*.test.sh` against both build outputs; it is not part of `make quality`). Those suites create scratch directories through `tests/lib/tmp.sh` (`mk_tmpdir`/`mk_tmpdir_resolved`/`mk_tmpfile`/`mk_tmpname`) and delete them through `safe_rm_rf`, never bare `mktemp`/`rm -rf`: bare `mktemp -d` can fail into an empty string that a later `cd`+`pwd -P` turns into the repo root, so `safe_rm_rf` refuses the repo root, its ancestors, `$HOME`, `/`, and empty paths. The guard is covered by `tests/tmp-lib.test.sh`.
- **Design records**: [CONTEXT.md](CONTEXT.md) defines project vocabulary. Architectural decisions live under [`docs/adr/`](docs/adr/).

The VS Code template has separate implementation notes in [devcontainer/CLAUDE.md](devcontainer/CLAUDE.md).

### Citations of the retired bash launchers

Comments across `internal/` cite the launchers they were ported from, in the form
`claude-contained:1970` or `claude-docked:1828` -- a file name and a line number.
Those two files were deleted in ticket 11, so the citations do not resolve against a
checkout. **They also do not resolve against a single commit**: the launchers kept
changing while the port was in progress, so a line number means whatever it meant when
that comment was written. `claude-docked:1828` is the host-gateway mapping as of ticket
09, but line 1813 by the time the launchers were deleted.

Resolve a citation against the commit that introduced the comment, not against the tip:

```bash
commit=$(git blame -L 18,19 --porcelain internal/runtime/platform.go | head -1 | cut -d' ' -f1)
git show "$commit^:claude-docked" | sed -n '1826,1832p'   # a few lines of context
```

Read them as anchors, not exact offsets -- some point at the head of a block rather than
the operative line. The deletion parent is `973eeff`, so `git show 973eeff:claude-docked`
recovers the final state of either launcher when you want the whole file rather than one
citation.

They are kept rather than stripped because they are the only surviving evidence for
*why* a behavior is shaped the way it is -- particularly the deliberate divergences,
where the comment records that the Go code knowingly does something the bash original
did not. Do not add new ones: for code written after the cut over there is no bash
original to cite, and the reason belongs in the comment itself.

## Contributor Guardrails

### Runtime-Conditional Behavior

Treat changes to flags, validation, mounts, prompts, naming, cleanup, or tool invocation as changes that may need to reach both runtimes. There is one implementation now, not two kept in lockstep: update the shared code once, and the two `Profile` help texts together when a flag or behavior description changes.

The Go launcher selects its container runtime in this order: `--container-runtime` (`apple` or `docker`), else `CLAUDE_CONTAINED_RUNTIME`, else an `argv[0]` basename containing `dock`, else the host platform — Apple Containers on macOS, Docker elsewhere. A basename *without* `dock` is not a selection, because "not docked" cannot mean Apple Containers on a host that has none. `argv[0]` is a compat affordance, not the primary mechanism: it exists for a user who symlinks `claude-docked` to the installed binary (or still has one from before the bash launchers were retired), and it is how the shell test suites drive the Docker runtime as a target path.

`--build-context DIR` names the checkout `--rebuild` builds from, ahead of `CLAUDE_CONTAINED_BUILD_CONTEXT` and self-location (see `docs/adr/0004-go-launcher-rewrite.md`).

`--layer DIR` names the project's tooling layer directory, ahead of `CLAUDE_CONTAINED_LAYER` and the per-project `.claude-contained/layer` default (see `docs/adr/0006-tooling-layers.md`). Build labels are emitted on Docker only, mirroring the run path.

Code above `internal/runtime` must not mention `container` or `docker` commands.

See [ADR-0003](docs/adr/0003-flag-only-cli.md) for the flag-only CLI, [ADR-0004](docs/adr/0004-go-launcher-rewrite.md) for the Go port, [ADR-0005](docs/adr/0005-diagnostic-stream.md) for the separate diagnostic stream, [ADR-0006](docs/adr/0006-tooling-layers.md) for the base/derived image split, and [ADR-0008](docs/adr/0008-go-owned-test-migration.md) for the Go-owned test migration and its compiled-binary black-box boundary.

### Add Diagnostic Records Deliberately

Diagnostic records are selective observations for contributors, not a mirror of every user-facing print. Add one when the existing prose loses useful cause or decision detail. Retrieve the logger from `context.Context` through `internal/diagnostic` and bind exactly one of its closed components: `cli`, `host`, `env`, `plan`, `runtime`, `worktree`, `zellij`, `attach`, `rebuild`, or `layer`. Never use package-level `slog` calls, `slog.Default`, or `slog.SetDefault` in production.

Records use `kind=diagnostic` plus `component`. Relocated output uses `kind=output` plus `stream=stdout|stderr` and bypasses the diagnostic level filter because discarding it would be data loss. Do not add `phase` or `operation`; put the action in the record message. Omit timestamps and source, and add an explicit `duration` only for elapsed work where it matters, such as runtime liveness or image rebuilds.

Use safe typed carriers rather than attaching raw configs, host state, plans, runtime arguments, or errors. Environment values and known tokens must never enter launcher-generated diagnostics at any level. Environment assignments expose only key and provenance, host tokens expose presence, and runtime argv must use the `-e`-redacting carrier. Relocated output is existing text carried verbatim and may contain secrets; do not describe it as redacted or safe to share.

When adding a component, flag, help description, or launcher-read environment variable, update the closed set, both `internal/runtime/help_*.txt` files, `USAGE.md`, focused metadata/redaction tests, and every test harness environment blacklist together. Do not add a diagnostic dimension to the golden fixtures.

### Keep the Dockerfile Small

Apple Containers fails when the Dockerfile reaches 16k. Keep it comfortably below that limit. Put scripts or long configuration blobs under `image/` and use `COPY`; do not add large inline heredocs to the Dockerfile.

This separation also protects build caching: editing a runtime helper should not force unrelated package-install layers to rebuild.

The build context is trimmed the same way: `bin/`, `cmd/`, `internal/`, `go.mod`, `Makefile`, `tests/`, `devcontainer/` and `.scratch` are excluded via `.dockerignore`. A multi-megabyte compiled binary must never ship to the builder, and `*.md` in `.dockerignore` does not match nested paths, so `.scratch` needs its own entry. See `docs/adr/0004-go-launcher-rewrite.md` Consequences for the rationale; this is the one place it is documented.

### Preserve Security Boundaries

The container or VM is the primary isolation boundary. The sandbox runtime is defense in depth:

- Generate its filesystem policy at runtime because mounted paths vary and Linux path matching is literal.
- Keep the generated policy root-owned and read-only.
- Fail closed if policy generation fails; `--no-sandbox` is the explicit escape hatch.
- Run the sandbox as the unprivileged user, never as root.
- Route commands started through runtime `exec` through `srt-run`, because `exec` bypasses the image entrypoint.

Project env files are writable by the contained agent and are convenience inputs, not a security boundary. Parse them literally, never source them, and preserve the reserved-key checks.

### Protect Worktrees and Signals

Mounted Git metadata can expose linked worktrees that are not mounted into the container. Preserve the auto-lock ownership rules, stale-mutex recovery, and cleanup ordering so an in-container prune cannot remove hidden worktrees.

In the Go launcher, use `signal.Notify`, not `signal.Ignore`. Ignored signal dispositions survive `exec` and can prevent the container-runtime child from terminating, which would also leak worktree locks.

## Documentation Changes

- Keep [README.md](README.md) focused on the project overview, quick start, caveats, supported tools, and high-level design.
- Put flags, examples, operational behavior, and troubleshooting in [USAGE.md](USAGE.md).
- Put development workflow and implementation constraints here.
- Keep [AGENTS.md](AGENTS.md) concise and agent-specific.
- When flags change, there is one help source now (`internal/runtime/help_*.txt`); update both `Profile` help texts and `USAGE.md` together.
