package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"claude-contained/internal/attach"
	"claude-contained/internal/cli"
	"claude-contained/internal/env"
	"claude-contained/internal/host"
	"claude-contained/internal/runtime"
)

// replaceProcess is the seam for process replacement. syscall.Exec never
// returns, so tests substitute a recorder. A package-level var rather than a
// parameter keeps runWith's signature -- and its four existing call sites in
// run_test.go -- untouched.
var replaceProcess = func(argv []string) error {
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(bin, argv, os.Environ()) //nolint:gosec // argv is the launcher's own rendered command line.
}

// attachAndExec reconnects to a running container and replaces this process,
// mirroring bash's `exec container exec ...` (claude-contained:989-1027).
//
// Process replacement is only safe because of *where* this is called: before
// the project directory is resolved, before any placeholder sweep, before the
// worktree-lock offer takes a lock or the mkdir mutex, and before
// catchInterrupts and the deferred cleanup are installed (run.go:145-163).
// Nothing is held that exec would strand. TestAttachHoldsNoWorktreeLock
// guards that ordering; if a future change moves lock acquisition earlier,
// that test fails rather than leaking a lock forever.
func attachAndExec(
	ctx context.Context, rt runtime.Runtime, cfg cli.Config,
	h host.State, pairs []env.Pair, p *prompter, stdout, stderr io.Writer,
) int {
	// List already swallows its own error (apple.go, docker.go), matching
	// bash's `container list --quiet 2>/dev/null || true`.
	running, _ := rt.List(ctx)

	dec := attach.Resolve(attach.Request{
		Name:       cfg.AttachName,
		Shell:      cfg.ShellMode,
		Tool:       cfg.Tool,
		Yolo:       cfg.YoloMode,
		SrtDisable: cfg.SrtDisable,
		Home:       h.Home,
		Env:        pairs,
		Running:    running,
		Stdout:     stdout,
		Stderr:     stderr,
		Prompt:     p.askLine,
	})
	if dec.Spec == nil {
		return dec.Code
	}

	argv := rt.RenderExec(*dec.Spec)
	if err := replaceProcess(argv); err != nil {
		// The message deliberately differs from bash's own `exec` failure
		// diagnostic, which embeds the script path and a source line number --
		// the same accepted divergence as completeEnv's file-read error
		// (run.go:317-321).
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 127 // bash's status for a failed `exec`.
	}
	return 0 // unreachable in production: a successful exec never returns.
}
