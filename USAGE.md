# Usage

This guide covers the complete `claude-contained` command-line interface and its runtime behavior. See [README.md](README.md) for installation and the project overview.

## Command Line

```text
claude-contained [options] [-- <tool args...>]
```

There are no positional arguments. Use `-C` for the project directory, `-m` for extra mounts, and the first `--` to pass all remaining arguments to the selected tool verbatim.

### Options

| Flag | Description |
|------|-------------|
| `-C`, `--dir DIR` | Project directory (default: current directory) |
| `-m`, `--mount DIR[:ro\|:rw]` | Mount an extra directory at the same path (repeatable) |
| `-t`, `--tool TOOL` | Run `claude` (default), `codex`, `copilot`, `gemini`, or `vibe` |
| `-R`, `--rebuild[=MODE]` | Rebuild and exit: `tools` (default) or `full` |
| `-H PORT[:HOSTPORT]` | Forward a host port to container localhost (repeatable) |
| `-p HOST:CONTAINER` | Publish a container port to the host (repeatable) |
| `-e`, `--env KEY=VALUE` | Set a variable for the tool process (repeatable) |
| `--no-project-env` | Ignore the project's `.claude-contained/env` file |
| `--dns IP` | Use an IP as a DNS resolver (repeatable; Apple default: `1.1.1.1`) |
| `--allow-host HOST` | Allow sandbox egress to a host (repeatable) |
| `--no-sandbox` | Disable the srt sandbox for this run |
| `--container-runtime NAME` | Container runtime: `apple` or `docker` |
| `--build-context DIR` | Checkout holding the Dockerfile for `--rebuild` |
| `--layer DIR` | Tooling layer directory (default: the project's `.claude-contained/layer`) |
| `--build-layer` | Build the tooling layer without confirming |
| `--no-layer` | Ignore the tooling layer and run the base image |
| `--log-level LEVEL` | Diagnostic detail: `debug`, `info`, `warn`, `error`, or `off` (default) |
| `--log-file PATH` | Write the diagnostic stream to a secured, truncated file |
| `--log-only` | Carry user-facing output on the diagnostic stream |
| `-s`, `--shell` | Start a bash shell instead of the selected tool |
| `-S`, `--ssh` | Enable SSH agent forwarding |
| `-w`, `--worktree` | Include a Git worktree's main repository without prompting |
| `-W`, `--lock-worktrees` | Auto-lock hidden linked worktrees without prompting |
| `-y`, `--yolo` | Skip permission prompts using the selected tool's flag |
| `-N`, `--contained-node-modules` | Use container-specific `node_modules` without prompting |
| `--share-skills DIR` | Mount shared skill folders from an absolute path read-only |
| `--share-host-claude` | Mount the host Claude profile directly instead of using the contained profile |
| `--readonly-extras` | Default all extra mounts to read-only; a per-mount suffix wins |
| `-a`, `--attach [NAME]` | Attach to a running container; error if none matches |
| `--name NAME` | Name a new container; mutually exclusive with `--attach` |
| `--zellij` | Run the tool inside a persistent Zellij workspace |
| `--session NAME` | With `--zellij`, select the session to start or attach |
| `--new-session` | With `--zellij`, start even when another session is live |
| `-h`, `--help` | Show the launcher help |

The launcher's `--help` output is authoritative for the installed version.

### Behavior

- The project directory and every extra mount appear at their original absolute host paths.
- Extra mounts are passed to Claude and Codex with `--add-dir`.
- Append `:ro` or `:rw` to an extra mount to override its access. The project directory cannot be read-only.
- Claude uses `~/.claude-contained/claude` as its contained profile by default. The host's `~/.claude/settings.json` is not mounted or copied there.
- Claude account state remains shared through `~/.claude-contained/.claude.json`.
- Host Claude extension resources are shared from `~/.claude/{skills,agents,commands,plugins}` into the contained profile.
- `--share-host-claude` or `CLAUDE_CONTAINED_SHARE_HOST_CLAUDE=1` restores the legacy behavior of mounting host `~/.claude` directly.
- Other tool configs (`~/.codex`, `~/.copilot`, `~/.gemini`, and `~/.vibe`), the Maven cache, and Vaadin state are bind-mounted for persistence.
- SSH agent forwarding is disabled by default. Enable it with `-S` or `--ssh`.
- Git worktrees are detected and the main repository's Git metadata is included for full Git access.

`--share-skills DIR` mounts the directory read-only as each tool's skills directory. The directory and symlink targets under it are also mounted read-only at path-parity locations so absolute symlinks continue to resolve. For Codex, the host's `~/.codex/skills/.system` is mounted back over `DIR/.system` so built-in skills remain visible. Supply an absolute path; the launcher does not expand `~`.

If mounted Git metadata can see linked worktrees outside the mounted directories, the launcher offers to auto-lock them while the container runs. This prevents an in-container `git worktree prune` or `git gc` from removing worktrees it cannot see. `-W` accepts the offer without prompting. Locks owned by the launcher are released after the last owning container exits, and stale launcher locks are reclaimed on a later run.

### Diagnostic Stream

Contributor-facing diagnostics are opt-in and use a separate stream from the launcher's user-facing prose. Choose `debug`, `info`, `warn`, `error`, or `off` with `--log-level`; the exact lowercase spelling is required. Resolution order is an explicit `--log-level`, then `CLAUDE_CONTAINED_LOG_LEVEL`, then the `info` implied by `--log-only`, then `off`. An explicit `off` prevents the implication. `--log-file` alone does not enable diagnostic records.

By default, enabled diagnostics use stderr. `--log-file PATH` instead creates or truncates PATH after narrowing it to mode `0600`; a setup failure exits 2 and never falls back to stderr. `--log-only` carries normal stdout and stderr lines as `kind=output stream=stdout|stderr` records to the same destination. These relocated output records are never level-filtered, so user-facing warnings survive even with `--log-level=error`. Help remains raw on stdout, and interactive prompt text remains raw on the terminal.

Attach and Zellij attach normally replace the launcher process. With `--log-only`, the launcher instead proxies that command as a child so its later stdout and stderr can continue through the diagnostic stream; the child exit status is preserved.

Launcher-generated records use `kind=diagnostic` and exactly one component from `cli`, `host`, `env`, `plan`, `runtime`, `worktree`, `zellij`, `attach`, `rebuild`, or `layer`. They never include environment assignment values or the value of `AI_GH_TOKEN`; rendered runtime arguments replace every `-e` operand with a redacted form at every level. Paths, mount information, and non-`-e` tool arguments can remain visible, and the redacted argv is not a pasteable reproduction of the real command.

Relocated output has a different security boundary: it is existing launcher, runtime, or child-process output carried verbatim and can contain arbitrary sensitive text. Mode `0600` limits file access but does not make a diagnostic file safe to share. If writing or flushing the stream fails, process replacement is blocked and a successful launcher result becomes a failure; an already nonzero primary result remains primary.

## Examples

```bash
# Tool selection
claude-contained                             # Claude (default)
claude-contained -t codex                    # OpenAI Codex
claude-contained -t copilot                  # GitHub Copilot CLI
claude-contained -t gemini                   # Google Gemini CLI
claude-contained -t vibe                     # Mistral Vibe

# Projects and mounts
claude-contained -C ~/code/my-app
claude-contained -m ../other/project
claude-contained -m ../lib:ro
claude-contained --readonly-extras -m ../a -m ../b

# Tool arguments and permissions
claude-contained -- --model sonnet --verbose
claude-contained -y -t codex

# Debugging and persistence
claude-contained -s
claude-contained -S
claude-contained --log-level=debug
claude-contained --log-level=debug --log-file ./launcher.log
claude-contained --share-skills /Users/me/Projects/skills
claude-contained --share-host-claude

# Environment and networking
claude-contained -e API_URL=http://host.local:8080
claude-contained -e 'GREETING=hello world' -e DEBUG=1
claude-contained -p 8080:8080
claude-contained -H 3845
claude-contained --allow-host example.com
claude-contained --no-sandbox -s

# Rebuilds
claude-contained --rebuild
claude-contained --rebuild=full

# Containers
claude-contained -a
claude-contained -a myproject
claude-contained --name myproject

# Zellij workspaces
claude-contained --zellij
claude-contained --zellij --attach
claude-contained --zellij --attach --session review
claude-contained --zellij --new-session --session review
claude-contained --zellij --shell
```

Every flag above works the same way regardless of which container runtime is selected, unless a section below calls out a runtime difference. The runtime is chosen in this order:

| Source | Value | Result |
|---|---|---|
| `--container-runtime` | `apple` / `docker` (case-insensitive) | that runtime |
| `CLAUDE_CONTAINED_RUNTIME` | same | that runtime |
| `argv[0]` basename | contains `dock` | Docker (compat affordance, e.g. a `claude-docked` symlink) |
| `argv[0]` basename | anything else | not a selection — falls through |
| host platform | `darwin` | Apple Containers |
| host platform | anything else | Docker |

`--container-runtime=apple` on a non-macOS host exits 2 with a message on stderr, since Apple Containers has no non-macOS implementation.

## Updating

The launcher does not check for updates. To pick up a new version:

```bash
git -C /path/to/claude-contained pull
make -C /path/to/claude-contained build
```

Then rebuild the image if the change touched the Dockerfile or anything under `image/`:

```bash
claude-contained --rebuild=full
```

A `git pull` alone only updates the checkout's sources; it neither rebuilds the compiled Go binary nor the container image.

## Rebuilding the Image

Use a launcher to refresh its image and exit:

```bash
claude-contained --rebuild                                # Refresh AI CLI layers
claude-contained --rebuild=full                            # Pull and rebuild everything without cache
claude-contained --container-runtime=docker --rebuild
claude-contained --container-runtime=docker --rebuild=full
```

The default `tools` rebuild refreshes the AI CLI portion of the image and the layers after it. If the targeted rebuild fails, the launcher automatically retries with a full rebuild.

`full` pulls the latest base image and rebuilds everything without cache.

Rebuilding needs to find the checkout that holds the Dockerfile. It resolves in this order: `--build-context DIR`, then `CLAUDE_CONTAINED_BUILD_CONTEXT=DIR`, then this executable's own directory if that holds a `Dockerfile`, then the Git repository enclosing it if *that* root holds one — which is what makes a repo-adjacent install or a symlink into the checkout keep working with no flag at all. A symlinked `make install` (the default) keeps this working with no flag; a *copy* of the binary outside the checkout has no enclosing checkout and needs `--build-context` or `CLAUDE_CONTAINED_BUILD_CONTEXT` on every rebuild.

### Optional Java Layer

JBR, HotswapAgent, jdtls, Maven, and JBang are included with `--build arg INCLUDE_JAVA_LAYER=true`.

## Tooling Layers

A project can add its own toolchain to the container by checking in a **tooling layer**: a complete Dockerfile built on top of the base image. The launcher builds it into a **derived image** and runs that instead of `claude-contained:latest`. A project with no layer behaves exactly as it always has.

The layer lives in `.claude-contained/layer/` inside the project directory. That directory is both the layer's home and its build context. Override it with `--layer DIR`, else `CLAUDE_CONTAINED_LAYER=DIR`; the default applies when neither is set.

### The contract

A layer is a whole Dockerfile, not a snippet. It must start with this preamble:

```dockerfile
ARG BASE_IMAGE=claude-contained:latest
FROM ${BASE_IMAGE}
RUN ...
```

The launcher overrides `BASE_IMAGE` with the base image's resolved ID, so the image built is the image that was hashed. The default in the file is what keeps the layer buildable by hand — `cd .claude-contained/layer && docker build -t my-layer .` — and inside a devcontainer that never runs the launcher. A layer that omits the `ARG` still builds, but draws an unconsumed-build-argument warning from the builder.

Nothing validates the layer's contents. It can break the base image's invariants, and the container belongs to the project, so examples and this documentation carry that weight rather than a checker.

### Identity and staleness

The derived image is tagged `claude-contained-layer:<project>-<hash>`, where the hash covers the base image's resolved ID, the layer Dockerfile, and every file in the layer directory. The tag *is* the staleness check: if that image exists the launcher runs it, and if it does not the launcher offers to build it. There is no state file and no explicit build step.

Consequences worth knowing:

- An unchanged layer never rebuilds; a changed one always does.
- `--rebuild` invalidates every derived image, because the base image ID they were named after no longer exists. It never builds a layer itself — the next ordinary run does.
- Switching container runtimes rebuilds the derived image: the two report different identities for the same base image.
- Everything in the layer directory is hashed and no `.dockerignore` is interpreted, so a large layer directory makes every run slower. The launcher warns rather than refusing, because the directory is writable from inside the container and a refusal would let a contained agent disable its own toolchain.
- File modes are hashed the way Git tracks them — the execute bit and nothing else — so a checked-out layer hashes identically regardless of umask. `chmod +x` invalidates; `chmod 0640` does not.

### Confirmation

Building a layer makes the host's container runtime execute arbitrary steps with unrestricted network egress, so every build is confirmed:

```text
Tooling layer found: /path/to/project/.claude-contained/layer/Dockerfile
It has not been built for the current base image. Building runs its
instructions on this host with unrestricted network access.
Build the tooling layer for this project? [y/N]
```

Unlike the launcher's other prompts, this one defaults to **no**. Nothing is remembered between runs; the prompt appears only when the tag is missing, which is once per actual change.

`--build-layer` answers it ahead of time and builds. `--no-layer` ignores the layer and runs the base image. Neither has an environment variable, deliberately: an exported variable is a stored approval that defeats the confirmation, and a forgotten `CLAUDE_CONTAINED_NO_LAYER` would silently produce a container missing its toolchain. `--no-layer` cannot be combined with `--layer` or `--build-layer`.

With no terminal to confirm on and an unbuilt layer, the launcher exits nonzero and names both flags rather than building unattended.

### Failure

A failed layer build is a hard error carrying the builder's own exit status. The launcher never falls back to the base image, because that would start a container that looks healthy while missing its toolchain.

A missing base image is reported rather than built:

```text
error: the base image claude-contained:latest is not built.
       Run 'claude-contained --rebuild=full' first; a tooling layer builds on top of it.
```

A layer directory named by `--layer` or `CLAUDE_CONTAINED_LAYER` that holds no `Dockerfile` is a usage error. The *default* directory holding no `Dockerfile` is simply "no layer" — nobody named it — and is silent on the terminal; `--log-level=debug` reports it as `tooling layer absent` with `reason=no-dockerfile`.

### Cleanup

Derived images accumulate at roughly a gigabyte each, one per project per layer version per base version, and `image prune` does not remove tagged images. Cleanup is manual by design: a launcher that deleted images it cannot prove are unused would be worse than disk growth.

The tag is the handle on both runtimes:

```bash
docker image ls claude-contained-layer
docker image rm claude-contained-layer:my-app-0123456789abcdef0123456789abcdef

container image list
container image delete claude-contained-layer:my-app-0123456789abcdef0123456789abcdef
```

Images built by the Docker runtime additionally carry `claude-contained.layer`, `claude-contained.layer.project`, `claude-contained.layer.dockerfile` and `claude-contained.layer.base` labels, visible with `docker image inspect`. Images built by Apple Containers do not; the labels are provenance for a human and are never read by the launcher.

## Zellij Workspaces

`--zellij` makes Zellij the top-level container process. The initial pane runs the same tool or shell the launcher would otherwise start. The entrypoint still wraps Zellij in the srt sandbox unless `--no-sandbox` is set, so child panes inherit the same network policy.

Session names are either explicit with `--session` or generated as `cc-{sanitized-project}-{path-hash}`. Explicit names may use letters, numbers, `_`, `.`, and `-`, and cannot start with `-`. `--new-session` is a force flag and takes no value.

Attach behavior is strict:

- `--zellij --attach [--session NAME]` attaches only to a live Zellij-backed container and never creates a replacement.
- Bare `--zellij --attach` connects directly when exactly one Zellij session is live and otherwise prompts.
- `--zellij --attach --shell` is invalid because attach reconnects to the existing workspace as-is.
- `--env` is invalid with `--zellij --attach`; an existing pane keeps the environment it was created with.

Zellij cache and data persist under `~/.claude-contained/zellij/`. Runtime sockets stay inside the container under `/tmp`, so stale host sockets are not reused across container lifetimes. Detaching leaves the container running until that session is killed.

If the launcher reports `zellij-run: command not found`, the launcher is newer than the local image. Run a full rebuild once and retry.

If Claude reports invalid project entries such as an empty `.mcp.json`, check that the file is non-empty valid JSON. On Linux, an interrupted sandbox can leave zero-byte protected placeholders; the launchers remove untracked placeholders before startup and after exit.

## Node.js Projects

macOS and the Linux container use different operating-system ABIs. A host `node_modules` can therefore contain native binaries that do not run inside the container, even when both systems use the same CPU architecture.

When the project directory contains `package.json`, the launchers offer to create a container-specific directory:

```text
Node.js project detected. Use container-specific node_modules? [Y/n]
```

If accepted, `.claude-contained/node_modules-linux-aarch64/` (or the matching x86_64 directory) is mounted over `node_modules` inside the container.

On the first run, install the Linux dependencies inside the container:

```bash
npm install # or yarn, pnpm, bun, and so on
```

The overlay persists on the host for later sessions. Use `-N` or `--contained-node-modules` to accept without prompting.

Add `.claude-contained/` to the project's `.gitignore` yourself. The launcher does not do this automatically. The prompt is skipped on Linux hosts and when the project has no `package.json`.

## DNS

Apple Containers often configures both the builder and runtime container to use an unreachable vmnet gateway resolver. The typical symptom is a name-resolution failure such as `Temporary failure resolving 'deb.debian.org'` while host networking still works.

This is distinct from network isolation: routing can work while DNS resolution fails.

### Build-Time DNS

Recreate the Apple Containers builder with a working resolver:

```bash
container builder stop
container builder delete
container builder start --dns 1.1.1.1
```

The builder retains this setting for later builds.

### Runtime DNS

`claude-contained` uses `1.1.1.1` by default. Override it for one run:

```bash
claude-contained --dns 1.1.1.1
```

Set a per-user resolver list with:

```bash
export CLAUDE_DNS=1.1.1.1,8.8.8.8
```

An explicit `--dns` overrides `CLAUDE_DNS`. Set `CLAUDE_DNS=system` or `CLAUDE_DNS=none` to use the container runtime's default resolver.

The Docker runtime keeps Docker's own resolver by default but supports the same explicit flag and environment override.

If DNS still fails, check whether a local resolver, VPN, or iCloud Private Relay is holding UDP port 53:

```bash
sudo lsof -nP -iUDP:53
```

See [apple/container#402](https://github.com/apple/container/issues/402) for related Apple Containers behavior.

## Environment Variables

Pass variables to the tool process with repeatable `-e` or `--env` flags:

```bash
claude-contained -e API_URL=http://host.local:8080 -e 'GREETING=hello world'
```

For project-specific values, create `.claude-contained/env` under the project directory:

```text
# Comments and blank lines are allowed
API_URL=http://host.local:8080
GREETING="hello world"
```

One pair of surrounding quotes is stripped. The file is parsed literally and never sourced. Flag values override the project env file, and the project file overrides built-in `TZ` and `GH_TOKEN` values. Use `--no-project-env` to ignore the file. Loaded key names are printed at startup, but values are not.

Environment changes do not apply when attaching to an existing container. `--env` is refused with `--zellij --attach`, where the existing pane retains its creation environment.

### Refused Variables

The launchers reject variables that could subvert container setup:

- `HOME`, `PATH`, `JAVA_HOME`, `GIT_PROTECT_DIRS`, `STAY_ROOT`, and `SSH_AUTH_SOCK`
- Names starting with `HOST_`, `SRT_`, or `CLAUDE_CONTAINED_`

Use `--ssh` instead of setting `SSH_AUTH_SOCK`. `LD_PRELOAD`, `LD_LIBRARY_PATH`, and `NODE_OPTIONS` are accepted as command-line flags but refused from the project env file.

The `CLAUDE_CONTAINED_` prefix is reserved because the launcher reads it for its own configuration: `CLAUDE_CONTAINED_RUNTIME` (see the runtime selection table above), `CLAUDE_CONTAINED_BUILD_CONTEXT` (see [Rebuilding the Image](#rebuilding-the-image)), `CLAUDE_CONTAINED_LAYER` (see [Tooling Layers](#tooling-layers)), `CLAUDE_CONTAINED_LOG_LEVEL` (see [Diagnostic Stream](#diagnostic-stream)), and `CLAUDE_CONTAINED_SHARE_HOST_CLAUDE`. These are host-side settings; the refusal above is what stops a contained agent from setting them for the next run through the project env file.

### Security Considerations

The project env file is not a security boundary. The project directory is writable from inside the container, so a contained agent can edit the file and affect the next launch. Use `--env` for security-sensitive values and `--no-project-env` with an untrusted checkout.

The launcher does not gitignore `.claude-contained/` for you, so an env file containing a token can be committed accidentally. Flag values are also visible to host process inspection and container inspection. This feature passes values into the container; it does not conceal secrets.

The VS Code devcontainer does not use the launchers. Configure its environment through `containerEnv` in `devcontainer.json`.

## Sandboxing

The tool process runs under [`@anthropic-ai/sandbox-runtime`](https://github.com/anthropic-experimental/sandbox-runtime) (srt), which provides a deny-by-default egress allowlist. It uses HTTP and SOCKS5 proxies, so enforcement is not limited to programs that honor `HTTPS_PROXY`.

The image includes the Linux dependencies required by srt.

### Security Boundary

Understand the limits before relying on it:

- `container exec` and `docker exec` bypass the entrypoint. Commands started that way are unsandboxed unless routed through `srt-run`; launcher attach flows do this automatically.
- Inside a container, srt uses `enableWeakerNestedSandbox`. Treat it as a guardrail against accidental or wandering access, not containment for actively hostile code.
- The Apple Container VM or Docker container remains the primary isolation boundary.
- A host user invoking the container runtime directly is outside this boundary.

### Allowing Hosts

The default allowlist covers the provider APIs, OAuth endpoints, package registries, and GitHub needed by the bundled tools. Extend it for one run:

```bash
claude-contained --allow-host example.com --allow-host '*.internal.dev'
```

For persistent settings, create `~/.claude-contained/srt-settings.json`:

```json
{
  "network": {
    "allowedDomains": ["corp.example.com", "*.internal.dev"],
    "deniedDomains": ["telemetry.example.com"]
  }
}
```

Default domains, file domains, and command-line domains are combined. Other srt settings in the file are passed through.

The filesystem section is generated for every run because mounted directories change and srt matches paths literally on Linux. The merged policy is written to `/run/srt-settings.json` as a root-owned, read-only file so the contained process cannot rewrite its own allowlist.

### Debugging

Compare a failing run with the sandbox disabled:

```bash
claude-contained --no-sandbox
claude-contained --no-sandbox -s
```

`--no-sandbox` and `--shell` are independent, so `-s` alone opens a sandboxed shell. Inside the container, `srt --debug` reports blocked access and `/run/srt-settings.json` shows the effective policy.

A blocked connection can surface as `Socket is closed` because the proxy closes refused connections. Add the required `--allow-host` rule or disable the sandbox deliberately while diagnosing.

To sandbox a command started with runtime `exec`:

```bash
container exec -it -u dev <container-name> srt-run claude
```

If policy generation fails, the entrypoint refuses to start rather than silently running unsandboxed.

## Claude Code Clipboard Behavior

The image sets Claude Code's managed `tui` setting to `default`, which keeps the classic inline renderer. The fullscreen renderer relies on terminal OSC 52 clipboard support and captures the mouse; in containerized terminals where OSC 52 is dropped, that can prevent both copy-on-select and normal terminal text selection.

This setting is container-scoped at `/etc/claude-code/managed-settings.json` and does not modify the host's `~/.claude/settings.json`.

## Accessing Host Services

Container `localhost` refers to the container, not the host. Use `host.local` for reachable host services or Docker's `-H` forwarding.

### Docker

Docker Desktop can route to services bound to host `127.0.0.1`. Forward one or more ports:

```bash
claude-contained --container-runtime=docker -H 3845
claude-contained --container-runtime=docker -H 3845 -H 8080
```

### Apple Containers

Apple Containers can reach host services bound to `0.0.0.0`, but not services bound only to `127.0.0.1`. Use `host.local` for services listening on all interfaces. The `-H` flag cannot bridge localhost-only services such as the Figma Desktop MCP.

`-H` still works here for services listening on `0.0.0.0`, so it is not refused — but the launcher warns on stderr that localhost-only services are out of reach. For those, select the Docker runtime (`--container-runtime=docker` or `CLAUDE_CONTAINED_RUNTIME=docker`).

See [apple/container#346](https://github.com/apple/container/issues/346) for the relevant host-routing feature request.

### Figma Desktop MCP

Figma Desktop MCP listens on localhost port 3845. Enable the MCP server in Figma Desktop, keep the app running, and use:

```bash
claude-contained --container-runtime=docker -H 3845
```

For other localhost-only MCPs, use `claude-contained --container-runtime=docker -H PORT`.

For a service listening on all host interfaces, configure its client to use `host.local`:

```json
{
  "mcpServers": {
    "my-mcp": {
      "type": "http",
      "url": "http://host.local:PORT/mcp"
    }
  }
}
```

## VS Code Devcontainer

The included template uses the same image for Java, Spring, and Vaadin development with Claude available in the integrated terminal.

1. Build the Docker image:

   ```bash
   docker build -t claude-contained .
   ```

2. Copy `devcontainer/` into the target project as `.devcontainer/`.
3. Open the project in VS Code and choose **Reopen in Container**.

The template preserves host path parity and shares the Maven cache and Git configuration. The image must be built first. Do not run the devcontainer and standalone launchers simultaneously against the same contained Claude profile. VS Code manages networking separately, so use its explicit port forwarding when `host.local` is unavailable.

See [devcontainer/README.md](devcontainer/README.md) for the full template guide.
