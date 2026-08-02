# AI Contained

AI Contained runs local coding agents inside containers while preserving selected host state for a normal CLI workflow.

## Language

**Project directory**:
The single directory a contained run is centered on, mounted at the same path inside the container and used as the working directory.
_Avoid_: main dir, main_host, working directory, project root

**Extra mount**:
An additional host directory made visible to a run at the same path, optionally read-only.
_Avoid_: extra dir, volume, bind

**Container runtime**:
The engine that executes contained sessions — either Apple Containers or Docker.
_Avoid_: backend, engine, driver, provider

**Base image**:
The image built from this repository's Dockerfile, tagged `claude-contained:latest`, and run directly when a project supplies no tooling layer.
_Avoid_: the image, main image, parent image

**Build context**:
The directory handed to the container runtime's build command for the base image — the repository checkout root, which holds the Dockerfile and `image/`. A tooling layer has its own, separate context.
_Avoid_: build dir, source dir, Docker context

**Tooling layer**:
A complete Dockerfile a project checks in, built `FROM` the base image to add that project's toolchain. It lives in `.claude-contained/layer/` inside the project, which is both its home and its build context.
_Avoid_: layer, custom image, overlay, Dockerfile snippet
_Note_: "Java layer" and `INCLUDE_JAVA_LAYER` still appear in `USAGE.md` and the `Dockerfile`. They name the retired built-in, not a tooling layer, and go away with it (ADR-0006).

**Derived image**:
The image built from a project's tooling layer and run in place of the base image, tagged under `claude-contained-layer` with a content hash of the base image's identity and the layer's build context.
_Avoid_: layered image, project image, child image

**Layer env fragment**:
A `KEY=VALUE` file a tooling layer installs into a directory the entrypoint reads, contributing runtime environment the image alone cannot express — such as a cache path under the host home directory, which is unknown at build time.
_Avoid_: layer env file, dotenv, ENV

**Host Claude profile**:
The user's normal Claude Code profile directory at `~/.claude`, used by uncontained Claude Code.
_Avoid_: Host Claude config

**Contained Claude profile**:
The Claude Code profile reserved for contained runs, stored at `~/.claude-contained/claude` and presented inside containers as `~/.claude`.
_Avoid_: Container Claude config

**Claude account state**:
The shared Claude login and account file stored at `~/.claude-contained/.claude.json`.
_Avoid_: Claude credentials

**Claude extension resources**:
Shared Claude file resources such as skills, agents, commands, and plugins.
_Avoid_: Claude settings

**Project env file**:
The per-project `.claude-contained/env` file, holding `KEY=VALUE` lines applied to the tool process on the next launch of that directory. Writable from inside the container, so it is a convenience rather than a trusted input.
_Avoid_: dotenv, .env

**Zellij session store**:
The host-backed Zellij state reserved for contained runs, used to resurrect named terminal workspaces after their container processes exit.
_Avoid_: Zellij config, Zellij cache

**Diagnostic record**:
One observation the launcher emits about its own decision-making, for someone debugging the launcher rather than someone using it. Silent unless asked for.
_Avoid_: log message, debug print, trace

**Relocated output**:
User-facing text that would normally be printed to the terminal, carried on the diagnostic stream instead because the user asked for it. It keeps its identity as output, so it is never filtered away.
_Avoid_: redirected output, captured output

**Diagnostic stream**:
Where diagnostic records and relocated output are written — stderr by default, a file when the user names one.
_Avoid_: log sink, log output, logger

**Component**:
The launcher subsystem a diagnostic record is attributed to, drawn from a closed set so records can be filtered by origin.
_Avoid_: module, subsystem, package
