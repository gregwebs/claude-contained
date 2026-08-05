package main

// artifact_test.go is the compiled-binary black-box suite: it drives the
// freshly built launcher artifact as a real subprocess, covering the properties
// the in-process golden and selection suites cannot prove because they run
// runWith directly -- that the shipped binary embeds and emits the help text,
// selects its runtime from argv[0], propagates a real child exit status, and
// gives its foreground child the right signal disposition. Everything the
// in-process suites already prove (exact CLI error text, the two-pass selection
// grammar, the full launcher matrix) stays there; see ADR-0008.
//
// The stub runtimes are this test binary re-executed under a runtime name; the
// TestMain below routes such an invocation into the blackbox stub instead of the
// test runner. See internal/blackbox.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"claude-contained/internal/blackbox"
	"claude-contained/internal/host"
)

func TestMain(m *testing.M) {
	if blackbox.RunStubIfInvoked() {
		return // unreachable: the stub exits the process.
	}
	code := m.Run()
	blackbox.Cleanup() // before os.Exit, which skips deferred cleanup.
	os.Exit(code)
}

// readHelp returns the exact bytes of an embedded help file from source -- the
// source of truth the shipped binary must emit verbatim.
func readHelp(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(blackbox.ModuleRoot(t), "internal", "runtime", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// TestArtifactEmbeddedHelpShips proves the built artifact carries each runtime's
// help text and emits it verbatim on stdout, alone, with no runtime invoked and
// the host untouched. Apple's help is reachable off macOS because --help wins
// before the non-macOS selection refusal.
func TestArtifactEmbeddedHelpShips(t *testing.T) {
	launcher := blackbox.BuildLauncher(t)
	for _, tc := range []struct {
		runtime string
		file    string
	}{
		{"apple", "help_contained.txt"},
		{"docker", "help_docked.txt"},
	} {
		t.Run(tc.runtime, func(t *testing.T) {
			home := t.TempDir()
			stubs := blackbox.NewStubs(t, "docker", "container")
			before := blackbox.Manifest(home)

			stdout, stderr, code := blackbox.Run(t, blackbox.RunOpts{
				Bin:   launcher.Primary,
				Args:  []string{"--container-runtime=" + tc.runtime, "--help"},
				Home:  home,
				Stubs: stubs,
			})

			if code != 0 {
				t.Errorf("exit %d, want 0", code)
			}
			if want := readHelp(t, tc.file); stdout != want {
				t.Errorf("stdout is not the embedded %s verbatim\n--- got ---\n%s\n--- want ---\n%s", tc.file, stdout, want)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
			if bins := stubs.InvokedBins(t); len(bins) != 0 {
				t.Errorf("a runtime was invoked on --help: %v", bins)
			}
			blackbox.AssertUnchanged(t, home, before)
		})
	}
}

// TestArtifactArgv0SelectsDocker proves the shipped -docked symlink selects the
// Docker runtime from its argv[0] basename alone, with no flag and no
// environment variable -- the compat affordance the Makefile builds.
func TestArtifactArgv0SelectsDocker(t *testing.T) {
	launcher := blackbox.BuildLauncher(t)
	home := t.TempDir()
	stubs := blackbox.NewStubs(t, "docker", "container")

	stdout, stderr, code := blackbox.Run(t, blackbox.RunOpts{
		Bin:   launcher.Docked,
		Args:  []string{"--help"},
		Home:  home,
		Stubs: stubs,
	})

	if code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
	if want := readHelp(t, "help_docked.txt"); stdout != want {
		t.Errorf("argv[0]=...-docked did not print the Docker help verbatim\n--- got ---\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

// TestArtifactRuntimeLaunchAndExit is the representative stubbed runtime launch:
// it proves the shipped binary reaches a real `docker run` after its liveness
// probe, invokes only the selected runtime, and propagates the child's non-zero
// exit status unchanged.
func TestArtifactRuntimeLaunchAndExit(t *testing.T) {
	launcher := blackbox.BuildLauncher(t)
	home := t.TempDir()
	proj := t.TempDir()
	stubs := blackbox.NewStubs(t, "docker", "container")
	stubs.Arm(t, "docker", blackbox.ArmConfig{Match: "run", Exit: 42})
	before := blackbox.Manifest(proj)

	stdout, stderr, code := blackbox.Run(t, blackbox.RunOpts{
		Bin:   launcher.Primary,
		Args:  []string{"--container-runtime=docker", "-N", "-s", "-C", proj},
		Home:  home,
		Stubs: stubs,
	})

	if code != 42 {
		t.Errorf("exit %d, want 42 (child status not propagated)\nstderr:\n%s", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if stubs.InvokedBins(t)["container"] {
		t.Error("the container runtime was invoked, but only docker was selected")
	}

	events := stubs.Events(t)
	infoIdx := firstEventIndex(events, "info")
	psIdx := firstEventIndex(events, "ps")
	runIdx := firstEventIndex(events, "run")
	if infoIdx < 0 {
		t.Error("docker info (liveness probe) was never invoked")
	}
	if psIdx < 0 {
		t.Error("docker ps (discovery) was never invoked")
	}
	if runIdx < 0 {
		t.Fatal("docker run was never invoked")
	}
	if infoIdx >= 0 && infoIdx > runIdx {
		t.Errorf("docker run (event %d) preceded the liveness probe (event %d)", runIdx, infoIdx)
	}

	runArgv := events[runIdx].Argv
	resolvedProj := host.ResolvePath(proj)
	for _, want := range []string{"-w", resolvedProj, "claude-contained:latest", "/usr/local/bin/shell-run"} {
		if !containsString(runArgv, want) {
			t.Errorf("docker run argv is missing %q\nargv: %v", want, runArgv)
		}
	}

	// The launch itself leaves the project directory untouched: everything the
	// launcher writes lands in HOME (the contained profile) or is passed to the
	// runtime as a mount, never scribbled into the project.
	blackbox.AssertUnchanged(t, proj, before)
}

// TestArtifactUsageErrorIsSilentAndInvokesNoRuntime proves the shipped binary's
// early-exit discipline on the error paths, not just on --help: an unknown flag
// exits 2 with its exact diagnostic on stderr alone, prints nothing to stdout,
// starts no runtime, and leaves the host untouched.
func TestArtifactUsageErrorIsSilentAndInvokesNoRuntime(t *testing.T) {
	launcher := blackbox.BuildLauncher(t)
	home := t.TempDir()
	stubs := blackbox.NewStubs(t, "docker", "container")
	before := blackbox.Manifest(home)

	stdout, stderr, code := blackbox.Run(t, blackbox.RunOpts{
		Bin:   launcher.Primary,
		Args:  []string{"--wat"},
		Home:  home,
		Stubs: stubs,
	})

	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	want := "error: unknown flag: --wat\n       run 'claude-contained --help' for the supported flags\n"
	if stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if bins := stubs.InvokedBins(t); len(bins) != 0 {
		t.Errorf("a runtime was invoked on a usage error: %v", bins)
	}
	blackbox.AssertUnchanged(t, home, before)
}

// signalCase is one termination signal and the launcher's conventional 128+n
// status for it.
type signalCase struct {
	name string
	sig  syscall.Signal
	want int
}

var terminationSignals = []signalCase{
	{"INT", syscall.SIGINT, 130},
	{"TERM", syscall.SIGTERM, 143},
	{"HUP", syscall.SIGHUP, 129},
}

// TestArtifactInheritedSignalKillsChildAndUnlocks proves the property no
// in-process test can: when the launcher and its foreground child share a
// process group and the group is signaled, the child dies of the signal's
// default disposition (so the launcher's wait returns instead of hanging
// forever on a child that inherited an ignored disposition), the launcher exits
// 128+signal, and the worktree lock it held is released.
//
// The child blocks on a FIFO rather than sleeping, so a launcher that wrongly
// handed it an ignored disposition would hang the whole run and trip the hang
// guard -- a stronger, sleepless assertion than any timed sleep.
func TestArtifactInheritedSignalKillsChildAndUnlocks(t *testing.T) {
	launcher := blackbox.BuildLauncher(t)
	for _, tc := range terminationSignals {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			mainRepo, hiddenWT := hiddenWorktreeFixture(t)
			stubs := blackbox.NewStubs(t, "docker", "container")
			ready := filepath.Join(stubs.Dir, "ready")
			fifo := blackbox.MakeFIFO(t, stubs.Dir, "run.fifo")
			stubs.Arm(t, "docker", blackbox.ArmConfig{Match: "run", ReadyFile: ready, BlockOnFIFO: fifo})

			p := blackbox.Start(t, blackbox.RunOpts{
				Bin:     launcher.Primary,
				Args:    []string{"--container-runtime=docker", "-N", "-s", "-C", mainRepo, "-W"},
				Home:    home,
				Stubs:   stubs,
				OwnPgid: true,
			})
			t.Cleanup(p.Kill)

			if !blackbox.WaitForFile(ready, 20*time.Second) {
				t.Fatalf("the stubbed container run never started\nstderr:\n%s", p.Stderr.String())
			}
			if !worktreeIsLocked(t, mainRepo, hiddenWT) {
				t.Error("the hidden worktree was not locked while the container ran")
			}

			start := time.Now()
			p.SignalGroup(t, tc.sig)
			if !p.WaitFor(20 * time.Second) {
				t.Fatal("launcher hung after the group signal: its child likely inherited an ignored disposition")
			}
			if got := p.Code(); got != tc.want {
				t.Errorf("exit %d, want %d", got, tc.want)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("launcher took %v to exit after the signal; the child did not die promptly", elapsed)
			}
			if worktreeIsLocked(t, mainRepo, hiddenWT) {
				t.Error("the worktree is still locked after the launcher exited: the lock leaked")
			}
		})
	}
}

// TestArtifactSignalDeferredToChild proves the complementary property: a signal
// delivered to the launcher alone (not the group) is deferred until the
// foreground child returns on its own, rather than killing the child or exiting
// immediately. The launcher must stay alive after the signal, the child must
// complete naturally once released, and only then does the launcher exit with
// the signal's status.
func TestArtifactSignalDeferredToChild(t *testing.T) {
	launcher := blackbox.BuildLauncher(t)
	for _, tc := range terminationSignals {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			mainRepo, hiddenWT := hiddenWorktreeFixture(t)
			stubs := blackbox.NewStubs(t, "docker", "container")
			ready := filepath.Join(stubs.Dir, "ready")
			done := filepath.Join(stubs.Dir, "done")
			fifo := blackbox.MakeFIFO(t, stubs.Dir, "run.fifo")
			stubs.Arm(t, "docker", blackbox.ArmConfig{Match: "run", ReadyFile: ready, BlockOnFIFO: fifo, DoneFile: done})

			p := blackbox.Start(t, blackbox.RunOpts{
				Bin:     launcher.Primary,
				Args:    []string{"--container-runtime=docker", "-N", "-s", "-C", mainRepo, "-W"},
				Home:    home,
				Stubs:   stubs,
				OwnPgid: true,
			})
			t.Cleanup(p.Kill)

			if !blackbox.WaitForFile(ready, 20*time.Second) {
				t.Fatalf("the stubbed container run never started\nstderr:\n%s", p.Stderr.String())
			}

			p.Signal(t, tc.sig) // the launcher only, not the group.

			if !p.StaysRunning(300 * time.Millisecond) {
				t.Fatal("launcher exited before the child completed: the signal was acted on immediately, not deferred")
			}
			if _, err := os.Stat(done); err == nil {
				t.Fatal("the child completed before it was released: test setup is wrong")
			}

			blackbox.ReleaseFIFO(t, fifo)

			if !p.WaitFor(20 * time.Second) {
				t.Fatal("launcher hung after the child was released")
			}
			if got := p.Code(); got != tc.want {
				t.Errorf("exit %d, want %d", got, tc.want)
			}
			if _, err := os.Stat(done); err != nil {
				t.Error("the child did not complete naturally: the launcher killed it instead of deferring the signal")
			}
			if worktreeIsLocked(t, mainRepo, hiddenWT) {
				t.Error("the worktree is still locked after the launcher exited: the lock leaked")
			}
		})
	}
}

// --- small structural helpers ----------------------------------------------

func firstEventIndex(events []blackbox.Invocation, sub string) int {
	for i, e := range events {
		if len(e.Argv) > 0 && e.Argv[0] == sub {
			return i
		}
	}
	return -1
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
