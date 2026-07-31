package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-contained/internal/host"
	"claude-contained/internal/runtime"
)

// markedInspectJSON is the Apple JSON shape runtime.Apple.InspectEnv parses,
// reporting a container backing the given Zellij session.
func markedInspectJSON(session string) string {
	return `[{"configuration":{"initProcess":{"environment":["CLAUDE_CONTAINED_ZELLIJ=1","CLAUDE_CONTAINED_ZELLIJ_SESSION=` + session + `"]}}}]`
}

// TestZellijAttachHoldsNoWorktreeLock is TestAttachHoldsNoWorktreeLock's
// sibling for the Zellij attach path: process replacement must happen before
// any lock is offered here too.
//
// Do not call t.Parallel() here: replaceProcess is swapped at package scope.
func TestZellijAttachHoldsNoWorktreeLock(t *testing.T) {
	project, wt := hiddenWorktreeFixture(t)
	withStubbedHostAndPath(t)
	t.Setenv("STUB_LIST", "aic-z1")
	t.Setenv("STUB_INSPECT", markedInspectJSON("alpha"))

	placeholder := filepath.Join(project, ".ripgreprc")
	if err := os.WriteFile(placeholder, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	orig := replaceProcess
	var argv []string
	replaceProcess = func(a []string) error {
		if worktreeIsLocked(t, project, wt) {
			t.Error("worktree should not be locked: Zellij attach must exec before any lock is offered")
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
	code := runWith(failRunner(t), runtime.Darwin, []string{"claude-contained", "--zellij", "--attach", "--session", "alpha", "-W", "-C", project},
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
	if !strings.HasSuffix(got, "aic-z1 srt-run /usr/local/bin/zellij-attach alpha") {
		t.Errorf("argv = %q, want it to end with %q", got, "aic-z1 srt-run /usr/local/bin/zellij-attach alpha")
	}
}

// TestZellijAttachNoSessionsCreatesNothing asserts the zero-live-session path
// exits without ever replacing the process or touching the filesystem.
func TestZellijAttachNoSessionsCreatesNothing(t *testing.T) {
	withStubbedHostAndPath(t)

	orig := replaceProcess
	replaceProcess = func(argv []string) error {
		t.Fatalf("replaceProcess should not have been called, argv=%v", argv)
		return nil
	}
	t.Cleanup(func() { replaceProcess = orig })

	var stdout, stderr bytes.Buffer
	code := runWith(failRunner(t), runtime.Darwin, []string{"claude-contained", "--zellij", "--attach"},
		strings.NewReader(""), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("runWith exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if want := "No live Zellij sessions\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	home := os.Getenv("HOME")
	if _, err := os.Stat(filepath.Join(home, ".claude-contained")); !os.IsNotExist(err) {
		t.Errorf("home .claude-contained directory exists (err=%v), want none", err)
	}
}

// TestZellijLaunchRefusesWhenAnotherSessionLive proves the gate runs before
// every mkdir: a refusal must leave the Zellij session store uncreated.
func TestZellijLaunchRefusesWhenAnotherSessionLive(t *testing.T) {
	project := host.ResolvePath(t.TempDir())
	withStubbedHostAndPath(t)
	t.Setenv("STUB_LIST", "aic-z1")
	t.Setenv("STUB_INSPECT", markedInspectJSON("other"))

	var stdout, stderr bytes.Buffer
	code := runWith(failRunner(t), runtime.Darwin, []string{"claude-contained", "--zellij", "-N", "-s", "-C", project},
		strings.NewReader(""), &stdout, &stderr)

	if code != 1 {
		t.Fatalf("runWith exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	wantErr := "A Zellij-backed container is already running:\n" +
		"  other\n" +
		"Use --zellij --attach [--session NAME] to reconnect, or --zellij --new-session [--session NAME] to start another session.\n"
	if stderr.String() != wantErr {
		t.Errorf("stderr = %q, want %q", stderr.String(), wantErr)
	}

	home := os.Getenv("HOME")
	if _, err := os.Stat(filepath.Join(home, ".claude-contained", "zellij")); !os.IsNotExist(err) {
		t.Errorf("zellij session store exists (err=%v), want none: the gate must precede every mkdir", err)
	}
}

// TestZellijLaunchProceedsWithNewSession is
// TestZellijLaunchRefusesWhenAnotherSessionLive's positive sibling:
// --new-session forces past the other live session, both markers reach the
// runtime argv, and the host-side session store is created.
func TestZellijLaunchProceedsWithNewSession(t *testing.T) {
	project := host.ResolvePath(t.TempDir())
	withStubbedHostAndPath(t)
	t.Setenv("STUB_LIST", "aic-z1")
	t.Setenv("STUB_INSPECT", markedInspectJSON("other"))

	var gotArgv []string
	recorder := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
		gotArgv = argv
		return 0
	}

	var stdout, stderr bytes.Buffer
	code := runWith(recorder, runtime.Darwin, []string{"claude-contained", "--zellij", "--new-session", "--session", "mine", "-N", "-s", "-C", project},
		strings.NewReader(""), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("runWith exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if len(gotArgv) == 0 {
		t.Fatal("the runner was never invoked")
	}
	joined := strings.Join(gotArgv, " ")
	if !strings.Contains(joined, "CLAUDE_CONTAINED_ZELLIJ=1") {
		t.Errorf("argv missing the Zellij marker: %v", gotArgv)
	}
	if !strings.Contains(joined, "CLAUDE_CONTAINED_ZELLIJ_SESSION=mine") {
		t.Errorf("argv missing the session marker: %v", gotArgv)
	}
	if !strings.Contains(joined, "zellij-run mine --") {
		t.Errorf("argv missing the zellij-run wrapper: %v", gotArgv)
	}

	home := os.Getenv("HOME")
	if _, err := os.Stat(filepath.Join(home, ".claude-contained", "zellij", "data")); err != nil {
		t.Errorf("zellij data dir missing (err=%v), want it created", err)
	}
}
