# AI Contained

Seamlessly run CLI coding agents (Claude, Codex, Gemini, Vibe) inside an [Apple Container](https://github.com/apple/container) or [Docker](https://www.docker.com) container with persistent state. 

The main goal is to provide a seamless experience; `alias claude='claude-contained --yolo'` and now `claude` runs in a container with a contained Claude profile. Only `.` or the folders you specify are shared with the container.
Login/account state and common Claude extension resources are retained even if you switch back to un-contained `claude`.

There are some caveats:

- **Host localhost access**: `-H PORT` works with `claude-docked` (Docker) but not `claude-contained` (Apple Containers) for services bound to localhost. See [Accessing Host Services](#accessing-host-services). (Apple Containers seems to be gaining support soon.)
- **`~/.claude.json` is relocated**: On first run, your `~/.claude.json` is moved to `~/.claude-contained/.claude.json` and replaced with a symlink. This allows containers to share the file. **If you delete `~/.claude-contained/`, you will lose your Claude credentials and some settings.** You'll have to log in again. This is a limitation on how files can be shared with the container. 
- **Contained Claude settings are separate**: By default, contained runs use `~/.claude-contained/claude` as their Claude profile and do not mount host `~/.claude/settings.json`. Host Claude extension resources (`skills`, `agents`, `commands`, `plugins`) and `~/.claude-contained/.claude.json` are still shared.
- **Be careful mixing contained and uncontained at the same time**: Regular `claude` and contained Claude use separate settings by default, but they still share account state and extension resources. Concurrent writes to those shared files may conflict.
- **Codex and PATH**: Codex runs commands via `bash -lc`, which sources `/etc/profile` and resets PATH to the Debian default. This means tools installed outside standard locations (e.g., via SDKMAN) won't be found unless symlinked into `/usr/local/bin/`. When the Java layer is included, the image has symlinks for `java`, `javac`, `jar`, `mvn`, and `jbang`. If you install additional tools in non-standard paths, add similar symlinks in the Dockerfile.

## Quick Start

### Apple Containers (macOS)

1. Build the container:
   ```bash
   container build --platform linux/arm64 -t claude-contained .
   ```
   Java/IntelliJ (JBR, HotswapAgent, jdtls, Maven, JBang) is included by default. If you don't need Java, skip it for a smaller, faster build:
   ```bash
   container build --platform linux/arm64 --build-arg INCLUDE_JAVA_LAYER=false -t claude-contained .
   ```

2. Put `claude-contained` somewhere on your PATH, optionally aliasing to `claude`.
   ```
   alias claude='claude-contained --yolo'
   alias vibe='claude-contained -t vibe --yolo'
   alias codex='claude-contained -t codex --yolo'
   alias gemini='claude-contained -t gemini --yolo'
   ```

4. Run:
   ```bash
   claude-contained              # Current directory
   claude-contained ./my-project # Specific directory
   ```

### Docker

1. Build the container:
   ```bash
   docker build --platform linux/arm64 -t claude-contained .
   ```
   Skip the Java/IntelliJ layer for a smaller, faster build with `--build-arg INCLUDE_JAVA_LAYER=false`.

2. Put `claude-docked` somewhere on your PATH.

3. Run:
   ```bash
   claude-docked              # Current directory
   claude-docked ./my-project # Specific directory
   ```

## Usage

```
claude-contained [options] [main_dir] [extra_dir ...] [-- <tool args...>]
```

### Options

| Flag | Description |
|------|-------------|
| `-t`, `--tool TOOL` | AI tool to run: `claude` (default), `codex`, `gemini`, `vibe` |
| `-R`, `--rebuild[=MODE]` | Rebuild image before run: `tools` (default) or `full` |
| `-H PORT[:HOSTPORT]` | Forward host port to container localhost (can be repeated) |
| `-p HOST:CONTAINER` | Publish container port to host (can be repeated) |
| `-e`, `--env KEY=VALUE` | Set an env var for the tool process (can be repeated). See [Environment Variables](#environment-variables) |
| `--no-project-env` | Ignore the project's `.claude-contained/env` file |
| `--dns IP` | Use `IP` as a DNS resolver (can be repeated). See [DNS](#dns) |
| `--allow-host HOST` | Allow sandbox egress to `HOST` (can be repeated). See [Sandboxing](#sandboxing) |
| `--no-sandbox` | Disable the srt sandbox for this run (debugging escape hatch) |
| `-s`, `--shell` | Start a bash shell instead of the AI tool (for debugging) |
| `-S`, `--ssh` | Enable SSH agent forwarding (for git push) |
| `-w`, `--worktree` | Auto-include git worktree's main repository (skip prompt) |
| `-y`, `--yolo` | Skip all permission prompts (tool-specific flag) |
| `-N`, `--contained-node-modules` | Use container-specific node_modules (skip prompt) |
| `--share-skills=DIR` | Mount shared skill folders from `DIR` (opt-in, no default; use a full path) |
| `--share-host-claude` | Compatibility mode: mount host `~/.claude` directly as container `~/.claude` |
| `-a`, `--attach [NAME]` | Attach to running container (runs tool, or bash with `-s`) |
| `--zellij` | Run the tool inside a persistent Zellij workspace |
| `--new-session[=NAME]` | With `--zellij`, start the target session even when another Zellij container is live |
| `-h`, `--help` | Show help message |

### Supported Tools

| Tool | Command | Yolo Flag | Config Dir |
|------|---------|-----------|------------|
| [Claude Code](https://claude.ai/code) | `claude` | `--dangerously-skip-permissions` | `~/.claude-contained/claude` mounted as `~/.claude` |
| [OpenAI Codex](https://github.com/openai/codex) | `codex` | `--yolo` | `~/.codex` |
| [Google Gemini CLI](https://github.com/google-gemini/gemini-cli) | `gemini` | `--yolo` | `~/.gemini` |
| [Mistral Vibe](https://github.com/mistralai/mistral-vibe) | `vibe` | `--auto-approve` | `~/.vibe` |

The contained Claude profile and the other tools' config directories are bind-mounted regardless of which tool you run.

### Behavior

- First directory is the working directory
- Additional directories are mounted and auto-added via `--add-dir` (Claude and Codex only)
- Append `:ro` to an extra dir to mount it read-only (or `:rw` to force read-write); use `--readonly-extras` to default all extras to read-only
- Claude uses `~/.claude-contained/claude` as its contained profile by default. Host `~/.claude/settings.json` is not mounted or copied into that profile.
- Claude account state remains shared through `~/.claude-contained/.claude.json`.
- Host Claude extension resources are shared from `~/.claude/{skills,agents,commands,plugins}` into the contained profile.
- Use `--share-host-claude` or `CLAUDE_CONTAINED_SHARE_HOST_CLAUDE=1` to restore the legacy behavior of mounting host `~/.claude` directly.
- Other tool configs and Maven cache (`~/.m2`) are bind-mounted for persistence
- `--share-skills=DIR` mounts `DIR` as each tool's skills directory: `~/.claude/skills`, `~/.codex/skills`, `~/.agents/skills`, and `~/.<tool>/skills` for Copilot, Gemini, and Vibe. For Codex, the host's `~/.codex/skills/.system` is mounted back over `DIR/.system` so built-in skills remain visible while new installs write to `DIR`. Use a full path; `~` is not expanded by the launcher.
- SSH agent forwarding is disabled by default; use `-S`/`--ssh` to enable
- Git worktrees are detected; main repository is included for full git access
- If a mounted main repository has linked worktrees outside the mounted directories, the launcher offers to auto-lock those worktrees while the container runs (otherwise an in-container `git worktree prune`/`git gc` could remove them). Auto-lock reasons use `cc-autolocked-by:` and are removed when the last owning container exits. The locking is self-healing — a lock left behind by a launcher that was killed is reclaimed automatically by the next run — and fail-safe, applying the locks even if the internal mutex is unavailable so the container never runs with worktrees unprotected.
- `--zellij` starts the selected tool, or `bash` with `--shell`, inside a named Zellij session. The default session is `cc-{project}-{path-hash}`. Zellij data lives in the Zellij session store at `~/.claude-contained/zellij/`; live sockets stay inside the container under `/tmp`.
- Detaching from Zellij keeps the container running until that Zellij session is killed. Plain `--zellij` refuses when any Zellij-backed container is already live; use `--zellij --attach [NAME]` to reconnect or `--zellij --new-session[=NAME]` to start another session.

### Examples

```bash
# Tool selection
claude-contained                                    # Claude (default)
claude-contained -t codex .                         # OpenAI Codex
claude-contained -t gemini .                        # Google Gemini CLI
claude-contained -t vibe .                          # Mistral Vibe

# Common usage
claude-contained . ../other/project                 # Multiple directories
claude-contained . ../lib:ro                        # Mount ../lib read-only
claude-contained --readonly-extras . ../a ../b      # All extras read-only
claude-contained . -- --model sonnet --verbose      # Pass args to tool
claude-contained -y -t codex .                      # Codex with --yolo
claude-contained --rebuild .                        # Refresh AI tools first
claude-contained --rebuild=full .                   # Full fresh rebuild first
claude-contained -s                                 # Debug shell
claude-contained --share-skills=/Users/me/Projects/skills . # Share skills into tool skill dirs
claude-contained --share-host-claude .              # Legacy direct host ~/.claude sharing
claude-contained -e API_URL=http://host.local:8080 .        # Set an env var for the tool
claude-contained -e 'GREETING=hello world' -e DEBUG=1 .     # Repeatable; values may contain spaces

# Zellij workspaces
claude-contained --zellij .                         # Start this project in Zellij
claude-contained --zellij --attach                  # Attach when exactly one Zellij session is live
claude-contained --zellij --attach review           # Attach to a named live Zellij session
claude-contained --zellij --new-session=review .    # Start a named Zellij session
claude-contained --zellij --shell .                 # Start bash as the initial Zellij pane

# Port forwarding
claude-contained -p 8080:8080 .                     # Expose port 8080
claude-contained -H 3845 .                          # Forward host:3845 to container
```

## Rebuilding the Image

Use the launcher when you want the image refreshed before starting a new session:

```bash
claude-contained --rebuild .      # Refresh AI CLI layers
claude-contained --rebuild=full . # Full fresh rebuild (--pull --no-cache)
claude-docked --rebuild .
claude-docked --rebuild=full .
```

`tools` rebuilds the AI CLI portion of the image and everything after it, which updates Claude Code, Codex, Gemini, Vibe, and Copilot without invalidating the entire build. If that targeted rebuild fails, the launcher automatically retries with a full rebuild.

`full` forces a clean rebuild of the entire image and pulls the latest base image. Rebuild requires the launcher script to run from this repo checkout, or via a symlink into it, so it can find the local `Dockerfile`.

## Zellij Workspaces

`--zellij` makes Zellij the top-level process inside the container. The initial pane runs the same command the launcher would normally run: Claude, Codex, Copilot, Gemini, Vibe, or `bash` with `--shell`. The entrypoint still wraps Zellij in the srt sandbox unless `--no-sandbox` is set, so child panes inherit the same network policy.

Session names are either explicit (`--new-session=review`, `--zellij --attach review`) or generated from the project path as `cc-{sanitized-basename}-{8-char-path-hash}`. Explicit names must use only letters, numbers, `_`, `.`, and `-`, and cannot start with `-`. Use `--new-session=NAME` for named sessions; `--new-session NAME` is rejected so it cannot be confused with `main_dir`.

Attach behavior is intentionally strict:

- `--zellij --attach [NAME]` attaches only to a live Zellij-backed container; it never creates a replacement container.
- Bare `--zellij --attach` attaches directly if exactly one Zellij session is live, otherwise it prompts.
- `--zellij --attach --shell` is invalid; attach reconnects to the existing Zellij workspace as-is.

The Zellij session store is `~/.claude-contained/zellij/`. Zellij cache and data persist there, but runtime sockets are pinned to `/tmp/claude-contained-zellij-runtime/` inside the container so stale host sockets are not reused across container lifetimes. A new `--zellij` launch removes saved metadata for that session before creating the initial layout, so stale panes cannot override the current tool command. Under the Linux srt backend, marked Zellij runs set `allowAllUnixSockets` because path-specific Unix socket allowlisting is not available there.

If `--zellij` reports `zellij-run: command not found`, your launcher is newer than the local image. Rebuild once with `claude-contained --rebuild=full` or `claude-docked --rebuild=full`, then retry.

If Claude reports invalid project entries such as `.mcp.json`, check that the file is non-empty valid JSON. On Linux, srt can leave zero-byte protected placeholder files behind when a sandbox is interrupted; the launchers remove those untracked zero-byte placeholders before startup and after exit. When the host lacks `~/.local/bin/claude`, the image creates a native-shaped `~/.local/share/claude/versions/<version>` link so Claude Code does not warn about an unmanaged launcher.

### Optional Java Layer

JBR, HotswapAgent, jdtls, Maven, and JBang are included by default (`INCLUDE_JAVA_LAYER=true`). To build a smaller image without them:

```bash
container build --build-arg INCLUDE_JAVA_LAYER=false -t claude-contained .   # Apple Containers
docker build --build-arg INCLUDE_JAVA_LAYER=false -t claude-contained .      # Docker
```

Build args aren't remembered across rebuilds — pass `--build-arg INCLUDE_JAVA_LAYER=false` again on subsequent `--rebuild=full` runs (or any manual rebuild) if you want to keep Java excluded. Without the Java layer, the `~/.m2` Maven cache mount and the [devcontainer](#vs-code-devcontainer) (which targets Java/Spring/Vaadin development) are not useful.

## Node.js Projects (node_modules Overlay)

When running on macOS, the container is Linux — but `node_modules` often contains platform-specific native binaries (e.g., esbuild, swc, sharp) that are compiled for the host architecture. macOS `arm64` binaries won't work inside a Linux `aarch64` container, even though the CPU architecture is the same, because the OS ABI differs.

To handle this, the scripts automatically detect Node.js projects (directories with a `package.json`) and offer to create a **container-specific `node_modules`** directory:

```
Node.js project detected. Use container-specific node_modules? [Y/n]
```

If accepted, a `.claude-contained/node_modules-linux-aarch64/` directory (or `node_modules-linux-x86_64` on Intel Macs) is created inside your project and mounted over `node_modules` inside the container. You should add `.claude-contained/` to your `.gitignore` manually (this also tells IDEs like IntelliJ and VS Code to skip indexing it). This keeps host and container dependencies separate — each platform gets the correct native binaries.

### First run

After accepting the prompt, run your package manager inside the container to install Linux-native dependencies:

```bash
npm install    # or yarn, pnpm, bun, etc.
```

### Subsequent runs

The overlay directory persists on the host, so dependencies survive across container sessions. No re-install needed unless you change `package.json`.

### Skipping the prompt

Use `-N` (or `--contained-node-modules`) to auto-accept without prompting:

```bash
claude-contained -N .
```

### .gitignore

You should manually add `.claude-contained/` to your project's `.gitignore` to exclude overlay directories from version control.

### When it's skipped

- **Linux hosts**: No overlay needed — host and container share the same OS, so native binaries are already compatible.
- **No `package.json`**: No prompt, no overlay.

## DNS

Apple Containers points both the build and the container at the vmnet gateway
(`192.168.64.1`) for DNS. That resolver is frequently unreachable — the symptom is
anything network-dependent failing to resolve, e.g. `apt-get update` reporting
`Temporary failure resolving 'deb.debian.org'`, while the host's own DNS is fine.

Note this is *not* network isolation. `container network inspect default` shows
`"mode": "nat"`, so outbound routing works; only name resolution is broken.

**At build time**, recreate the builder with a working resolver:

```bash
container builder stop && container builder delete
container builder start --dns 1.1.1.1
```

The builder keeps that setting, so subsequent builds work without further flags.

**At run time**, use `--dns`:

```bash
claude-contained --dns 1.1.1.1 .
```

To avoid typing it every run, set `CLAUDE_DNS` (comma-separated for several):

```bash
export CLAUDE_DNS=1.1.1.1,8.8.8.8
```

An explicit `--dns` flag overrides `CLAUDE_DNS`. Both work with `claude-docked`
too, though Docker usually resolves fine without them.

If DNS still fails, check whether something local is holding port 53
(`sudo lsof -nP -iUDP:53`) — a local resolver, VPN client, or iCloud Private Relay
can break the vmnet resolver. See [apple/container#402](https://github.com/apple/container/issues/402).

## Environment Variables

Pass variables to the tool process with `-e`/`--env`, repeatable:

```bash
claude-contained -e API_URL=http://host.local:8080 -e 'GREETING=hello world' .
```

To avoid retyping them, put `KEY=VALUE` lines in the main directory's
`.claude-contained/env`:

```bash
# .claude-contained/env
API_URL=http://host.local:8080
GREETING="hello world"      # one pair of surrounding quotes is stripped
```

Blank lines and `#` comments are skipped. `--env` beats the file, and the file beats the
`TZ`/`GH_TOKEN` defaults the launcher supplies. `--no-project-env` ignores the file.
Loaded variable names (never values) are printed at startup so you can see what is being
applied. Both flag and file work identically in `claude-docked`.

Not applied when attaching to an existing container — `-a` reuses the environment the
container already has — and `--env` is refused outright with `--zellij --attach`, where
the pane keeps the environment it was created with.

### What is refused

Variables the container itself depends on are rejected with an error rather than silently
ignored: `HOME`, `PATH`, `JAVA_HOME`, `GIT_PROTECT_DIRS`, `STAY_ROOT`, `SSH_AUTH_SOCK`
(use `--ssh`), and anything starting with `HOST_`, `SRT_`, or `CLAUDE_CONTAINED_`.
`LD_PRELOAD`, `LD_LIBRARY_PATH`, and `NODE_OPTIONS` are accepted as flags but refused
from the project file.

### The project file is not a security boundary

`.claude-contained/` is writable from inside the container, so the agent can edit
`.claude-contained/env` and affect your *next* launch. The file is parsed literally and
never sourced, the refusals above block the variables that would subvert the sandbox or
the privilege drop, and the printed key names make additions visible — but an agent that
can write that file can already edit the code you are about to run. Prefer `--env` for
anything security-relevant, and use `--no-project-env` with an untrusted checkout.

Two more things worth knowing: a project's `.claude-contained/` is **not** gitignored for
you (see [Node.js Projects](#nodejs-projects-node_modules-overlay)), so an `env` file
holding a token can be committed by accident. And `--env` values are visible in
`container inspect`/`docker inspect` and in host `ps` output — this is plumbing between
you and your container, not a way to hide secrets.

The VS Code devcontainer does not use these launchers; set `containerEnv` in
`devcontainer.json` instead.

## Sandboxing

The container runs the AI tool under
[`@anthropic-ai/sandbox-runtime`](https://github.com/anthropic-experimental/sandbox-runtime)
(srt), which enforces a **deny-by-default egress allowlist**. Only allowlisted hosts are
reachable; everything else is refused. Enforcement is proxy-based — an HTTP proxy plus a SOCKS5
proxy for other TCP — so it is not limited to programs that honour `HTTPS_PROXY`.

srt's Linux dependencies (`bubblewrap`, `socat`, `ripgrep`) are already in the image, so there
is nothing extra to install.

### What this does and does not protect against

The sandbox is applied by `image/entrypoint.sh`, which wraps the tool process. Understand the
boundary before relying on it:

- **`container exec` / `docker exec` bypass the entrypoint entirely.** Anything started that way
  is unsandboxed unless routed through the `srt-run` wrapper (below). `-a/--attach` does this
  for you.
- **Inside a container, srt runs with `enableWeakerNestedSandbox`**, because bubblewrap cannot
  create privileged namespaces there. Treat srt as a guardrail against a careless or wandering
  agent — **not** as containment for actively hostile code. The Apple Container VM remains the
  real boundary; srt is defense in depth inside it.
- A host user invoking `container`/`docker` directly is outside this boundary by design.

### Allowing hosts

A default allowlist covers what the AI CLIs need to function: the provider APIs, **`/login`
OAuth for each bundled tool**, npm, PyPI, and GitHub. To extend it for a single run:

```bash
claude-contained --allow-host example.com --allow-host '*.internal.dev' .
```

For a persistent policy, create `~/.claude-contained/srt-settings.json`:

```json
{
  "network": {
    "allowedDomains": ["corp.example.com", "*.internal.dev"],
    "deniedDomains": ["telemetry.example.com"]
  }
}
```

Domains from the defaults, this file, and `--allow-host` are unioned. Any other srt setting you
put in this file (`tlsTerminate`, `allowUnixSockets`, …) is passed through untouched.

The `filesystem` section is generated per run and will be overwritten — the mounted directories
change every invocation, and srt matches paths literally on Linux (no globs), so they cannot be
hardcoded. The merged result is written to `/run/srt-settings.json` inside the container,
root-owned and read-only, so the sandboxed process cannot rewrite its own allowlist.

### Debugging

When something cannot reach the network, the first question is whether the sandbox is the cause:

```bash
claude-contained --no-sandbox .        # run with the sandbox off
claude-contained --no-sandbox -s .     # unsandboxed debug shell
```

`--no-sandbox` is independent of `-s/--shell`, so you can also get a *sandboxed* shell (`-s`
alone) to test an allowlist interactively. Inside the container, `srt --debug` reports what is
being blocked, and `cat /run/srt-settings.json` shows the effective policy.

A refused connection doesn't fail cleanly — srt's proxy closes it, so a blocked host tends to
surface as something like **"Socket is closed"** rather than a readable network error. If you
see that, the fix is `--allow-host` or `--no-sandbox`, not a networking problem elsewhere.

To run something under the sandbox in an already-running container, use the `srt-run` wrapper:

```bash
container exec -it -u dev <container-name> srt-run claude
```

If policy generation fails, the entrypoint refuses to start rather than silently running
unsandboxed; pass `--no-sandbox` to bypass deliberately.

## Accessing Host Services

The container runs in an isolated network, so `localhost` refers to the container itself, not your Mac. To connect to services running on your Mac, use `host.local` or the `-H` flag.

### Docker (`claude-docked`) - Recommended for Host Services

Use `-H PORT` to forward host ports to container localhost. This works because Docker Desktop has special routing to reach services bound to `127.0.0.1` on the host.

```bash
claude-docked -H 3845 .           # Forward host:3845 to container localhost:3845
claude-docked -H 3845 -H 8080 .   # Multiple ports
```

### Apple Containers (`claude-contained`) - Limited Host Access

Apple Containers can only reach host services bound to `0.0.0.0` (all interfaces), not `127.0.0.1` (localhost only). Most services (including Figma Desktop) bind to localhost only for security. See [apple/container#346](https://github.com/apple/container/issues/346) for the feature request to add `host.docker.internal` equivalent.

**What works:**
- Services you control that bind to `0.0.0.0`
- Using `host.local` hostname for services on all interfaces

**What doesn't work:**
- `-H` flag for localhost-bound services (like Figma Desktop MCP)

For localhost-bound services, use `claude-docked` instead.

### Configuring Figma Desktop MCP

Figma Desktop MCP binds to `localhost:3845`. Use Docker:

```bash
claude-docked -H 3845 .
```

**Requirements:**
- Figma Desktop must be running on your Mac
- The Figma MCP server must be enabled (Figma Desktop → Settings → enable MCP)
- Port 3845 is the default; adjust if you've changed it

### Other MCPs

For MCPs that expect `localhost`, use `claude-docked -H PORT`.

For services bound to all interfaces (`0.0.0.0`), you can use `host.local` in a `.mcp.json` override:

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

Use the claude-contained image as a VS Code devcontainer for Java/Spring/Vaadin development with full IDE features and Claude in the terminal.

### What It Provides

- Full Java IntelliSense, debugging, and Spring Boot support via pre-installed extensions
- `claude` command available in the integrated terminal
- **Path parity**: Your project stays at its original path (not `/workspaces/project`)
- Maven cache and git config shared with host

### Setup

1. Build the Docker image first:
   ```bash
   docker build -t claude-contained .
   ```

2. Copy the template to your project:
   ```bash
   cp -r devcontainer/ /path/to/your-project/.devcontainer/
   ```

3. Open the project in VS Code and select "Reopen in Container"

### Included Extensions

- Red Hat Java (IntelliSense)
- Debugger for Java
- Test Runner for Java
- Maven for Java
- Spring Boot Extension Pack
- Lombok Annotations Support
- GitLens

### Limitations

- **Image must be pre-built**: Run `docker build` before using
- **Don't run simultaneously**: Avoid running devcontainer while also using standalone `claude-contained` or `claude-docked` against the same contained Claude profile
- **host.local may not work**: VS Code manages networking differently; use explicit port forwarding instead

See `devcontainer/README.md` for detailed usage and customization options.
