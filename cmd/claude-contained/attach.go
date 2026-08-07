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
	"claude-contained/internal/diagnostic"
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

// attachAndExec reconnects to a running container and normally replaces this
// process, mirroring bash's `exec container exec ...`
// (claude-contained:989-1027). --log-only must proxy instead: process
// replacement cannot preserve Go's line-aware relocated-output writers.
//
// Process replacement is only safe because of *where* this is called: before
// the project directory is resolved, before any placeholder sweep, before the
// worktree-lock offer takes a lock or the mkdir mutex, and before
// catchInterrupts and the deferred cleanup are installed (run.go:145-163).
// Nothing is held that exec would strand. TestAttachHoldsNoWorktreeLock
// guards that ordering; if a future change moves lock acquisition earlier,
// that test fails rather than leaking a lock forever.
func attachAndExec(
	ctx context.Context, exec runner, proxyOutput bool, rt runtime.Runtime, cfg cli.Config,
	h host.State, pairs []env.Pair, p *prompter, stdout, stderr io.Writer,
) int {
	// List already swallows its own error (apple.go, docker.go), matching
	// bash's `container list --quiet 2>/dev/null || true`.
	running, _ := rt.List(ctx)
	requestMode := "picker"
	if cfg.AttachName != "" {
		requestMode = "named"
	}
	diagnostic.For(ctx, diagnostic.ComponentAttach).Info("attach candidates observed",
		diagnostic.Int("running_count", len(running)),
		diagnostic.String("request_mode", requestMode))

	dec := attach.Resolve(attach.Request{
		Name:       cfg.AttachName,
		Shell:      cfg.ShellMode,
		Command:    cfg.Command,
		SrtDisable: cfg.SrtDisable,
		Home:       h.Home,
		Env:        pairs,
		Running:    running,
		Stdout:     stdout,
		Stderr:     stderr,
		Prompt:     p.askLine,
	})
	return execDecision(ctx, exec, proxyOutput, rt, dec, diagnostic.ComponentAttach,
		p.reader, stdout, stderr)
}

// execDecision carries out an attach decision: either it is a plain exit code,
// or it replaces this process with the runtime's exec. Shared by the plain and
// Zellij attach paths, which reach identical decisions by different routes.
func execDecision(
	ctx context.Context, exec runner, proxyOutput bool, rt runtime.Runtime, dec attach.Decision,
	component diagnostic.Component, stdin io.Reader, stdout, stderr io.Writer,
) int {
	if dec.Spec == nil {
		diagnostic.For(ctx, component).Info("attach decision completed without process replacement",
			diagnostic.Int("exit_status", dec.Code))
		return dec.Code
	}

	argv := rt.RenderExec(*dec.Spec)
	executionMode := "replace"
	if proxyOutput {
		executionMode = "proxy"
	}
	diagnostic.For(ctx, component).Info("attach execution prepared",
		diagnostic.String("execution_mode", executionMode),
		diagnostic.Value("argv", runtime.DiagnosticArgv(argv)))
	var ints *interrupts
	if proxyOutput {
		ints = catchInterrupts()
		defer ints.stop()
	}
	if err := diagnostic.Flush(ctx); err != nil {
		return cli.ExitFailure
	}
	if proxyOutput {
		// Proxying keeps Go's routed writers in the output path, so the launcher
		// remains alive. Catch the same signals as the ordinary run path: a
		// launcher-only signal is deferred until the child returns, while a
		// process-group signal reaches the child directly and is reflected as
		// the launcher's conventional 128+signal status.
		code := exec(ctx, argv, stdin, stdout, stderr)
		diagnostic.For(ctx, component).Info("attach proxied command completed",
			diagnostic.Int("exit_status", code))
		if sig, ok := awaitPendingSignal(ints, code); ok {
			return signalExitCode(sig)
		}
		return code
	}
	if err := replaceProcess(argv); err != nil {
		diagnostic.For(ctx, component).Error("attach process replacement failed",
			diagnostic.ErrorAttr(err))
		// The message deliberately differs from bash's own `exec` failure
		// diagnostic, which embeds the script path and a source line number --
		// the same accepted divergence as completeEnv's file-read error
		// (run.go:317-321).
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 127 // bash's status for a failed `exec`.
	}
	return 0 // unreachable in production: a successful exec never returns.
}
