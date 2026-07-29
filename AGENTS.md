# CLAUDE.md

Guidance for coding agents working in this repository.

## Start Here

- Read [README.md](README.md) for the project overview and container design.
- Read [USAGE.md](USAGE.md) for the public CLI and runtime behavior.
- Read [CONTRIBUTING.md](CONTRIBUTING.md) before changing code.
- Read [CONTEXT.md](CONTEXT.md) and the relevant decisions under [`docs/adr/`](docs/adr/) before changing domain behavior or architecture.

For repository workflow conventions, see:

- [Issue tracker](docs/agents/issue-tracker.md)
- [Triage labels](docs/agents/triage-labels.md)
- [Domain documentation](docs/agents/domain.md)

Issues and specs live under `.scratch/<feature-slug>/`.

## Critical Invariants

- Keep `claude-contained` and `claude-docked` behavior and flags in sync; they remain the differential oracle for the Go launcher.
- In the Go launcher, container-runtime-specific commands belong only in `internal/runtime`.
- The Dockerfile must stay below Apple Containers' 16k file limit. Put scripts and long configuration in `image/` and copy them into the image.
- Preserve the privilege drop and fail-closed sandbox setup. The sandbox runtime must not run as root or use a policy writable by the contained agent.
- Preserve worktree-lock cleanup and signal behavior. Use `signal.Notify`, not `signal.Ignore`, in the Go launcher.
- Every production shell script should state its purpose at the top of the file.

Run `make quality` for the standard contributor gate. See [CONTRIBUTING.md](CONTRIBUTING.md#quality-checks) for required tool versions and the broader test matrix.
