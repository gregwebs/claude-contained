package imagescript

// Tests for image/shell-run.sh, which gives a debug shell a fresh PTY (via
// util-linux `script`) when the contained runtime left bash without a
// controlling terminal, and otherwise falls back to plain bash.

import (
	"testing"

	"claude-contained/internal/blackbox"
)

func TestShellRunForcedPTYUsesScript(t *testing.T) {
	stubs := blackbox.NewStubs(t, "script")
	stubs.Arm(t, "script", blackbox.ArmConfig{Match: "*", Exit: 43})

	res := runScript(t, scriptOpts{
		Script: scriptPath(t, "shell-run.sh"),
		Env:    []string{"CLAUDE_CONTAINED_SHELL_RUN_FORCE_SCRIPT=1"},
		Stubs:  stubs,
	})

	if res.Code != 43 {
		t.Errorf("exit %d, want 43 (script's status not propagated)\nstderr:\n%s", res.Code, res.Stderr)
	}
	events := stubs.Events(t)
	if len(events) != 1 {
		t.Fatalf("script invoked %d times, want once: %v", len(events), events)
	}
	want := []string{"-qfec", "exec /usr/bin/env bash", "/dev/null"}
	if !equalStrings(events[0].Argv, want) {
		t.Errorf("script argv = %v, want %v", events[0].Argv, want)
	}
}

func TestShellRunFallsBackToBashWhenNotTTY(t *testing.T) {
	stubs := blackbox.NewStubs(t, "script")

	res := runScript(t, scriptOpts{
		Script: scriptPath(t, "shell-run.sh"),
		Args:   []string{"-c", "printf fallback"},
		Stubs:  stubs,
	})

	if res.Code != 0 {
		t.Errorf("exit %d, want 0\nstderr:\n%s", res.Code, res.Stderr)
	}
	if res.Stdout != "fallback" {
		t.Errorf("stdout = %q, want %q", res.Stdout, "fallback")
	}
	if events := stubs.Events(t); len(events) != 0 {
		t.Errorf("script was invoked on the non-TTY fallback path: %v", events)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
