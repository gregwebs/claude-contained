package main

import (
	"context"
	"io"

	"claude-contained/internal/attach"
	"claude-contained/internal/cli"
	"claude-contained/internal/host"
	"claude-contained/internal/runtime"
	"claude-contained/internal/zellij"
)

// liveZellijRecords mirrors load_live_zellij_records (claude-contained:469-482):
// every running container with the conventional prefix, inspected for the
// Zellij markers, in the order the runtime listed them.
//
// Errors are swallowed on both calls, matching bash's `2>/dev/null || true` on
// the list (:427) and the `|| true` on the inspect (:465,:476). A runtime that
// cannot answer means "nothing is live", not a failure.
func liveZellijRecords(ctx context.Context, rt runtime.Runtime) []zellij.Record {
	names, _ := rt.List(ctx)
	var out []zellij.Record
	for _, name := range attach.FilterRunning(names) {
		lines, _ := rt.InspectEnv(ctx, name)
		if s := zellij.SessionFromEnv(lines); s != "" {
			out = append(out, zellij.Record{Container: name, Session: s})
		}
	}
	return out
}

// zellijLaunchGate resolves the target session and applies the refusal/force
// rules (claude-contained:1439-1471). It returns the session name and 0 to
// proceed, or "" and a non-zero exit code. It runs where bash runs it: after
// the project env file has had its say, before the second placeholder sweep,
// the worktree prompt and every mkdir -- so a refusal leaves the host as it
// found it.
func zellijLaunchGate(
	ctx context.Context, rt runtime.Runtime, cfg cli.Config, mainHost string, stderr io.Writer,
) (string, int) {
	session := cfg.ZellijSessionName
	if !cfg.ZellijSessionNameSet {
		session = zellij.SessionName(mainHost)
	}

	// Unreachable in practice, and kept anyway for parity with bash's own
	// redundant re-check (claude-contained:1446): an explicit name was already
	// validated during parsing (cli.Config), and a generated one is
	// "cc-" + sanitized + "-" + hex, which cannot violate the pattern.
	if err := cli.ValidateZellijSessionName(session, stderr); err != nil {
		return "", exitCode(err)
	}

	records := liveZellijRecords(ctx, rt)

	if code := zellij.ResolveLaunch(session, records, cfg.ZellijNewSession, stderr); code != 0 {
		return "", code
	}
	return session, 0
}

// zellijAttachAndExec reconnects a Zellij client to a live session and replaces
// this process, mirroring bash's exec at claude-contained:949.
//
// Like attachAndExec (attach.go:40) this is only safe because of *where* it is
// called: before the project directory is resolved, before the placeholder
// sweep, before any lock or mutex, before catchInterrupts and the deferred
// cleanup. TestZellijAttachHoldsNoWorktreeLock guards that ordering.
func zellijAttachAndExec(
	ctx context.Context, rt runtime.Runtime, cfg cli.Config,
	h host.State, p *prompter, stdout, stderr io.Writer,
) int {
	records := liveZellijRecords(ctx, rt)

	dec := zellij.ResolveAttach(zellij.AttachRequest{
		Session:    cfg.ZellijSessionName,
		SrtDisable: cfg.SrtDisable,
		Home:       h.Home,
		Records:    records,
		Stdout:     stdout,
		Stderr:     stderr,
		Prompt:     p.askLine,
	})
	return execDecision(rt, dec, stderr)
}
