# AI Contained

AI Contained runs local coding agents inside containers while preserving selected host state for a normal CLI workflow.

## Language

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

**Zellij session store**:
The host-backed Zellij state reserved for contained runs, used to resurrect named terminal workspaces after their container processes exit.
_Avoid_: Zellij config, Zellij cache
