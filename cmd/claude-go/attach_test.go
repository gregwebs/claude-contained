package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-contained/internal/runtime"
)

// failRunner fails the test if the container `run` path is ever reached --
// used by attach tests, where attach must exec before getting anywhere near
// it.
func failRunner(t *testing.T) runner {
	t.Helper()
	return func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
		t.Fatalf("container run should not have been reached, argv=%v", argv)
		return 0
	}
}

// TestAttachHoldsNoWorktreeLock is the ordering guard the ticket explicitly
// asks for: any future change that moves lock acquisition earlier than the
// attach dispatch in run.go would break the assumption that process
// replacement is safe there. It is modeled on
// TestLocksReleasedOnlyAfterContainerExit, which also proves an ordering
// rather than an end state.
//
// Do not call t.Parallel() here: replaceProcess is swapped at package scope.
func TestAttachHoldsNoWorktreeLock(t *testing.T) {
	project, wt := hiddenWorktreeFixture(t)
	withStubbedHostAndPath(t)
	t.Setenv("STUB_LIST", "aic-live")

	// A zero-byte srt placeholder file (host.CleanupPlaceholderFiles' own
	// fixture shape) must also survive: the sweep sits well below the attach
	// dispatch in run.go, so it must never run on this path either.
	placeholder := filepath.Join(project, ".ripgreprc")
	if err := os.WriteFile(placeholder, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	orig := replaceProcess
	var argv []string
	replaceProcess = func(a []string) error {
		// Assert at the exact instant of replacement: no worktree lock, no
		// mutex directory -- attach must run before either is taken.
		if worktreeIsLocked(t, project, wt) {
			t.Error("worktree should not be locked: attach must exec before any lock is offered")
		}
		mutexDir := filepath.Join(project, ".git", "claude-contained-worktree-locks.lock")
		if _, err := os.Stat(mutexDir); !os.IsNotExist(err) {
			t.Errorf("worktree-lock mutex directory exists (err=%v), want none", err)
		}
		if _, err := os.Stat(placeholder); err != nil {
			t.Errorf("placeholder file swept before attach exec'd (err=%v), want it untouched", err)
		}
		argv = a
		return nil
	}
	t.Cleanup(func() { replaceProcess = orig })

	var stdout, stderr bytes.Buffer
	// -W requests locking; attach must bypass it entirely. failRunner fails
	// the test if the container `run` path is ever reached.
	code := runWith(failRunner(t), runtime.Darwin, []string{"claude-contained", "-a", "live", "-W", "-C", project},
		strings.NewReader(""), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("runWith exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if worktreeIsLocked(t, project, wt) {
		t.Error("worktree should still be unlocked once runWith returns")
	}
	if len(argv) == 0 {
		t.Fatal("replaceProcess was never called")
	}
	got := strings.Join(argv, " ")
	if !strings.HasSuffix(got, "aic-live srt-run /opt/claude/claude") {
		t.Errorf("argv = %q, want it to end with %q", got, "aic-live srt-run /opt/claude/claude")
	}
}

// TestAttachPickerHoldsNoWorktreeLock is TestAttachHoldsNoWorktreeLock's
// sibling for the picker path (bare -a with no name): the picker prompts,
// reads a selection from stdin, and must exec before any lock is offered
// exactly like the by-name path.
//
// Do not call t.Parallel() here: replaceProcess is swapped at package scope.
func TestAttachPickerHoldsNoWorktreeLock(t *testing.T) {
	project, wt := hiddenWorktreeFixture(t)
	withStubbedHostAndPath(t)
	t.Setenv("STUB_LIST", "aic-live")

	orig := replaceProcess
	var argv []string
	replaceProcess = func(a []string) error {
		if worktreeIsLocked(t, project, wt) {
			t.Error("worktree should not be locked: attach must exec before any lock is offered")
		}
		argv = a
		return nil
	}
	t.Cleanup(func() { replaceProcess = orig })

	var stdout, stderr bytes.Buffer
	code := runWith(failRunner(t), runtime.Darwin, []string{"claude-contained", "-a", "-W", "-C", project},
		strings.NewReader("1\n"), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("runWith exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if worktreeIsLocked(t, project, wt) {
		t.Error("worktree should still be unlocked once runWith returns")
	}
	if len(argv) == 0 {
		t.Fatal("replaceProcess was never called")
	}
	got := strings.Join(argv, " ")
	if !strings.HasSuffix(got, "aic-live srt-run /opt/claude/claude") {
		t.Errorf("argv = %q, want it to end with %q", got, "aic-live srt-run /opt/claude/claude")
	}
}

// TestAttachByNameMissCreatesNothing asserts the name-miss path exits without
// ever replacing the process or touching the filesystem.
func TestAttachByNameMissCreatesNothing(t *testing.T) {
	project, _ := hiddenWorktreeFixture(t)
	withStubbedHostAndPath(t)
	t.Setenv("STUB_LIST", "aic-live")

	orig := replaceProcess
	replaceProcess = func(argv []string) error {
		t.Fatalf("replaceProcess should not have been called, argv=%v", argv)
		return nil
	}
	t.Cleanup(func() { replaceProcess = orig })

	var stdout, stderr bytes.Buffer
	code := runWith(failRunner(t), runtime.Darwin, []string{"claude-contained", "-a", "nope", "-C", project},
		strings.NewReader(""), &stdout, &stderr)

	if code != 1 {
		t.Fatalf("runWith exit = %d, want 1\nstderr:\n%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".claude-contained")); !os.IsNotExist(err) {
		t.Errorf("project .claude-contained directory exists (err=%v), want none", err)
	}
	home := os.Getenv("HOME")
	if _, err := os.Stat(filepath.Join(home, ".claude-contained")); !os.IsNotExist(err) {
		t.Errorf("home .claude-contained directory exists (err=%v), want none", err)
	}
}
