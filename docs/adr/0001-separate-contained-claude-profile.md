# Separate Contained Claude Profile

Contained Claude runs default to a container-specific profile at `~/.claude-contained/claude`, mounted inside the container as `~/.claude`, while Claude account state stays shared through `~/.claude-contained/.claude.json` and host Claude extension resources are mounted from `~/.claude/{skills,agents,commands,plugins}`. We chose this over bind-mounting host `~/.claude` wholesale so contained runs can have different user settings without duplicating login state or common extensions; `--share-host-claude` remains as a compatibility path for the legacy behavior.

Status: not superseded. [ADR-0009](0009-positional-container-command.md) removes the launcher's knowledge of program names generally, but the Claude-profile plumbing this ADR describes is deliberately left standing — it is the one remaining AI-specific decision in the launcher, and its removal (if any) is a follow-up tracked separately from the #20 ticket series.
