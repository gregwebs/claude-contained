# Zellij Session Store

Zellij support is opt-in through `--zellij`. The launchers make Zellij the top-level container command, with the selected AI tool or `bash` passed as the initial pane command. The entrypoint still applies the srt sandbox around Zellij unless `--no-sandbox` is set.

The Zellij session store is `~/.claude-contained/zellij/`. Zellij data and cache persist there so sessions can be resurrected across container runs. Runtime sockets stay container-local because `XDG_RUNTIME_DIR` is set to `/tmp/claude-contained-zellij-runtime/`; Zellij then creates its versioned socket tree there, avoiding stale host socket reuse and keeping attach scoped to the live container. Marked Zellij runs enable `allowAllUnixSockets` under the Linux srt backend because path-specific Unix socket allowlisting is not available there.

Detaching from Zellij must not end the launcher. `zellij-run` waits until `zellij list-sessions --no-formatting` no longer reports the session as live, treating `(EXITED` sessions as resurrectable but not live. That keeps the container and any worktree locks alive while panes can still run project commands.

Launchers mark Zellij containers with `CLAUDE_CONTAINED_ZELLIJ=1` and `CLAUDE_CONTAINED_ZELLIJ_SESSION=<session>`. Docker also receives labels, but env inspection is the portable source of truth because Apple Containers and Docker expose env consistently enough through inspect output.
