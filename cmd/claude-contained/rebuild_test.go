package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"claude-contained/internal/cli"
	"claude-contained/internal/host"
	"claude-contained/internal/plan"
	"claude-contained/internal/runtime"
)

// fixedNow is the clock rebuildAttempts formats into the cache-bust token,
// chosen to match the plan's worked examples (§6).
var fixedNow = time.Date(2026, 7, 29, 21, 15, 7, 0, time.UTC)

// recordingRunner returns a runner that logs every argv it is given, in
// order, and returns codes[i] for the i-th call (the last code repeats once
// codes is exhausted, so a single-attempt test can pass one element).
func recordingRunner(codes ...int) (*[][]string, runner) {
	var calls [][]string
	r := func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
		calls = append(calls, append([]string(nil), argv...))
		i := len(calls) - 1
		if i >= len(codes) {
			i = len(codes) - 1
		}
		return codes[i]
	}
	return &calls, r
}

func TestRebuildAttempts(t *testing.T) {
	const token = "20260729211507"

	cases := []struct {
		name string
		mode string
		ok   bool
		want []rebuildAttempt
	}{
		{
			name: "tools",
			mode: "tools",
			ok:   true,
			want: []rebuildAttempt{
				{
					Before: []string{"Rebuilding claude-contained image (AI tools refresh)..."},
					Spec:   runtime.BuildSpec{Tag: plan.Image, BuildArgs: []string{"AI_TOOLS_CACHE_BUST=" + token}},
				},
				{
					Before: []string{
						"AI tools refresh failed. Retrying with full rebuild...",
						"Rebuilding claude-contained image (full fresh rebuild)...",
					},
					Spec: runtime.BuildSpec{Tag: plan.Image, Pull: true, NoCache: true},
				},
			},
		},
		{
			name: "full",
			mode: "full",
			ok:   true,
			want: []rebuildAttempt{
				{
					Before: []string{"Rebuilding claude-contained image (full fresh rebuild)..."},
					Spec:   runtime.BuildSpec{Tag: plan.Image, Pull: true, NoCache: true},
				},
			},
		},
		{name: "empty mode", mode: "", ok: false},
		{name: "unknown mode", mode: "nonsense", ok: false},
		{name: "wrong case is not a match", mode: "Tools", ok: false},
		{name: "the sentinel is not a mode rebuildAttempts handles", mode: "none", ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := rebuildAttempts(tc.mode, fixedNow)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rebuildAttempts(%q) = %#v, want %#v", tc.mode, got, tc.want)
			}
		})
	}
}

func TestRebuildToolsSucceedsWithoutRetrying(t *testing.T) {
	ctx := t.Context()
	dir := contextDirFixture(t, true)
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runRebuild(ctx, run, runtime.NewApple(runtime.Darwin), "tools",
		host.BuildContextSources{Flag: dir}, fixedNow, strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", code, cli.ExitOK, stderr.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("recorded %d builds, want 1: %#v", len(*calls), *calls)
	}
	if (*calls)[0][1] != "build" {
		t.Errorf("argv[1] = %q, want build", (*calls)[0][1])
	}
	wantNotice := "Rebuilding claude-contained image (AI tools refresh)...\n"
	if stderr.String() != wantNotice {
		t.Errorf("stderr = %q, want %q", stderr.String(), wantNotice)
	}
}

func TestRebuildToolsRetriesAsFullRebuild(t *testing.T) {
	ctx := t.Context()
	dir := contextDirFixture(t, true)
	calls, run := recordingRunner(1, 0)
	var stdout, stderr bytes.Buffer

	code := runRebuild(ctx, run, runtime.NewApple(runtime.Darwin), "tools",
		host.BuildContextSources{Flag: dir}, fixedNow, strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", code, cli.ExitOK, stderr.String())
	}
	if len(*calls) != 2 {
		t.Fatalf("recorded %d builds, want 2: %#v", len(*calls), *calls)
	}
	if !contains(t, (*calls)[0], "--build-arg") {
		t.Errorf("first build should be the tools refresh: %v", (*calls)[0])
	}
	if !contains(t, (*calls)[1], "--pull") || !contains(t, (*calls)[1], "--no-cache") {
		t.Errorf("second build should be the full rebuild: %v", (*calls)[1])
	}

	want := "Rebuilding claude-contained image (AI tools refresh)...\n" +
		"AI tools refresh failed. Retrying with full rebuild...\n" +
		"Rebuilding claude-contained image (full fresh rebuild)...\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestRebuildRetryFailurePropagatesExitStatus(t *testing.T) {
	ctx := t.Context()
	dir := contextDirFixture(t, true)
	_, run := recordingRunner(1, 7)
	var stdout, stderr bytes.Buffer

	code := runRebuild(ctx, run, runtime.NewApple(runtime.Darwin), "tools",
		host.BuildContextSources{Flag: dir}, fixedNow, strings.NewReader(""), &stdout, &stderr)

	if code != 7 {
		t.Errorf("exit = %d, want the second build's status 7", code)
	}
}

func TestRebuildFullDoesNotRetry(t *testing.T) {
	ctx := t.Context()
	dir := contextDirFixture(t, true)
	calls, run := recordingRunner(1)
	var stdout, stderr bytes.Buffer

	code := runRebuild(ctx, run, runtime.NewApple(runtime.Darwin), "full",
		host.BuildContextSources{Flag: dir}, fixedNow, strings.NewReader(""), &stdout, &stderr)

	if code != 1 {
		t.Errorf("exit = %d, want the build's own status 1", code)
	}
	if len(*calls) != 1 {
		t.Fatalf("recorded %d builds, want exactly 1 (full does not retry): %#v", len(*calls), *calls)
	}
}

func TestRebuildUnknownModeNamesSupportedModes(t *testing.T) {
	ctx := t.Context()
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runRebuild(ctx, run, runtime.NewApple(runtime.Darwin), "nonsense",
		host.BuildContextSources{}, fixedNow, strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitFailure {
		t.Errorf("exit = %d, want %d", code, cli.ExitFailure)
	}
	want := "Unknown rebuild mode: nonsense\nSupported rebuild modes: tools, full\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
	if len(*calls) != 0 {
		t.Errorf("runner should never be called for an unknown mode: %#v", *calls)
	}
}

func TestRebuildMissingBuildContextIsDiagnosed(t *testing.T) {
	ctx := t.Context()
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runRebuild(ctx, run, runtime.NewApple(runtime.Darwin), "tools",
		host.BuildContextSources{}, fixedNow, strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitFailure {
		t.Errorf("exit = %d, want %d", code, cli.ExitFailure)
	}
	out := stderr.String()
	for _, want := range []string{cli.BuildContextFlag, host.BuildContextEnvVar, "claude-contained"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr = %q, want it to mention %q", out, want)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("runner should never be called with no build context: %#v", *calls)
	}
}

func TestRebuildBadExplicitBuildContextNamesTheSource(t *testing.T) {
	ctx := t.Context()
	badDir := contextDirFixture(t, false)

	t.Run("flag", func(t *testing.T) {
		_, run := recordingRunner(0)
		var stdout, stderr bytes.Buffer
		code := runRebuild(ctx, run, runtime.NewApple(runtime.Darwin), "tools",
			host.BuildContextSources{Flag: badDir}, fixedNow, strings.NewReader(""), &stdout, &stderr)
		if code != 2 {
			t.Errorf("exit = %d, want 2", code)
		}
		want := "error: --build-context has no Dockerfile: " + badDir + "\n"
		if stderr.String() != want {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
	})

	t.Run("env", func(t *testing.T) {
		_, run := recordingRunner(0)
		var stdout, stderr bytes.Buffer
		code := runRebuild(ctx, run, runtime.NewApple(runtime.Darwin), "tools",
			host.BuildContextSources{Env: badDir}, fixedNow, strings.NewReader(""), &stdout, &stderr)
		if code != 2 {
			t.Errorf("exit = %d, want 2", code)
		}
		want := "error: " + host.BuildContextEnvVar + " has no Dockerfile: " + badDir + "\n"
		if stderr.String() != want {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
	})
}

// Pins §4.2's ordering choice: the mode is checked before the build context,
// so a command line with both wrong reports the mode alone.
func TestRebuildModeCheckedBeforeBuildContext(t *testing.T) {
	ctx := t.Context()
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runRebuild(ctx, run, runtime.NewApple(runtime.Darwin), "nonsense",
		host.BuildContextSources{}, fixedNow, strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitFailure {
		t.Errorf("exit = %d, want %d", code, cli.ExitFailure)
	}
	if !strings.Contains(stderr.String(), "Unknown rebuild mode") {
		t.Errorf("stderr = %q, want the mode message, not a build-context diagnosis", stderr.String())
	}
	if strings.Contains(stderr.String(), "Dockerfile") || strings.Contains(stderr.String(), "checkout") {
		t.Errorf("stderr = %q, must not also report the missing build context", stderr.String())
	}
	if len(*calls) != 0 {
		t.Errorf("runner should never be called: %#v", *calls)
	}
}

func TestRebuildRendersPerRuntime(t *testing.T) {
	dir := contextDirFixture(t, true)

	appleCalls, appleRun := recordingRunner(0)
	var out, errw bytes.Buffer
	runRebuild(t.Context(), appleRun, runtime.NewApple(runtime.Darwin), "tools",
		host.BuildContextSources{Flag: dir}, fixedNow, strings.NewReader(""), &out, &errw)

	dockerCalls, dockerRun := recordingRunner(0)
	out.Reset()
	errw.Reset()
	runRebuild(t.Context(), dockerRun, runtime.NewDocker(runtime.Linux), "tools",
		host.BuildContextSources{Flag: dir}, fixedNow, strings.NewReader(""), &out, &errw)

	if (*appleCalls)[0][0] != "container" {
		t.Errorf("apple argv[0] = %q, want container", (*appleCalls)[0][0])
	}
	if (*dockerCalls)[0][0] != "docker" {
		t.Errorf("docker argv[0] = %q, want docker", (*dockerCalls)[0][0])
	}
	if !reflect.DeepEqual((*appleCalls)[0][1:], (*dockerCalls)[0][1:]) {
		t.Errorf("apple and docker should render identically apart from argv[0]:\napple:  %v\ndocker: %v",
			(*appleCalls)[0][1:], (*dockerCalls)[0][1:])
	}
}

// --- fixtures ---------------------------------------------------------------

func contextDirFixture(t *testing.T, withDockerfile bool) string {
	t.Helper()
	dir := t.TempDir()
	if withDockerfile {
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func contains(t *testing.T, haystack []string, needle string) bool {
	t.Helper()
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
