# AI Contained

Seamlessly run CLI coding agents (Claude, Codex, Copilot, Gemini, and Vibe) inside an [Apple Container](https://github.com/apple/container) or [Docker](https://www.docker.com) container with persistent state.

The goal is a normal local workflow with a smaller host footprint: alias a tool to `claude-contained --yolo`, then use it as usual. Only the project directory and the extra mounts you select are shared with the container. Login state and common Claude extension resources persist across contained sessions.

## Documentation

- [Usage](USAGE.md) — flags, examples, runtime behavior, networking, sandboxing, and troubleshooting
- [Contributing](CONTRIBUTING.md) — development setup, quality checks, architecture, and contributor guardrails
- [Devcontainer template](devcontainer/README.md) — VS Code devcontainer setup and customization

## Important Caveats

- **Host localhost access**: `-H PORT` reaches host services listening on `0.0.0.0` under either container runtime, but only Docker reaches services bound to `127.0.0.1`. The launcher says so on stderr when you use `-H` under Apple Containers. See [Accessing Host Services](USAGE.md#accessing-host-services).
- **`~/.claude.json` is relocated**: On first run, your `~/.claude.json` is moved to `~/.claude-contained/.claude.json` and replaced with a symlink. This allows containers to share the file. **If you delete `~/.claude-contained/`, you will lose your Claude account state and some settings.** You will have to log in again.
- **Contained Claude settings are separate**: Contained runs use `~/.claude-contained/claude` as their Claude profile by default and do not mount host `~/.claude/settings.json`. Host Claude extension resources (`skills`, `agents`, `commands`, and `plugins`) and `~/.claude-contained/.claude.json` are still shared.
- **Concurrent contained and uncontained sessions share some state**: Regular and contained Claude use separate settings by default, but they share account state and extension resources. Concurrent writes to those shared files may conflict.
- **Codex and PATH**: Codex runs commands through `bash -lc`, which sources `/etc/profile` and resets PATH to the Debian default. Tools installed outside standard locations must be symlinked into `/usr/local/bin/`. The optional Java layer already provides links for `java`, `javac`, `jar`, `mvn`, and `jbang`.
- **Devcontainer and standalone sessions share a profile**: Do not run the VS Code devcontainer and standalone launchers simultaneously against the same contained Claude profile.

## Quick Start

### Build the launcher

Both runtimes share one Go binary:

```bash
make build                          # bin/claude-contained
make install                        # symlinks it to ~/.local/bin/claude-contained
```

`PREFIX` overrides the install location (default `~/.local`); make sure `$PREFIX/bin` is on your `PATH`.

### Apple Containers (macOS)

1. Build the image:

   ```bash
   container build --platform linux/arm64 -t claude-contained .
   ```

   Java/IntelliJ tooling is included by default. For a smaller image without it:

   ```bash
   container build --platform linux/arm64 --build-arg INCLUDE_JAVA_LAYER=false -t claude-contained .
   ```

2. Optionally set up aliases:

   ```bash
   alias claude='claude-contained --yolo'
   alias codex='claude-contained -t codex --yolo'
   alias copilot='claude-contained -t copilot --yolo'
   alias gemini='claude-contained -t gemini --yolo'
   alias vibe='claude-contained -t vibe --yolo'
   ```

3. Run it:

   ```bash
   claude-contained                 # Current directory
   claude-contained -C ./my-project # Specific project directory
   ```

   macOS selects Apple Containers by default; no flag needed.

### Docker

1. Build the image:

   ```bash
   docker build --platform linux/arm64 -t claude-contained .
   ```

   Add `--build-arg INCLUDE_JAVA_LAYER=false` for the smaller image.

2. Select the Docker runtime:

   ```bash
   claude-contained --container-runtime=docker                 # Current directory
   claude-contained --container-runtime=docker -C ./my-project # Specific project directory
   ```

   Or set `CLAUDE_CONTAINED_RUNTIME=docker` once instead of passing the flag every time. On a non-macOS host, Docker is already the default and neither is required. A `claude-docked` symlink to the installed binary (`ln -s claude-contained ~/.local/bin/claude-docked`) also selects Docker, for anyone who had one from before the two launchers were unified.

See [USAGE.md](USAGE.md) for the complete CLI reference and operational guides.

## Supported Tools

| Tool | Command | Yolo flag | Persistent config |
|------|---------|-----------|-------------------|
| [Claude Code](https://claude.ai/code) | `claude` | `--dangerously-skip-permissions` | `~/.claude-contained/claude` mounted as `~/.claude` |
| [OpenAI Codex](https://github.com/openai/codex) | `codex` | `--yolo` | `~/.codex` |
| GitHub Copilot CLI | `copilot` | `--yolo` | `~/.copilot` |
| [Google Gemini CLI](https://github.com/google-gemini/gemini-cli) | `gemini` | `--yolo` | `~/.gemini` |
| [Mistral Vibe](https://github.com/mistralai/mistral-vibe) | `vibe` | `--auto-approve` | `~/.vibe` |

The contained Claude profile and the other tools' config directories are bind-mounted regardless of which tool you run.

## Container Design

- **Path parity**: The project directory, extra mounts, and the host HOME path appear at the same absolute paths inside the container.
- **UID/GID parity**: The container user adopts the host user's IDs so files created in mounted directories keep useful ownership.
- **Persistent tool state**: Tool profiles and selected caches live on the host while tool processes run inside the container.
- **Runtime choices**: one launcher targets either Apple Containers or Docker, chosen by `--container-runtime`, `CLAUDE_CONTAINED_RUNTIME`, or the host platform, while exposing the same CLI behavior.
- **Defense in depth**: The container or VM is the isolation boundary. The included sandbox runtime adds a deny-by-default network guardrail around the tool process.
- **Host services**: Containers use `host.local` for reachable host services; Docker can additionally forward localhost-bound services with `-H`.
- **Image files**: Files under `image/` are copied into the image and kept out of the Dockerfile so the Dockerfile stays below Apple Containers' 16k file limit.
- **Project toolchains**: a project can check in a tooling layer Dockerfile that the launcher builds on top of the base image and runs in its place. See [Tooling Layers](USAGE.md#tooling-layers).

For implementation details and architectural constraints, see [CONTRIBUTING.md](CONTRIBUTING.md) and the decisions under [`docs/adr/`](docs/adr/).
