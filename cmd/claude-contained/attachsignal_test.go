package main

// attachsignal_test.go covers the attach proxy path's signal handling in
// process. The compiled-binary suite (artifact_test.go) proves the ordinary run
// path's inherited-signal behavior end to end; the attach --log-only/Zellij
// routes reflect signals through the very same seam -- catchInterrupts, then
// awaitPendingSignal on the child's exit (attach.go execDecision) -- so their
// coverage is this seam plus the proxy route that reaches it, rather than a
// second subprocess. This is what lets the retired signal suite's log-only and
// Zellij attach cases be replaced rather than dropped.

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"claude-contained/internal/runtime"
)

// TestAwaitPendingSignal pins the reflection logic both attach and the ordinary
// run path share. The exitCode == -1 branch is the process-group race guard: a
// child killed by the same group signal can be reaped before the Go runtime has
// delivered that signal to the channel, so only that case gets a bounded poll.
func TestAwaitPendingSignal(t *testing.T) {
	t.Run("immediate pending signal is reflected", func(t *testing.T) {
		ch := make(chan os.Signal, 1)
		ch <- syscall.SIGTERM
		sig, ok := awaitPendingSignal(&interrupts{ch: ch}, 0)
		if !ok || signalExitCode(sig) != 143 {
			t.Fatalf("ok=%v code=%d, want ok=true code=143", ok, signalExitCode(sig))
		}
	})

	t.Run("no signal on a normal exit is not reflected", func(t *testing.T) {
		if _, ok := awaitPendingSignal(&interrupts{ch: make(chan os.Signal, 1)}, 0); ok {
			t.Fatal("ok=true, want false when nothing was caught")
		}
	})

	t.Run("a signal that lands during the exit -1 poll is reflected", func(t *testing.T) {
		ch := make(chan os.Signal, 1)
		go func() {
			time.Sleep(10 * time.Millisecond)
			ch <- syscall.SIGINT
		}()
		sig, ok := awaitPendingSignal(&interrupts{ch: ch}, -1)
		if !ok || signalExitCode(sig) != 130 {
			t.Fatalf("ok=%v code=%d, want ok=true code=130", ok, signalExitCode(sig))
		}
	})

	t.Run("exit -1 with no signal returns after the bounded poll", func(t *testing.T) {
		if _, ok := awaitPendingSignal(&interrupts{ch: make(chan os.Signal, 1)}, -1); ok {
			t.Fatal("ok=true, want false when the poll finds nothing")
		}
	})
}

// TestAttachProxyReflectsSignal drives the real --log-only attach route
// (attachAndExec -> execDecision proxy branch) and proves it reflects a signal
// caught while the proxied child was in the foreground as the launcher's 128+n
// status. The injected runner raises SIGTERM to this process -- caught by the
// interrupts execDecision installs just before it -- and returns -1, exactly as
// a real child killed by that signal would. Not parallel: it installs a
// process-wide signal handler for the duration of the call.
func TestAttachProxyReflectsSignal(t *testing.T) {
	withStubbedHostAndPath(t)
	t.Setenv("STUB_LIST", "aic-live")

	raiseAndReportKilled := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
		// The signal is diverted to execDecision's own channel (signal.Notify is
		// already active here), so it never terminates the test process; the -1
		// mirrors exec.ExitError.ExitCode() for a signal-killed child and routes
		// awaitPendingSignal into its bounded poll, which catches the pending
		// signal deterministically.
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		return -1
	}

	var stdout, stderr bytes.Buffer
	code := runWith(raiseAndReportKilled, runtime.Darwin,
		[]string{"claude-contained", "--log-only", "--log-level=off", "-a", "live"},
		strings.NewReader(""), &stdout, &stderr)

	if code != 143 {
		t.Errorf("proxy attach did not reflect SIGTERM as 143: exit %d\nstderr:\n%s", code, stderr.String())
	}
}
