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

func installRuntimeMarkers(t *testing.T, stubDir string) []string {
	t.Helper()
	markers := []string{
		filepath.Join(stubDir, "container.called"),
		filepath.Join(stubDir, "docker.called"),
	}
	for i, bin := range []string{"container", "docker"} {
		script := "#!/bin/sh\ntouch " + markers[i] + "\nexit 0\n"
		if err := os.WriteFile(filepath.Join(stubDir, bin), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return markers
}

func assertNoRuntimeMarkers(t *testing.T, markers []string) {
	t.Helper()
	for _, marker := range markers {
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("runtime operation created marker %s", marker)
		} else if !os.IsNotExist(err) {
			t.Errorf("stat runtime marker %s: %v", marker, err)
		}
	}
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
	markers := installRuntimeMarkers(t, stubDir)

	var stdout, stderr bytes.Buffer
	argv := []string{"claude-contained", "--container-runtime=apple", "-s", "-N", "-C", project}
	code := runWith(failRunner(t), runtime.Linux, argv, strings.NewReader(""), &stdout, &stderr)

	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	want := "error: the apple container runtime is available only on macOS\n" +
		"       use --container-runtime=docker or CLAUDE_CONTAINED_RUNTIME=docker\n"
	if got := stderr.String(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	assertNoRuntimeMarkers(t, markers)
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
			stubDir := withStubbedHostAndPath(t)
			t.Setenv("CLAUDE_CONTAINED_RUNTIME", tc.env)
			project := selectionProject(t)

			argv := append([]string{"claude-contained", "-s", "-N", "-C", project}, tc.args...)
			var got []string
			var stdout, stderr bytes.Buffer
			code := runWith(recordArgv(&got), runtime.Darwin, argv, strings.NewReader(""), &stdout, &stderr)

			if tc.want == "" {
				if code != 0 {
					t.Fatalf("exit %d, stderr: %s", code, stderr.String())
				}
				return
			}
			markers := installRuntimeMarkers(t, stubDir)
			got = nil
			stdout.Reset()
			stderr.Reset()
			code = runWith(failRunner(t), runtime.Darwin, argv, strings.NewReader(""), &stdout, &stderr)
			if code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if stderr.String() != tc.want {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.want)
			}
			assertNoRuntimeMarkers(t, markers)
		})
	}
}

// -h wins wherever it appears, exactly as in bash, so validation runs after it.
func TestHelpWinsOverInvalidRuntime(t *testing.T) {
	stubDir := withStubbedHostAndPath(t)
	markers := installRuntimeMarkers(t, stubDir)

	var stdout, stderr bytes.Buffer
	argv := []string{"claude-contained", "--container-runtime=bogus", "--help"}
	if code := runWith(failRunner(t), runtime.Darwin, argv, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, want 0; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "--container-runtime") {
		t.Error("help text was not printed")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
	assertNoRuntimeMarkers(t, markers)
}

// The help text belongs to the *selected* runtime, so an apple selection off
// macOS still describes Apple Containers before being refused.
//
// The discriminator is each runtime's description line, not the program name:
// ticket 11 gave both runtimes the same name (internal/runtime.ProgName), so
// the name is no longer a valid way to tell which runtime's help was printed.
func TestHelpDescribesTheSelectedRuntime(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"docker before help", []string{"--container-runtime=docker", "--help"}, "Docker container"},
		{"docker after help", []string{"--help", "--container-runtime=docker"}, "Docker container"},
		{"apple before help", []string{"--container-runtime=apple", "--help"}, "Apple Containers sandbox"},
		{"apple after help", []string{"--help", "--container-runtime=apple"}, "Apple Containers sandbox"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubDir := withStubbedHostAndPath(t)
			t.Setenv("CLAUDE_CONTAINED_RUNTIME", "")
			markers := installRuntimeMarkers(t, stubDir)
			var stdout, stderr bytes.Buffer
			argv := append([]string{"claude-contained"}, tc.args...)
			if code := runWith(failRunner(t), runtime.Darwin, argv, strings.NewReader(""), &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d; stderr: %s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Errorf("args %q printed help that never mentions %q", tc.args, tc.want)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
			assertNoRuntimeMarkers(t, markers)
		})
	}
}

func TestHelpUsesMergedRuntimeSelectionGrammar(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"required value masks runtime", []string{"--help", "-e", "--container-runtime=docker"}, "Apple Containers sandbox"},
		{"malformed runtime leaves following runtime flag", []string{"--help", "--container-runtime", "--container-runtime=docker"}, "Docker container"},
		{"malformed runtime leaves tool boundary", []string{"--help", "--container-runtime", "--", "--container-runtime=docker"}, "Apple Containers sandbox"},
		{"consumed boundary does not stop parsing", []string{"--help", "-e", "--", "--container-runtime=docker"}, "Docker container"},
		{"empty inline runtime overwrites docker", []string{"--help", "--container-runtime=docker", "--container-runtime="}, "Apple Containers sandbox"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubDir := withStubbedHostAndPath(t)
			t.Setenv("CLAUDE_CONTAINED_RUNTIME", "")
			markers := installRuntimeMarkers(t, stubDir)
			var stdout, stderr bytes.Buffer
			argv := append([]string{"claude-contained"}, tc.args...)
			code := runWith(failRunner(t), runtime.Darwin, argv, strings.NewReader(""), &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit %d, want 0; stderr: %s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Errorf("stdout does not contain %q", tc.want)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
			assertNoRuntimeMarkers(t, markers)
		})
	}
}

func TestFrontEndFailureAndHelpPrecedence(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		args     []string
		platform runtime.Platform
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{
			"syntax failure before help",
			"", []string{"--container-runtime", "--help"}, runtime.Darwin, 2, "",
			"error: --container-runtime requires apple or docker\n",
		},
		{
			"help wins over invalid runtime flag",
			"", []string{"--help", "--container-runtime=bogus"}, runtime.Darwin, 0, "Apple Containers sandbox", "",
		},
		{
			"help wins over invalid runtime environment",
			"bogus", []string{"--help"}, runtime.Darwin, 0, "Apple Containers sandbox", "",
		},
		{
			"apple help wins over non macOS refusal",
			"", []string{"--container-runtime=apple", "--help"}, runtime.Linux, 0, "Apple Containers sandbox", "",
		},
		{
			"semantic failure outranks invalid runtime",
			"", []string{"--container-runtime=bogus", "--new-session"}, runtime.Darwin, 2, "",
			"error: --new-session is valid only with --zellij\n",
		},
		{
			"syntax failure outranks invalid runtime",
			"", []string{"--wat", "--container-runtime=bogus"}, runtime.Darwin, 2, "",
			"error: unknown flag: --wat\n       run 'claude-contained --help' for the supported flags\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubDir := withStubbedHostAndPath(t)
			t.Setenv("CLAUDE_CONTAINED_RUNTIME", tc.env)
			markers := installRuntimeMarkers(t, stubDir)
			var stdout, stderr bytes.Buffer
			argv := append([]string{"claude-contained"}, tc.args...)
			code := runWith(failRunner(t), tc.platform, argv, strings.NewReader(""), &stdout, &stderr)
			if code != tc.wantCode {
				t.Errorf("exit %d, want %d", code, tc.wantCode)
			}
			if tc.wantOut == "" {
				if stdout.Len() != 0 {
					t.Errorf("stdout = %q, want empty", stdout.String())
				}
			} else if !strings.Contains(stdout.String(), tc.wantOut) {
				t.Errorf("stdout does not contain %q", tc.wantOut)
			}
			if stderr.String() != tc.wantErr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantErr)
			}
			assertNoRuntimeMarkers(t, markers)
		})
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
