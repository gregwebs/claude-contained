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

**Build context**:
The directory handed to the container runtime's build command — the repository checkout root, which holds the Dockerfile and `image/`.
_Avoid_: build dir, source dir, Docker context

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
