package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-contained/internal/diagnostic"
	"claude-contained/internal/runtime"
)

type failOnceWriter struct {
	bytes.Buffer
	failed bool
}

func (w *failOnceWriter) Write(p []byte) (int, error) {
	if !w.failed {
		w.failed = true
		return 0, errors.New("injected diagnostic write failure")
	}
	return w.Buffer.Write(p)
}

func diagnosticProject(t *testing.T) string {
	t.Helper()
	withStubbedHostAndPath(t)
	t.Setenv("CLAUDE_CONTAINED_LOG_LEVEL", "")
	t.Setenv("CLAUDE_CONTAINED_RUNTIME", "")
	return t.TempDir()
}

func TestDiagnosticRunWithEmitsAnchorsWithoutEnvironmentValues(t *testing.T) {
	const sentinel = "DIAGNOSTIC-LEAK-SENTINEL"
	project := diagnosticProject(t)
	t.Setenv("AI_GH_TOKEN", sentinel)
	if err := os.MkdirAll(filepath.Join(project, ".claude-contained"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".claude-contained", "env"), []byte("FILE_SECRET="+sentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runnerSawSecrets := false
	fake := func(_ context.Context, argv []string, _ io.Reader, _, _ io.Writer) int {
		rendered := strings.Join(argv, "\n")
		runnerSawSecrets = strings.Contains(rendered, "GH_TOKEN="+sentinel) &&
			strings.Contains(rendered, "FLAG_SECRET="+sentinel) &&
			strings.Contains(rendered, "FILE_SECRET="+sentinel)
		return 0
	}
	var stdout, stderr bytes.Buffer
	code := runWith(fake, runtime.Darwin, []string{
		"claude-contained", "--log-level=debug", "-e", "FLAG_SECRET=" + sentinel,
		"-s", "-N", "-C", project,
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
	if !runnerSawSecrets {
		t.Fatal("runtime did not receive the real environment values")
	}
	got := stderr.String()
	if strings.Contains(got, sentinel) {
		t.Fatalf("diagnostic stream leaked sentinel: %q", got)
	}
	for _, anchor := range []string{
		"msg=\"launcher configuration parsed\" kind=diagnostic component=cli",
		"msg=\"host state probed\" kind=diagnostic component=host",
		"msg=\"container runtime selected\" kind=diagnostic component=runtime",
		"msg=\"environment assignment resolved\" kind=diagnostic component=env",
		"FLAG_SECRET", "FILE_SECRET", "GH_TOKEN", "=<redacted>",
	} {
		if !strings.Contains(got, anchor) {
			t.Errorf("diagnostic stream missing %q: %q", anchor, got)
		}
	}
}

func TestLogOnlyErrorLevelKeepsRelocatedWarningAndFiltersDiagnostic(t *testing.T) {
	diagnosticProject(t)
	var stdout, stderr bytes.Buffer
	code := runWith(failRunner(t), runtime.Darwin,
		[]string{"claude-contained", "--log-only", "--log-level=error", "--wat"},
		strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	got := stderr.String()
	if !strings.Contains(got, "kind=output stream=stderr") || !strings.Contains(got, "error: unknown flag: --wat") {
		t.Errorf("relocated validation output missing: %q", got)
	}
	if strings.Contains(got, "command line validation failed") {
		t.Errorf("warn diagnostic survived error threshold: %q", got)
	}
}

// Attach normally replaces the launcher process. Under --log-only it must
// instead proxy the child so Go's line-aware writers remain in the data path.
func TestLogOnlyProxiesAttachAndZellijChildOutput(t *testing.T) {
	diagnosticProject(t)
	original := replaceProcess
	replaceProcess = func(argv []string) error {
		t.Fatalf("log-only attach must proxy instead of replacing, argv=%v", argv)
		return nil
	}
	t.Cleanup(func() { replaceProcess = original })

	tests := []struct {
		name string
		args []string
		prep func(*testing.T)
	}{
		{
			name: "plain attach",
			args: []string{"claude-contained", "--log-only", "--log-level=error", "--attach", "live"},
			prep: func(t *testing.T) { t.Setenv("STUB_LIST", "aic-live") },
		},
		{
			name: "zellij attach",
			args: []string{"claude-contained", "--log-only", "--log-level=error", "--zellij", "--attach", "--session", "alpha"},
			prep: func(t *testing.T) {
				t.Setenv("STUB_LIST", "aic-z1")
				t.Setenv("STUB_INSPECT", markedInspectJSON("alpha"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.prep(t)
			var stdout, stderr bytes.Buffer
			fake := func(_ context.Context, _ []string, _ io.Reader, childStdout, childStderr io.Writer) int {
				_, _ = io.WriteString(childStdout, "attached stdout\n")
				_, _ = io.WriteString(childStderr, "attached stderr\n")
				return 0
			}
			code := runWith(fake, runtime.Darwin, tt.args, strings.NewReader(""), &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("raw stdout escaped relocation: %q", stdout.String())
			}
			for _, anchor := range []string{
				"msg=\"attached stdout\" kind=output stream=stdout",
				"msg=\"attached stderr\" kind=output stream=stderr",
			} {
				if !strings.Contains(stderr.String(), anchor) {
					t.Errorf("stream missing %q: %q", anchor, stderr.String())
				}
			}
		})
	}
}

func TestStreamFailureStatusPrecedenceAndSingleReport(t *testing.T) {
	project := diagnosticProject(t)
	tests := []struct {
		name       string
		args       []string
		wantStatus int
	}{
		{
			name:       "successful primary result becomes failure",
			args:       []string{"claude-contained", "--log-level=debug", "-s", "-N", "-C", project},
			wantStatus: 1,
		},
		{
			name:       "usage result remains primary",
			args:       []string{"claude-contained", "--log-level=debug", "--wat"},
			wantStatus: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			stderr := &failOnceWriter{}
			code := runWith(func(context.Context, []string, io.Reader, io.Writer, io.Writer) int { return 0 },
				runtime.Darwin, tt.args, strings.NewReader(""), &stdout, stderr)
			if code != tt.wantStatus {
				t.Errorf("exit = %d, want %d", code, tt.wantStatus)
			}
			if got := strings.Count(stderr.String(), "error: diagnostic stream failed:"); got != 1 {
				t.Errorf("stream failure reports = %d, want 1: %q", got, stderr.String())
			}
		})
	}
}

func TestPreExecFlushFailureBlocksReplacement(t *testing.T) {
	diagnosticProject(t)
	t.Setenv("STUB_LIST", "aic-live")
	original := replaceProcess
	replaced := false
	replaceProcess = func([]string) error {
		replaced = true
		return nil
	}
	t.Cleanup(func() { replaceProcess = original })

	var stdout bytes.Buffer
	stderr := &failOnceWriter{}
	code := runWith(failRunner(t), runtime.Darwin,
		[]string{"claude-contained", "--log-level=debug", "--attach", "live"},
		strings.NewReader(""), &stdout, stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if replaced {
		t.Error("replaceProcess ran after a failed diagnostic flush")
	}
	if got := strings.Count(stderr.String(), "error: diagnostic stream failed:"); got != 1 {
		t.Errorf("stream failure reports = %d, want 1: %q", got, stderr.String())
	}
}

func TestHelpNeverCreatesOrTruncatesDiagnosticFile(t *testing.T) {
	diagnosticProject(t)
	for _, args := range [][]string{
		{"claude-contained", "--help", "--log-file"},
		{"claude-contained", "--log-file", "--help"},
	} {
		path := filepath.Join(t.TempDir(), "help.log")
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, path)
		// Put PATH in the right place for the second form.
		if args[1] == "--log-file" {
			args = []string{"claude-contained", "--log-file", path, "--help"}
		}
		var stdout, stderr bytes.Buffer
		if code := runWith(failRunner(t), runtime.Darwin, args, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("args %q exit = %d, stderr: %s", args, code, stderr.String())
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "keep" {
			t.Errorf("help touched log file: %q", content)
		}
	}
}

func TestInvalidLevelOutranksSemanticFailureWithoutOpeningFile(t *testing.T) {
	diagnosticProject(t)
	for _, args := range [][]string{
		{"claude-contained", "--new-session", "--log-level=LOUD"},
		{"claude-contained", "--log-level=LOUD", "--new-session"},
	} {
		path := filepath.Join(t.TempDir(), "must-not-exist.log")
		args = append(args, "--log-file", path)
		var stdout, stderr bytes.Buffer
		code := runWith(failRunner(t), runtime.Darwin, args, strings.NewReader(""), &stdout, &stderr)
		if code != 2 || !strings.Contains(stderr.String(), "log level must be debug, info, warn, error, or off") {
			t.Fatalf("args %q exit/stderr = %d/%q", args, code, stderr.String())
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("invalid level opened file: %v", err)
		}
	}
}

func TestLogFileSetupPrecedesDeferredSyntaxInBothOrders(t *testing.T) {
	diagnosticProject(t)
	bad := filepath.Join(t.TempDir(), "missing", "diagnostic.log")
	for _, args := range [][]string{
		{"claude-contained", "--wat", "--log-file", bad},
		{"claude-contained", "--log-file", bad, "--wat"},
	} {
		var stdout, stderr bytes.Buffer
		code := runWith(failRunner(t), runtime.Darwin, args, strings.NewReader(""), &stdout, &stderr)
		if code != 2 || !strings.Contains(stderr.String(), "cannot open diagnostic file") ||
			strings.Contains(stderr.String(), "unknown flag") {
			t.Errorf("args %q exit/stderr = %d/%q", args, code, stderr.String())
		}
	}
}

func TestValidLogFileIsSecuredBeforeDeferredSyntaxInBothOrders(t *testing.T) {
	diagnosticProject(t)
	for _, fileFirst := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "syntax.log")
		if err := os.WriteFile(path, []byte("old"), 0o666); err != nil {
			t.Fatal(err)
		}
		args := []string{"claude-contained", "--wat", "--log-file", path}
		if fileFirst {
			args = []string{"claude-contained", "--log-file", path, "--wat"}
		}
		var stdout, stderr bytes.Buffer
		code := runWith(failRunner(t), runtime.Darwin, args, strings.NewReader(""), &stdout, &stderr)
		if code != 2 || !strings.Contains(stderr.String(), "unknown flag: --wat") {
			t.Errorf("args %q exit/stderr = %d/%q", args, code, stderr.String())
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 || len(content) != 0 {
			t.Errorf("mode/content = %o/%q, want 600/empty", info.Mode().Perm(), content)
		}
	}
}

func TestDiagnosticSetupAndDeferredValidationPrecedence(t *testing.T) {
	diagnosticProject(t)
	for _, args := range [][]string{
		{"claude-contained", "--wat", "--log-file", "BAD"},
		{"claude-contained", "--log-file", "BAD", "--wat"},
	} {
		bad := filepath.Join(t.TempDir(), "missing", "diagnostic.log")
		for i := range args {
			if args[i] == "BAD" {
				args[i] = bad
			}
		}
		var stdout, stderr bytes.Buffer
		code := runWith(failRunner(t), runtime.Darwin, args, strings.NewReader(""), &stdout, &stderr)
		if code != 2 || !strings.Contains(stderr.String(), "cannot open diagnostic file") || strings.Contains(stderr.String(), "unknown flag") {
			t.Errorf("args %q exit/stderr = %d/%q", args, code, stderr.String())
		}
	}

	path := filepath.Join(t.TempDir(), "syntax.log")
	if err := os.WriteFile(path, []byte("old"), 0o666); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runWith(failRunner(t), runtime.Darwin,
		[]string{"claude-contained", "--wat", "--log-file", path},
		strings.NewReader(""), &stdout, &stderr)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if code != 2 || !strings.Contains(stderr.String(), "unknown flag") || len(content) != 0 {
		t.Errorf("valid-file syntax failure exit/stderr/file = %d/%q/%q", code, stderr.String(), content)
	}
}

func TestDiagnosticFileIsSecuredAndOpenFailureNeverFallsBack(t *testing.T) {
	const staleContent = "STALE-DIAGNOSTIC-CONTENTS"
	project := diagnosticProject(t)
	path := filepath.Join(t.TempDir(), "diagnostic.log")
	if err := os.WriteFile(path, []byte(staleContent), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runWith(func(context.Context, []string, io.Reader, io.Writer, io.Writer) int { return 0 }, runtime.Darwin,
		[]string{"claude-contained", "--log-level=info", "--log-file", path, "-s", "-N", "-C", project},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "container runtime selected") || strings.Contains(string(content), staleContent) {
		t.Errorf("file was not truncated/used: %q", content)
	}
	if stderr.Len() != 0 {
		t.Errorf("file diagnostics also reached stderr: %q", stderr.String())
	}

	bad := filepath.Join(t.TempDir(), "missing", "diagnostic.log")
	stdout.Reset()
	stderr.Reset()
	code = runWith(failRunner(t), runtime.Darwin,
		[]string{"claude-contained", "--log-level=debug", "--log-file", bad},
		strings.NewReader(""), &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "cannot open diagnostic file") || strings.Contains(stderr.String(), "kind=") {
		t.Errorf("open failure exit/stderr = %d/%q", code, stderr.String())
	}
}

func TestLogFileAloneCreatesAnEmptySecuredFile(t *testing.T) {
	project := diagnosticProject(t)
	path := filepath.Join(t.TempDir(), "off.log")
	if err := os.WriteFile(path, []byte("old"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runWith(func(context.Context, []string, io.Reader, io.Writer, io.Writer) int { return 0 }, runtime.Darwin,
		[]string{"claude-contained", "--log-file", path, "-s", "-N", "-C", project},
		strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || len(content) != 0 {
		t.Errorf("mode/content = %o/%q, want 600/empty", info.Mode().Perm(), content)
	}
}

func TestPromptTextBypassesRelocatedWriters(t *testing.T) {
	var destination, terminal bytes.Buffer
	stream, err := diagnostic.Open(diagnostic.Options{
		Resolution: diagnostic.Resolution{Level: diagnostic.LevelInfo},
		LogOnly:    true,
	}, &destination)
	if err != nil {
		t.Fatal(err)
	}
	_, relocatedStderr := stream.Writers(io.Discard, io.Discard)
	p := newPrompter(strings.NewReader("y\n"), &terminal, true)
	answer, ok := p.ask("Continue? [Y/n] ", true, nil)
	if !ok || !answer {
		t.Fatalf("prompt answer = %v/%v", answer, ok)
	}
	if _, err := io.WriteString(relocatedStderr, "ordinary warning\n"); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if terminal.String() != "Continue? [Y/n] " {
		t.Errorf("terminal = %q", terminal.String())
	}
	if strings.Contains(destination.String(), "Continue?") || !strings.Contains(destination.String(), "ordinary warning") {
		t.Errorf("prompt/output routing = %q", destination.String())
	}
}
