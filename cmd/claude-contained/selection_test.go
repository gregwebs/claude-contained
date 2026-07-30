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

// recordArgv captures the runtime argv the launcher would have executed. argv[0]
// is the discriminator these tests care about: `container` versus `docker`.
func recordArgv(got *[]string) runner {
	return func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
		*got = argv
		return 0
	}
}

func selectionProject(t *testing.T) string {
	t.Helper()
	return host.ResolvePath(t.TempDir())
}

// Selection end to end, through the argv actually emitted. -W is omitted because
// the project directory is not a git repository here; -s and -N skip the prompts.
func TestRuntimeSelectedByFlagAndEnv(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		args    []string
		plat    runtime.Platform
		wantBin string
	}{
		{"default on darwin is apple", "", nil, runtime.Darwin, "container"},
		{"flag selects docker", "", []string{"--container-runtime=docker"}, runtime.Darwin, "docker"},
		{"flag with a separate value", "", []string{"--container-runtime", "docker"}, runtime.Darwin, "docker"},
		{"env selects docker", "docker", nil, runtime.Darwin, "docker"},
		{"flag beats env", "docker", []string{"--container-runtime=apple"}, runtime.Darwin, "container"},
		{"env beats argv0 default", "docker", nil, runtime.Darwin, "docker"},
		// The platform default, which is the fix for Linux hosts: argv[0] here is
		// "claude-contained", and it still must not select Apple Containers.
		{"platform default on linux is docker", "", nil, runtime.Linux, "docker"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withStubbedHostAndPath(t)
			t.Setenv("CLAUDE_CONTAINED_RUNTIME", tc.env)
			project := selectionProject(t)

			argv := append([]string{"claude-contained", "-s", "-N", "-C", project}, tc.args...)
			var got []string
			var stdout, stderr bytes.Buffer

			if code := runWith(recordArgv(&got), tc.plat, argv, strings.NewReader(""), &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d, stderr: %s", code, stderr.String())
			}
			if len(got) == 0 {
				t.Fatal("no runtime argv was emitted")
			}
			if got[0] != tc.wantBin {
				t.Errorf("ran %q, want %q (full argv: %v)", got[0], tc.wantBin, got)
			}
		})
	}
}

// This is the test that licenses Apple.EnsureUp to assume a macOS host: the
// refusal happens before any container command can run.
func TestAppleRuntimeRefusedOffMacOS(t *testing.T) {
	stubDir := withStubbedHostAndPath(t)
	project := selectionProject(t)

	// Replace the stub with one that records having been called at all.
	marker := filepath.Join(stubDir, "container.called")
	script := "#!/bin/sh\ntouch " + marker + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(stubDir, "container"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	argv := []string{"claude-go", "--container-runtime=apple", "-s", "-N", "-C", project}
	code := runWith(failRunner(t), runtime.Linux, argv, strings.NewReader(""), &stdout, &stderr)

	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	want := "error: the apple container runtime is available only on macOS\n" +
		"       use --container-runtime=docker or CLAUDE_CONTAINED_RUNTIME=docker\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a container command ran under a runtime that was about to be refused")
	}
}

func TestInvalidRuntimeValueIsRefused(t *testing.T) {
	cases := []struct {
		name string
		env  string
		args []string
		want string
	}{
		{"bad flag", "", []string{"--container-runtime=bogus"},
			"error: --container-runtime must be apple or docker: bogus\n"},
		{"bad env", "bogus", nil,
			"error: CLAUDE_CONTAINED_RUNTIME must be apple or docker: bogus\n"},
		// A valid flag rescues a broken environment variable, which is what "the
		// flag wins" has to mean to be useful.
		{"valid flag rescues a bad env", "bogus", []string{"--container-runtime=docker"}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withStubbedHostAndPath(t)
			t.Setenv("CLAUDE_CONTAINED_RUNTIME", tc.env)
			project := selectionProject(t)

			argv := append([]string{"claude-go", "-s", "-N", "-C", project}, tc.args...)
			var got []string
			var stdout, stderr bytes.Buffer
			code := runWith(recordArgv(&got), runtime.Darwin, argv, strings.NewReader(""), &stdout, &stderr)

			if tc.want == "" {
				if code != 0 {
					t.Fatalf("exit %d, stderr: %s", code, stderr.String())
				}
				return
			}
			if code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if stderr.String() != tc.want {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.want)
			}
		})
	}
}

// -h wins wherever it appears, exactly as in bash, so validation runs after it.
func TestHelpWinsOverInvalidRuntime(t *testing.T) {
	withStubbedHostAndPath(t)

	var stdout, stderr bytes.Buffer
	argv := []string{"claude-go", "--container-runtime=bogus", "--help"}
	if code := runWith(failRunner(t), runtime.Darwin, argv, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, want 0; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "--container-runtime") {
		t.Error("help text was not printed")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// The help text belongs to the *selected* runtime, so an apple selection off
// macOS still describes Apple Containers before being refused.
//
// The discriminator is each runtime's description line, not the program name:
// ticket 11 gave both runtimes the same name (internal/runtime.ProgName), so
// the name is no longer a valid way to tell which runtime's help was printed.
func TestHelpDescribesTheSelectedRuntime(t *testing.T) {
	withStubbedHostAndPath(t)

	for _, tc := range []struct{ flag, want string }{
		{"--container-runtime=docker", "Docker container"},
		{"--container-runtime=apple", "Apple Containers sandbox"},
	} {
		var stdout, stderr bytes.Buffer
		argv := []string{"claude-contained", tc.flag, "--help"}
		if code := runWith(failRunner(t), runtime.Darwin, argv, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("exit %d; stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), tc.want) {
			t.Errorf("%s printed help that never mentions %q", tc.flag, tc.want)
		}
	}
}

// The -H notice is the runtime-conditional capability, observed where the user
// would see it.
func TestHostForwardNoticeIsRuntimeConditional(t *testing.T) {
	const first = "Warning: Apple Containers cannot reach host services bound only to 127.0.0.1."

	for _, tc := range []struct {
		name       string
		args       []string
		wantNotice bool
	}{
		{"apple warns", nil, true},
		{"docker stays quiet", []string{"--container-runtime=docker"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withStubbedHostAndPath(t)
			project := selectionProject(t)

			argv := append([]string{"claude-contained", "-s", "-N", "-C", project, "-H", "3845"}, tc.args...)
			var got []string
			var stdout, stderr bytes.Buffer
			if code := runWith(recordArgv(&got), runtime.Darwin, argv, strings.NewReader(""), &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d, stderr: %s", code, stderr.String())
			}

			if strings.Contains(stderr.String(), first) != tc.wantNotice {
				t.Errorf("notice present = %v, want %v; stderr: %q",
					!tc.wantNotice, tc.wantNotice, stderr.String())
			}
			// Either way the forward itself is still requested.
			if !strings.Contains(strings.Join(got, " "), "HOST_FORWARD_PORTS=3845") {
				t.Errorf("HOST_FORWARD_PORTS missing from argv: %v", got)
			}
		})
	}
}
