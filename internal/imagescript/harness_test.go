package imagescript

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-contained/internal/blackbox"
)

// TestMain routes a re-exec of this test binary under a stubbed command name
// (socat, script, zellij, id) into the blackbox stub, and otherwise runs the
// tests. Stub mode is entered only when BLACKBOX_STUB_SPEC is set in the
// inherited environment, which happens only in a script subprocess, never in
// the go-test parent.
func TestMain(m *testing.M) {
	if blackbox.RunStubIfInvoked() {
		return // unreachable: the stub exits the process.
	}
	os.Exit(m.Run())
}

// requireBash resolves bash via PATH (not /bin/bash: macOS ships bash 3.2 there,
// while the image scripts and the container use bash 5) and fails clearly when
// it is absent -- a contributor prerequisite, never a skip.
func requireBash(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is a required test prerequisite and was not found on PATH (see CONTRIBUTING.md: Development Setup): %v", err)
	}
	return path
}

// requireJQ fails clearly when jq is absent. srt-settings.sh generates its
// policy with jq, so a missing jq is a contributor-environment error, not a
// reason to skip the security-critical coverage.
func requireJQ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatalf("jq is a required test prerequisite and was not found on PATH (see CONTRIBUTING.md: Development Setup): %v", err)
	}
}

// requireSh resolves a POSIX sh, used by the tests that source mavenrc the way a
// direct-image Maven run would.
func requireSh(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("sh is a required test prerequisite and was not found on PATH: %v", err)
	}
	return path
}

// repoFile returns the absolute path of a repository file (relative to the
// module root), for reading shipped scripts and assets.
func repoFile(t *testing.T, rel ...string) string {
	t.Helper()
	return filepath.Join(append([]string{blackbox.ModuleRoot(t)}, rel...)...)
}

// scriptPath returns the absolute path of a shipped image script.
func scriptPath(t *testing.T, name string) string {
	t.Helper()
	return repoFile(t, "image", name)
}

// scriptOpts configures one image-script run. The environment is built from
// scratch -- only PATH (with the stub directory prepended when Stubs is set),
// HOME, the stub spec, and Env reach the script -- so a variable in the
// developer's shell can never leak into a test (srt-settings.sh, for one, reads
// SSH_AUTH_SOCK).
type scriptOpts struct {
	Script string
	Args   []string
	Env    []string // extra KEY=VALUE entries
	Path   string   // base PATH; defaults to the parent PATH (so bash/jq/env resolve)
	Home   string
	Stubs  *blackbox.Stubs
	Stdin  string
}

type scriptResult struct {
	Stdout string
	Stderr string
	Code   int
}

// runScript runs `bash <script> args...` under a 30s hang guard and returns its
// observable result.
func runScript(t *testing.T, opts scriptOpts) scriptResult {
	t.Helper()
	bash := requireBash(t)

	base := opts.Path
	if base == "" {
		base = os.Getenv("PATH")
	}
	if opts.Stubs != nil {
		base = opts.Stubs.Dir + string(os.PathListSeparator) + base
	}
	env := []string{"PATH=" + base}
	if opts.Home != "" {
		env = append(env, "HOME="+opts.Home)
	}
	if opts.Stubs != nil {
		env = append(env, opts.Stubs.LauncherEnv(t))
	}
	env = append(env, opts.Env...)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bash, append([]string{opts.Script}, opts.Args...)...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(opts.Stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("%s did not exit within the hang guard\nstdout:\n%s\nstderr:\n%s",
			filepath.Base(opts.Script), stdout.String(), stderr.String())
	}
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	} else if err != nil {
		t.Fatalf("starting %s: %v", filepath.Base(opts.Script), err)
	}
	return scriptResult{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
}

// parseEnvOutput turns `KEY=VALUE` lines (as printed by env) into a map,
// splitting on the first '='.
func parseEnvOutput(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		if i := strings.IndexByte(line, '='); i >= 0 {
			m[line[:i]] = line[i+1:]
		}
	}
	return m
}
