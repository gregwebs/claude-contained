package runtime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// EnsureUp is driven entirely through PATH stubs, so every case runs identically
// on a macOS developer machine and on CI's Linux runner -- including the
// Docker-on-Linux refusal, which no test could otherwise reach from here.

// ensureUpStubs installs `container`, `docker` and `open` stubs on a fresh PATH
// entry and returns the directory. failures is how many times the liveness probe
// (`docker info` / `container system status`) fails before it starts succeeding; a
// negative count fails forever.
func ensureUpStubs(t *testing.T, failures int) string {
	t.Helper()
	dir := t.TempDir()
	countFile := filepath.Join(dir, "failures")
	if err := os.WriteFile(countFile, []byte(strconv.Itoa(failures)), 0o600); err != nil {
		t.Fatal(err)
	}

	// probe() exits 1 while the countdown is positive, decrementing as it goes; a
	// negative count never recovers.
	probe := `
n=$(cat ` + countFile + `)
if [ "$n" -lt 0 ]; then exit 1; fi
if [ "$n" -gt 0 ]; then echo $((n - 1)) > ` + countFile + `; exit 1; fi
exit 0`

	writeStub(t, dir, "docker", `#!/bin/sh
if [ "$1" = info ]; then`+probe+`
fi
exit 0
`)
	writeStub(t, dir, "container", `#!/bin/sh
if [ "$1" = system ] && [ "$2" = status ]; then`+probe+`
fi
if [ "$1" = system ] && [ "$2" = start ]; then
  echo "started on stdout"
  echo "started on stderr" >&2
  touch `+filepath.Join(dir, "start.marker")+`
  exit 0
fi
exit 0
`)
	writeStub(t, dir, "open", `#!/bin/sh
printf '%s\n' "$*" > `+filepath.Join(dir, "open.argv")+`
exit 0
`)

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func fileMissing(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		t.Errorf("%s should not have happened", what)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// shrinkPollInterval makes the darwin poll loop finish in test time.
func shrinkPollInterval(t *testing.T) {
	t.Helper()
	previous := dockerPollInterval
	dockerPollInterval = time.Millisecond
	t.Cleanup(func() { dockerPollInterval = previous })
}

func refuseToConfirm(t *testing.T) func(string) bool {
	t.Helper()
	return func(string) bool {
		t.Error("confirm should not have been called")
		return false
	}
}

func TestDockerEnsureUpAlreadyRunning(t *testing.T) {
	dir := ensureUpStubs(t, 0)
	var stdout, stderr bytes.Buffer

	if err := NewDocker(Darwin).EnsureUp(context.Background(), &stdout, &stderr, refuseToConfirm(t)); err != nil {
		t.Fatalf("EnsureUp: %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("a running daemon should say nothing, got stdout %q stderr %q", stdout.String(), stderr.String())
	}
	fileMissing(t, filepath.Join(dir, "open.argv"), "opening Docker Desktop")
}

func TestDockerEnsureUpDarwinOpensAndPolls(t *testing.T) {
	dir := ensureUpStubs(t, 3) // the entry probe plus two loop probes
	shrinkPollInterval(t)
	var stdout, stderr bytes.Buffer

	err := NewDocker(Darwin).EnsureUp(context.Background(), &stdout, &stderr, func(prompt string) bool {
		if prompt != "Docker is not running. Start Docker Desktop? [Y/n] " {
			t.Errorf("prompt = %q", prompt)
		}
		return true
	})
	if err != nil {
		t.Fatalf("EnsureUp: %v", err)
	}

	// The progress line goes to stdout, matching claude-docked:871.
	if got := stdout.String(); got != "Waiting for Docker to start...\n" {
		t.Errorf("stdout = %q", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(dir, "open.argv"))); got != "-a Docker" {
		t.Errorf("open invoked with %q, want %q", got, "-a Docker")
	}
}

func TestDockerEnsureUpDarwinDeclined(t *testing.T) {
	dir := ensureUpStubs(t, -1)
	var stdout, stderr bytes.Buffer

	err := NewDocker(Darwin).EnsureUp(context.Background(), &stdout, &stderr, func(string) bool { return false })
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("EnsureUp error = %v, want ErrAborted", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("declining should print nothing here, got stdout %q stderr %q", stdout.String(), stderr.String())
	}
	fileMissing(t, filepath.Join(dir, "open.argv"), "opening Docker Desktop")
}

// Off macOS there is nothing to offer: the daemon is a service. A [Y/n] whose
// "yes" branch cannot work is worse than a diagnosis, so this refuses at once --
// deliberately diverging from claude-docked, which runs `open` on a host without
// it.
func TestDockerEnsureUpLinuxRefusesWithoutPrompting(t *testing.T) {
	dir := ensureUpStubs(t, -1)
	var stdout, stderr bytes.Buffer

	// Returning promptly is the "no unbounded wait" guarantee. Rather than guess a
	// wall-clock bound -- stub processes cost real time -- make one poll
	// unmistakable: if the refusal ever entered the loop, this would take ten
	// minutes instead of milliseconds.
	previous := dockerPollInterval
	dockerPollInterval = 10 * time.Minute
	t.Cleanup(func() { dockerPollInterval = previous })

	start := time.Now()
	err := NewDocker(Linux).EnsureUp(context.Background(), &stdout, &stderr, refuseToConfirm(t))
	elapsed := time.Since(start)

	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("EnsureUp error = %v, want ErrNotRunning", err)
	}
	if elapsed > 30*time.Second {
		t.Errorf("took %v; the refusal must not wait on a poll interval", elapsed)
	}
	want := "error: Docker is not running.\n" +
		"       Start the daemon and retry (for example: sudo systemctl start docker,\n" +
		"       or systemctl --user start docker-desktop for Docker Desktop).\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if stdout.Len() != 0 {
		t.Errorf("the refusal belongs on stderr, stdout = %q", stdout.String())
	}
	fileMissing(t, filepath.Join(dir, "open.argv"), "opening Docker Desktop")
}

// The zero-value platform takes the bash else-arm, i.e. behaves as Linux.
func TestDockerEnsureUpUnknownPlatformRefuses(t *testing.T) {
	ensureUpStubs(t, -1)
	var stdout, stderr bytes.Buffer

	err := NewDocker("").EnsureUp(context.Background(), &stdout, &stderr, refuseToConfirm(t))
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("EnsureUp error = %v, want ErrNotRunning", err)
	}
}

func TestDockerEnsureUpContextCancelled(t *testing.T) {
	ensureUpStubs(t, -1)
	shrinkPollInterval(t)
	var stdout, stderr bytes.Buffer

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewDocker(Darwin).EnsureUp(ctx, &stdout, &stderr, func(string) bool { return true })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureUp error = %v, want context.Canceled", err)
	}
}

// Apple Containers issues one start command and lets it inherit both streams, so
// its progress and any failure reach the user. Those streams are the injected
// writers, which is what makes this testable at all.
func TestAppleEnsureUpStartsAndStreams(t *testing.T) {
	dir := ensureUpStubs(t, 1)
	var stdout, stderr bytes.Buffer

	err := NewApple(Darwin).EnsureUp(context.Background(), &stdout, &stderr, func(prompt string) bool {
		if prompt != "Container system is not running. Start it? [Y/n] " {
			t.Errorf("prompt = %q", prompt)
		}
		return true
	})
	if err != nil {
		t.Fatalf("EnsureUp: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "start.marker")); err != nil {
		t.Error("container system start was never invoked")
	}
	if got := stdout.String(); got != "started on stdout\n" {
		t.Errorf("stdout = %q", got)
	}
	if got := stderr.String(); got != "started on stderr\n" {
		t.Errorf("stderr = %q", got)
	}
}

func TestAppleEnsureUpDeclined(t *testing.T) {
	dir := ensureUpStubs(t, -1)
	var stdout, stderr bytes.Buffer

	err := NewApple(Darwin).EnsureUp(context.Background(), &stdout, &stderr, func(string) bool { return false })
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("EnsureUp error = %v, want ErrAborted", err)
	}
	fileMissing(t, filepath.Join(dir, "start.marker"), "starting the container system")
}
