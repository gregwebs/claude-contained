package blackbox

// These tests cover the stub generalizations the image-script suite relies on:
// contains-matching (a command whose discriminating subcommand sits past
// argv[0]), environment capture (proving a script exported a variable into a
// command it invoked), and the WaitForEvents readiness helper (for commands the
// script backgrounds and then races). They drive the stub the same way a real
// launcher would: this test binary re-executed under a command name off a PATH
// the harness controls, with BLACKBOX_STUB_SPEC inherited.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if RunStubIfInvoked() {
		return // unreachable: the stub exits the process.
	}
	os.Exit(m.Run())
}

// runStubCmd executes one stubbed command (a symlink to this binary under
// stubs.Dir) with the spec env inherited, returning its stdout and exit code.
func runStubCmd(t *testing.T, s *Stubs, name string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(filepath.Join(s.Dir, name), args...)
	cmd.Env = append(os.Environ(), s.LauncherEnv(t))
	out, err := cmd.Output()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("running stub %s: %v", name, err)
		}
	}
	return string(out), code
}

func TestStubMatchContainsSelectsPastArgv0(t *testing.T) {
	s := NewStubs(t, "zellij")
	s.Arm(t, "zellij", ArmConfig{MatchContains: "list-sessions", Stdout: "sess (EXITED)\n"})
	s.Arm(t, "zellij", ArmConfig{MatchContains: "attach", Exit: 7})

	out, code := runStubCmd(t, s, "zellij", "--config", "x", "--data-dir", "y", "list-sessions", "--no-formatting")
	if out != "sess (EXITED)\n" {
		t.Errorf("list-sessions stdout = %q, want the armed output", out)
	}
	if code != 0 {
		t.Errorf("list-sessions exit = %d, want 0", code)
	}

	_, code = runStubCmd(t, s, "zellij", "--config", "x", "--data-dir", "y", "attach", "--create", "sess")
	if code != 7 {
		t.Errorf("attach exit = %d, want 7 (the attach arm was not selected)", code)
	}
}

func TestStubCaptureEnvRecordsOnlySetKeys(t *testing.T) {
	s := NewStubs(t, "probe")
	s.CaptureEnv(t, "WANTED", "MISSING")

	cmd := exec.Command(filepath.Join(s.Dir, "probe"), "go")
	cmd.Env = append(os.Environ(), s.LauncherEnv(t), "WANTED=here")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running probe stub: %v", err)
	}

	events := s.Events(t)
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(events))
	}
	if got := events[0].Env["WANTED"]; got != "here" {
		t.Errorf("captured WANTED = %q, want %q", got, "here")
	}
	if _, ok := events[0].Env["MISSING"]; ok {
		t.Error("MISSING was captured despite being unset")
	}
}

func TestWaitForEventsReachesCountAndTimesOut(t *testing.T) {
	s := NewStubs(t, "probe")

	if WaitForEvents(t, s, 1, 150*time.Millisecond) {
		t.Error("WaitForEvents reported an event before any ran")
	}

	cmd := exec.Command(filepath.Join(s.Dir, "probe"), "once")
	cmd.Env = append(os.Environ(), s.LauncherEnv(t))
	if err := cmd.Run(); err != nil {
		t.Fatalf("running probe stub: %v", err)
	}

	if !WaitForEvents(t, s, 1, 5*time.Second) {
		t.Error("WaitForEvents did not observe the recorded invocation")
	}
}
