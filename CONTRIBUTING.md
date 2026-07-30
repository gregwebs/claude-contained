# Overview

This project maintains two production bash launchers while incrementally porting their behavior to a shared Go binary. Changes should preserve behavior across Apple Containers and Docker.

# Critical Invariants

- Keep `claude-contained` and `claude-docked` behavior and flags in sync; they remain the differential oracle for the Go launcher. Three differences are intentional: `claude-contained` forces a default DNS resolver, `claude-docked` emits Zellij tracking labels, and `claude-contained` prints the `-H` capability notice. Everything else diverging is a bug.
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
make difftest
for test in tests/*.test.sh; do "$test"; done
```

GitHub Actions runs `make quality` for pull requests and pushes to `main`.

## Repository Architecture

- **Bash launchers**: `claude-contained` targets Apple Containers and `claude-docked` targets Docker. They expose the same flag interface and must be changed together, apart from the three intentional differences listed under Critical Invariants. They are also the behavioral oracle for the Go rewrite.
- **Go launcher**: `cmd/claude-go` drives the port. `internal/cli` parses flags, `internal/host` inspects and prepares host state, `internal/plan` builds a resumable execution plan, and `internal/runtime` is the container-runtime seam.
- **Container image**: `Dockerfile` assembles the image. Production helpers and configuration live under `image/`; each script documents its purpose at the top of the file.
- **Tests**: Go unit tests live beside their packages. Shell suites under `tests/` exercise runtime behavior. `tests/differential/` compares the Go candidate with both bash launchers.
- **Design records**: [CONTEXT.md](CONTEXT.md) defines project vocabulary. Architectural decisions live under [`docs/adr/`](docs/adr/).

The VS Code template has separate implementation notes in [devcontainer/CLAUDE.md](devcontainer/CLAUDE.md).

## Contributor Guardrails

### Preserve Runtime Parity

Treat changes to flags, validation, mounts, prompts, naming, cleanup, or tool invocation as cross-runtime changes. Update both bash launchers, their help text, the Go port where implemented, and the appropriate parity tests.

The Go launcher selects its container runtime in this order: `--container-runtime` (`apple` or `docker`), else `CLAUDE_CONTAINED_RUNTIME`, else an `argv[0]` basename containing `dock`, else the host platform — Apple Containers on macOS, Docker elsewhere. A basename *without* `dock` is not a selection, because "not docked" cannot mean Apple Containers on a host that has none.

`--container-runtime` and `--build-context` are the two flags the bash launchers do not have; they reject both as unknown, and `tests/arg-parsing.test.sh` pins each half of both divergences so a later parity fix does not delete either flag. `--build-context DIR` names the checkout `--rebuild` builds from, ahead of `CLAUDE_CONTAINED_BUILD_CONTEXT` and self-location (see `docs/adr/0004-go-launcher-rewrite.md`); bash always finds it by self-location, since it is a script inside the checkout.

Code above `internal/runtime` must not mention `container` or `docker` commands.

See [ADR-0003](docs/adr/0003-flag-only-cli.md) for the flag-only CLI and [ADR-0004](docs/adr/0004-go-launcher-rewrite.md) for the Go port.

### Keep the Dockerfile Small

Apple Containers fails when the Dockerfile reaches 16k. Keep it comfortably below that limit. Put scripts or long configuration blobs under `image/` and use `COPY`; do not add large inline heredocs to the Dockerfile.

This separation also protects build caching: editing a runtime helper should not force unrelated package-install layers to rebuild.

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
- When flags change, update launcher help, generated Go help fixtures, and `USAGE.md` together.
