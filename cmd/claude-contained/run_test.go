package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"claude-contained/internal/host"
	"claude-contained/internal/layer"
	"claude-contained/internal/plan"
	"claude-contained/internal/runtime"
)

// --- fixtures -------------------------------------------------------------

// writeStubContainer puts a fake `container` binary on a fresh PATH entry so
// runtime.Apple's EnsureUp ("container system status") and List ("container
// list --quiet") succeed without a real Apple Containers install. The actual
// container *run* never reaches this stub: the tests inject their own
// `runner` in place of execRuntime.
//
// `list` echoes $STUB_LIST, one name per line, when set -- letting attach
// tests report a running container. Unset (the common case), it stays silent,
// so every pre-existing caller of this fixture is unaffected.
//
// `inspect` echoes $STUB_INSPECT verbatim -- the Apple JSON shape
// runtime.Apple.InspectEnv parses -- letting Zellij driver tests present a
// marked container. Unset, it stays silent, same as `list`.
//
// `image` answers Runtime.DescribeImage; see stubImageArm.
func writeStubContainer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"  system) exit 0 ;;\n" +
		"  list) [ -n \"$STUB_LIST\" ] && printf '%s\\n' \"$STUB_LIST\"; exit 0 ;;\n" +
		"  inspect) [ -n \"$STUB_INSPECT\" ] && printf '%s' \"$STUB_INSPECT\"; exit 0 ;;\n" +
		// Apple Containers' inspect output is JSON; parseAppleImageID reads it.
		stubImageArm("    printf '[{\"descriptor\":{\"digest\":\"%s\"}}]\\n' \"$(cat \"$idfile\")\"\n") +
		"  *) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "container"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// stubImageArm is the `image inspect` arm both stubs share, parameterized only
// by the line that renders a present image's identifier.
//
// It implements Runtime.DescribeImage's contract rather than relying on the scripts'
// `*) exit 0 ;;` default: that default means "exit 0 with no output", which
// probeImageID classifies as a *fault*, so a stub without this arm would turn
// every layer probe into a runtime error rather than the absence it means.
//
//   - $STUB_IMAGE_LOG, when set, records every invocation, which is how a test
//     asserts that a no-layer run never probes an image at all.
//   - `--help` exits $STUB_IMAGE_HELP_EXIT (0 by default). Setting it nonzero
//     is how a test presents a CLI that does not have the subcommand, which
//     must be a named fault and never a false absence.
//   - Otherwise the last argument is the reference, and an id file under
//     $STUB_IMAGE_ID_DIR decides present (exit 0, print it) or absent (exit 1).
func stubImageArm(render string) string {
	return "  image)\n" +
		"    [ -n \"${STUB_IMAGE_LOG:-}\" ] && printf '%s\\n' \"$*\" >> \"$STUB_IMAGE_LOG\"\n" +
		"    for a in \"$@\"; do\n" +
		"      [ \"$a\" = --help ] && exit \"${STUB_IMAGE_HELP_EXIT:-0}\"\n" +
		"    done\n" +
		"    ref=\"\"\n" +
		"    for a in \"$@\"; do ref=\"$a\"; done\n" +
		"    idfile=\"${STUB_IMAGE_ID_DIR:-}/$(printf '%s' \"$ref\" | tr ':/' '__').id\"\n" +
		"    [ -f \"$idfile\" ] || exit 1\n" +
		render +
		"    exit 0 ;;\n"
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// hiddenWorktreeFixture builds a real main repo (the project directory) with
// one linked worktree outside it and outside any mounted root -- the prune
// hazard the auto-lock offer exists to protect.
func hiddenWorktreeFixture(t *testing.T) (project, wt string) {
	t.Helper()
	base := host.ResolvePath(t.TempDir())
	project = filepath.Join(base, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, project, "init", "-q")
	runGitTest(t, project, "-c", "user.email=t@example.com", "-c", "user.name=test", "commit", "-q", "--allow-empty", "-m", "init")

	wt = filepath.Join(base, "hidden-wt")
	runGitTest(t, project, "worktree", "add", "-q", "--detach", wt)
	return project, host.ResolvePath(wt)
}

// lockInfo reports a worktree's lock reason and whether it is locked at all,
// reading git's own porcelain output rather than the lock file directly.
func lockInfo(t *testing.T, main, wt string) (reason string, locked bool) {
	t.Helper()
	out := runGitTest(t, main, "worktree", "list", "--porcelain")
	var cur string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			cur = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "locked") && cur == wt:
			locked = true
			reason = strings.TrimPrefix(strings.TrimPrefix(line, "locked"), " ")
		}
	}
	return reason, locked
}

func worktreeIsLocked(t *testing.T, main, wt string) bool {
	t.Helper()
	_, locked := lockInfo(t, main, wt)
	return locked
}

// launcherArgv drives the launcher through the auto-lock offer with no
// interaction required: -W locks unconditionally, -N and -s skip the
// node-modules and tool-selection prompts respectively.
func launcherArgv(project string) []string {
	return []string{"claude-contained", "-s", "-N", "-W", "-C", project}
}

// writeStubDocker is writeStubContainer's Docker twin: `info` stands in for
// `system status` and `ps` for `list`. Installed in the same directory so a test
// can select either runtime without knowing which binary gets called.
func writeStubDocker(t *testing.T, dir string) {
	t.Helper()
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"  info) exit 0 ;;\n" +
		"  ps) [ -n \"$STUB_LIST\" ] && printf '%s\\n' \"$STUB_LIST\"; exit 0 ;;\n" +
		"  inspect) [ -n \"$STUB_INSPECT\" ] && printf '%s' \"$STUB_INSPECT\"; exit 0 ;;\n" +
		// Docker's --format {{.Id}} output is the bare identifier.
		stubImageArm("    cat \"$idfile\"\n") +
		"  *) exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// withStubbedHostAndPath points HOME at a scratch directory and prepends stub
// `container` and `docker` binaries to PATH, both restored by testing.T's
// cleanup.
//
// Both stubs are installed because runWith's platform is a *parameter*: a test
// that selects Docker must not fall through to a real daemon. On CI's Linux
// runner that daemon exists and answers, which would make these tests pass or
// fail for reasons unrelated to their assertions.
func withStubbedHostAndPath(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	stubDir := writeStubContainer(t)
	writeStubDocker(t, stubDir)
	t.Setenv("HOME", home)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONTAINED_LOG_LEVEL", "")
	return stubDir
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// --- Step 9: ordering tests ------------------------------------------------

// The property under test is the *sequence*, not just the end state: the
// worktree must be observably locked while the container is up, and unlocked
// only once runWith has returned.
func TestLocksReleasedOnlyAfterContainerExit(t *testing.T) {
	project, wt := hiddenWorktreeFixture(t)
	withStubbedHostAndPath(t)

	var events []string
	fake := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
		events = append(events, "container-start")
		events = append(events, "locked="+boolStr(worktreeIsLocked(t, project, wt)))
		events = append(events, "container-exit")
		return 0
	}

	var stdout, stderr bytes.Buffer
	code := runWith(fake, runtime.Darwin, launcherArgv(project), strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runWith exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}

	want := []string{"container-start", "locked=true", "container-exit"}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("events = %v, want %v\nstderr:\n%s", events, want, stderr.String())
	}
	if worktreeIsLocked(t, project, wt) {
		t.Error("worktree should be unlocked once runWith returns")
	}
}

// A signal caught mid-run must not release the locks until the container's
// own runner returns, and the exit status must match bash's trap handlers.
func TestSignalDuringRunReleasesAfterExit(t *testing.T) {
	cases := []struct {
		name string
		sig  syscall.Signal
		want int
	}{
		{"SIGINT", syscall.SIGINT, 130},
		{"SIGTERM", syscall.SIGTERM, 143},
		{"SIGHUP", syscall.SIGHUP, 129},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project, wt := hiddenWorktreeFixture(t)
			withStubbedHostAndPath(t)

			var lockedDuringRun bool
			fake := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
				if err := syscall.Kill(os.Getpid(), tc.sig); err != nil {
					t.Errorf("kill: %v", err)
				}
				// Give the signal handler time to land before the runner
				// returns -- the whole point is observing the lock still
				// held *during* the run, not just afterward.
				time.Sleep(50 * time.Millisecond)
				lockedDuringRun = worktreeIsLocked(t, project, wt)
				return 0
			}

			var stdout, stderr bytes.Buffer
			code := runWith(fake, runtime.Darwin, launcherArgv(project), strings.NewReader(""), &stdout, &stderr)

			if !lockedDuringRun {
				t.Error("lock should still be held while the container is up, mid-signal")
			}
			if code != tc.want {
				t.Errorf("exit code = %d, want %d\nstderr:\n%s", code, tc.want, stderr.String())
			}
			if worktreeIsLocked(t, project, wt) {
				t.Error("worktree should be unlocked once runWith returns")
			}
		})
	}
}

// A lock the user set by hand must survive the run untouched, even while a
// different, truly-hidden worktree in the same repository is auto-locked and
// released normally.
func TestUserLockSurvivesAcrossRun(t *testing.T) {
	base := host.ResolvePath(t.TempDir())
	project := filepath.Join(base, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, project, "init", "-q")
	runGitTest(t, project, "-c", "user.email=t@example.com", "-c", "user.name=test", "commit", "-q", "--allow-empty", "-m", "init")

	userWT := filepath.Join(base, "user-wt")
	runGitTest(t, project, "worktree", "add", "-q", "--detach", userWT)
	userWT = host.ResolvePath(userWT)
	runGitTest(t, project, "worktree", "lock", "--reason", "mine", userWT)

	hiddenWT := filepath.Join(base, "hidden-wt")
	runGitTest(t, project, "worktree", "add", "-q", "--detach", hiddenWT)
	hiddenWT = host.ResolvePath(hiddenWT)

	withStubbedHostAndPath(t)

	fake := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int { return 0 }
	var stdout, stderr bytes.Buffer
	code := runWith(fake, runtime.Darwin, launcherArgv(project), strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runWith exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}

	reason, locked := lockInfo(t, project, userWT)
	if !locked || reason != "mine" {
		t.Fatalf("user lock = %q locked=%v, want unchanged \"mine\"", reason, locked)
	}
	if worktreeIsLocked(t, project, hiddenWT) {
		t.Error("the truly-hidden worktree should be unlocked again after the run")
	}
}

// A lock this run shares with another still-running container's owner token
// must keep that owner after our release -- only the last owner leaving
// actually unlocks.
func TestOtherOwnerSurvivesAcrossRun(t *testing.T) {
	project, wt := hiddenWorktreeFixture(t)
	runGitTest(t, project, "worktree", "lock", "--reason", "cc-autolocked-by: aic-other-1111", wt)

	withStubbedHostAndPath(t)

	fake := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int { return 0 }
	var stdout, stderr bytes.Buffer
	code := runWith(fake, runtime.Darwin, launcherArgv(project), strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runWith exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}

	reason, locked := lockInfo(t, project, wt)
	if !locked {
		t.Fatal("worktree should still be locked: aic-other-1111 remains an owner")
	}
	if !strings.Contains(reason, "aic-other-1111") {
		t.Fatalf("lock reason = %q, want it to still contain aic-other-1111", reason)
	}
}

// --- ticket 10: rebuild dispatch and the deleted update check --------------

// rebuildContextFixture returns a directory holding a Dockerfile, passed via
// --build-context. Self-location cannot be exercised end to end here: under
// `go test`, os.Executable() resolves to a scratch build directory outside any
// checkout (internal/host/buildcontext_test.go covers self-location directly,
// against real fixture checkouts).
func rebuildContextFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRebuildExitsWithoutStartingASession(t *testing.T) {
	withStubbedHostAndPath(t)
	bc := rebuildContextFixture(t)

	var calls [][]string
	rec := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
		calls = append(calls, argv)
		return 0
	}

	var stdout, stderr bytes.Buffer
	code := runWith(rec, runtime.Darwin, []string{"claude-contained", "-R", "--build-context", bc}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if len(calls) != 1 {
		t.Fatalf("recorded %d runtime invocations, want exactly 1: %#v", len(calls), calls)
	}
	if calls[0][1] != "build" {
		t.Errorf("argv[1] = %q, want build", calls[0][1])
	}
	for _, a := range calls[0] {
		if a == "run" || strings.HasPrefix(a, "aic-") {
			t.Errorf("rebuild must never start a session: %v", calls[0])
		}
	}
}

func TestRebuildIgnoresTheProjectDirectory(t *testing.T) {
	withStubbedHostAndPath(t)
	bc := rebuildContextFixture(t)

	rec := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int { return 0 }

	var stdout, stderr bytes.Buffer
	code := runWith(rec, runtime.Darwin,
		[]string{"claude-contained", "-R", "--build-context", bc, "-C", "/nonexistent/definitely"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 -- bash rebuilds before it ever resolves the project directory\nstderr:\n%s", code, stderr.String())
	}
}

func TestRebuildNoneStartsASession(t *testing.T) {
	project := t.TempDir()
	withStubbedHostAndPath(t)

	var calls [][]string
	rec := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
		calls = append(calls, argv)
		return 0
	}

	var stdout, stderr bytes.Buffer
	code := runWith(rec, runtime.Darwin, []string{"claude-contained", "-R", "none", "-s", "-N", "-C", project}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if len(calls) != 1 || calls[0][1] != "run" {
		t.Fatalf("calls = %#v, want exactly one `run`: -R none is the typable sentinel and starts a session", calls)
	}
}

func TestRebuildUsesTheSelectedRuntime(t *testing.T) {
	withStubbedHostAndPath(t)
	bc := rebuildContextFixture(t)

	var calls [][]string
	rec := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
		calls = append(calls, argv)
		return 0
	}

	var stdout, stderr bytes.Buffer
	code := runWith(rec, runtime.Darwin,
		[]string{"claude-contained", "--container-runtime=docker", "-R", "--build-context", bc},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if len(calls) != 1 || calls[0][0] != "docker" {
		t.Fatalf("calls = %#v, want argv[0] = docker", calls)
	}
}

// TestNoUpdateCheckAfterARun guards against the deletion in §4.5 being
// reintroduced: after a full run, stdout says nothing about an update, and
// nothing fetched -- the project directory's .git/FETCH_HEAD is untouched.
func TestNoUpdateCheckAfterARun(t *testing.T) {
	project := host.ResolvePath(t.TempDir())
	runGitTest(t, project, "init", "-q")
	runGitTest(t, project, "-c", "user.email=t@example.com", "-c", "user.name=test", "commit", "-q", "--allow-empty", "-m", "init")

	withStubbedHostAndPath(t)

	fetchHead := filepath.Join(project, ".git", "FETCH_HEAD")
	before, beforeErr := os.Stat(fetchHead)

	fake := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int { return 0 }
	var stdout, stderr bytes.Buffer
	code := runWith(fake, runtime.Darwin, []string{"claude-contained", "-s", "-N", "-C", project}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}

	if strings.Contains(stdout.String(), "Update available") {
		t.Errorf("stdout should never mention an update: %q", stdout.String())
	}

	after, afterErr := os.Stat(fetchHead)
	switch {
	case beforeErr == nil && (afterErr != nil || !after.ModTime().Equal(before.ModTime())):
		t.Error("FETCH_HEAD changed during the run: something fetched")
	case beforeErr != nil && afterErr == nil:
		t.Error("FETCH_HEAD appeared during the run: something fetched")
	}
}

// Checklist item 12 as a property rather than as a diff against fifteen golden
// files: a project with no tooling layer produces the same run, naming the base
// image, in every runtime/platform configuration. The golden suite asserts the
// same thing byte for byte; this states *why* it holds, so a future change that
// breaks it fails with a sentence instead of a wall of golden diff.
func TestNoLayerRunSpecIsRuntimeIndependent(t *testing.T) {
	for _, gc := range goldenTrees {
		t.Run(gc.tree, func(t *testing.T) {
			project := host.ResolvePath(t.TempDir())
			withStubbedHostAndPath(t)
			// Explicit rather than inherited: the point of the case is that
			// nothing outside the argv decides which image is run.
			t.Setenv(host.LayerEnvVar, "")
			t.Setenv("CLAUDE_CONTAINED_RUNTIME", "")
			if gc.dockerEnv {
				t.Setenv("CLAUDE_CONTAINED_RUNTIME", "docker")
			}

			var calls [][]string
			fake := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
				calls = append(calls, append([]string(nil), argv...))
				return 0
			}

			var stdout, stderr bytes.Buffer
			code := runWith(fake, gc.plat,
				[]string{"claude-contained", "-N", "-s", "-C", project},
				strings.NewReader(""), &stdout, &stderr)

			if code != 0 {
				t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
			}
			if len(calls) != 1 {
				t.Fatalf("recorded %d runner calls, want exactly the container run: %#v", len(calls), calls)
			}
			if !contains(t, calls[0], plan.Image) {
				t.Errorf("run argv must carry %q and nothing derived: %v", plan.Image, calls[0])
			}
			for _, a := range calls[0] {
				if strings.HasPrefix(a, layer.Repo+":") {
					t.Errorf("a project with no layer must never name a derived image: %q", a)
				}
			}
		})
	}
}

// A zero-byte srt placeholder that appears *during* the container run is swept
// by the deferred post-run cleanup (run.go:324-326), not by the pre-run sweeps
// (run.go:236, :290) a golden case can reach. Golden 50 seeds its placeholders
// before the run, so it only exercises the pre-run sweep; this drives the
// deferred one by having the injected runner create the file mid-run, the way
// the retired startup-diagnostics.test.sh drove it with SRT_STUB_PLACEHOLDER_ROOTS.
// A pre-seeded non-empty placeholder-named file rides along to keep the negative
// side: the sweep removes only the zero-byte one.
func TestPlaceholderCreatedDuringRunIsSweptAfterExit(t *testing.T) {
	project := host.ResolvePath(t.TempDir())
	withStubbedHostAndPath(t)

	survivor := filepath.Join(project, ".zshrc")
	if err := os.WriteFile(survivor, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(project, ".mcp.json")

	fake := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
		if err := os.WriteFile(created, nil, 0o644); err != nil {
			t.Errorf("seeding the mid-run placeholder: %v", err)
		}
		return 0
	}

	var stdout, stderr bytes.Buffer
	code := runWith(fake, runtime.Darwin, []string{"claude-contained", "-s", "-N", "-C", project}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runWith exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}

	if _, err := os.Lstat(created); !os.IsNotExist(err) {
		t.Errorf(".mcp.json created during the run should have been swept by the deferred cleanup (err=%v)", err)
	}
	if _, err := os.Lstat(survivor); err != nil {
		t.Errorf("a non-empty placeholder-named file must survive (err=%v)", err)
	}
}
