package attach

import (
	"reflect"
	"strings"
	"testing"

	"claude-contained/internal/env"
	"claude-contained/internal/runtime"
)

// neverPrompt fails the test if Prompt is invoked at all -- used by cases that
// must never reach the picker.
func neverPrompt(t *testing.T) func(string) (string, bool) {
	t.Helper()
	return func(prompt string) (string, bool) {
		t.Fatalf("Prompt should not have been called (got %q)", prompt)
		return "", false
	}
}

func baseRequest(t *testing.T) Request {
	var stdout, stderr strings.Builder
	return Request{
		Tool:   "claude",
		Home:   "/h",
		Stdout: &stdout,
		Stderr: &stderr,
		Prompt: neverPrompt(t),
	}
}

// --- checklist box 1: attaching by name reconnects and runs the tool -------

func TestResolveByNameBuildsExec(t *testing.T) {
	var stdout, stderr strings.Builder
	req := baseRequest(t)
	req.Stdout, req.Stderr = &stdout, &stderr
	req.Name = "myproject"
	req.Running = []string{"aic-myproject"}

	dec := Resolve(req)

	if dec.Spec == nil {
		t.Fatal("Spec is nil, want a built exec spec")
	}
	if dec.Spec.Container != "aic-myproject" {
		t.Errorf("Container = %q, want aic-myproject", dec.Spec.Container)
	}
	if dec.Spec.User != "dev" {
		t.Errorf("User = %q, want dev", dec.Spec.User)
	}
	if !dec.Spec.TTY {
		t.Error("TTY = false, want true")
	}
	want := []string{"/usr/local/bin/tool-env", "/usr/local/bin/srt-run", "/opt/claude/claude"}
	if !reflect.DeepEqual(dec.Spec.Command, want) {
		t.Errorf("Command = %#v, want %#v", dec.Spec.Command, want)
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// --- checklist box 2: name miss exits non-zero, creates nothing ------------

func TestResolveByNameMissRefuses(t *testing.T) {
	cases := []struct {
		name    string
		running []string
	}{
		{"no matching container", []string{"aic-other"}},
		{"no containers at all", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			req := baseRequest(t)
			req.Stdout, req.Stderr = &stdout, &stderr
			req.Name = "gamma"
			req.Running = tc.running

			dec := Resolve(req)

			if dec.Spec != nil {
				t.Fatalf("Spec = %#v, want nil", dec.Spec)
			}
			if dec.Code != 1 {
				t.Errorf("Code = %d, want 1", dec.Code)
			}
			wantErr := "error: no running container named aic-gamma\n" +
				"       use --name gamma to create a new one\n"
			if stderr.String() != wantErr {
				t.Errorf("stderr = %q, want %q", stderr.String(), wantErr)
			}
			if stdout.String() != "" {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

// --- checklist box 3: prefix added when omitted, stripped for display ------

func TestNormalizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"foo", "aic-foo"},
		{"aic-foo", "aic-foo"},
		{"aic", "aic-aic"},
		{"aic-", "aic-"},
	}
	for _, tc := range cases {
		if got := normalizeName(tc.in); got != tc.want {
			t.Errorf("normalizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPickerStripsPrefixForDisplay(t *testing.T) {
	var stdout, stderr strings.Builder
	req := baseRequest(t)
	req.Stdout, req.Stderr = &stdout, &stderr
	req.Running = []string{"aic-alpha", "aic-beta"}
	req.Prompt = func(string) (string, bool) { return "q", true }

	Resolve(req)

	want := "Running containers:\n  1) alpha\n  2) beta\n\n"
	if stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

// --- checklist box 4: picker lists, prompts, quits, rejects invalid --------

func TestPickerSelects(t *testing.T) {
	var promptText string
	req := baseRequest(t)
	req.Running = []string{"aic-alpha", "aic-beta"}
	req.Prompt = func(p string) (string, bool) {
		promptText = p
		return "2\n", true
	}

	dec := Resolve(req)

	if dec.Spec == nil || dec.Spec.Container != "aic-beta" {
		t.Fatalf("Spec = %#v, want container aic-beta", dec.Spec)
	}
	wantPrompt := "Select container (1-2, q to quit): "
	if promptText != wantPrompt {
		t.Errorf("prompt = %q, want %q", promptText, wantPrompt)
	}
}

func TestPickerQuit(t *testing.T) {
	cases := []string{"", "q", "Q", "  q  \n"}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			var stdout, stderr strings.Builder
			req := baseRequest(t)
			req.Stdout, req.Stderr = &stdout, &stderr
			req.Running = []string{"aic-alpha", "aic-beta"}
			req.Prompt = func(string) (string, bool) { return line, true }

			dec := Resolve(req)

			if dec.Spec != nil {
				t.Fatalf("Spec = %#v, want nil", dec.Spec)
			}
			if dec.Code != 0 {
				t.Errorf("Code = %d, want 0", dec.Code)
			}
			if stderr.String() != "" {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestPickerInvalidSelection(t *testing.T) {
	cases := []string{"0", "3", "x", "2x", "-1", "1 2", "2\r"}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			var stdout, stderr strings.Builder
			req := baseRequest(t)
			req.Stdout, req.Stderr = &stdout, &stderr
			req.Running = []string{"aic-alpha", "aic-beta"}
			req.Prompt = func(string) (string, bool) { return line, true }

			dec := Resolve(req)

			if dec.Spec != nil {
				t.Fatalf("Spec = %#v, want nil", dec.Spec)
			}
			if dec.Code != 1 {
				t.Errorf("Code = %d, want 1", dec.Code)
			}
			if stderr.String() != "Invalid selection\n" {
				t.Errorf("stderr = %q, want %q", stderr.String(), "Invalid selection\n")
			}
		})
	}
}

func TestPickerEOF(t *testing.T) {
	var stdout, stderr strings.Builder
	req := baseRequest(t)
	req.Stdout, req.Stderr = &stdout, &stderr
	req.Running = []string{"aic-alpha"}
	req.Prompt = func(string) (string, bool) { return "", false }

	dec := Resolve(req)

	if dec.Spec != nil {
		t.Fatalf("Spec = %#v, want nil", dec.Spec)
	}
	if dec.Code != 1 {
		t.Errorf("Code = %d, want 1", dec.Code)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// --- checklist box 5: no containers running reports and exits cleanly ------

func TestPickerNoContainers(t *testing.T) {
	cases := []struct {
		name    string
		running []string
	}{
		{"nothing running", nil},
		{"only foreign containers", []string{"not-ours"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			req := baseRequest(t)
			req.Stdout, req.Stderr = &stdout, &stderr
			req.Running = tc.running
			// neverPrompt (via baseRequest) fails the test if Prompt is called.

			dec := Resolve(req)

			if dec.Spec != nil {
				t.Fatalf("Spec = %#v, want nil", dec.Spec)
			}
			if dec.Code != 0 {
				t.Errorf("Code = %d, want 0", dec.Code)
			}
			if stdout.String() != "No running aic containers\n" {
				t.Errorf("stdout = %q, want %q", stdout.String(), "No running aic containers\n")
			}
		})
	}
}

// A nil Prompt (a zero-value Request reaching the picker) must behave like an
// immediate EOF rather than panicking.
func TestPickerNilPrompt(t *testing.T) {
	var stdout, stderr strings.Builder
	req := baseRequest(t)
	req.Stdout, req.Stderr = &stdout, &stderr
	req.Running = []string{"aic-alpha"}
	req.Prompt = nil

	dec := Resolve(req)

	if dec.Spec != nil {
		t.Fatalf("Spec = %#v, want nil", dec.Spec)
	}
	if dec.Code != 1 {
		t.Errorf("Code = %d, want 1", dec.Code)
	}
}

// --- checklist box 6: the debug shell flag attaches a shell -----------------

func TestCommandDebugShell(t *testing.T) {
	var stderr strings.Builder
	req := baseRequest(t)
	req.Stderr = &stderr
	req.Shell = true
	req.Tool = "codex"
	req.Yolo = true

	got := Command(req)
	want := []string{"/usr/local/bin/tool-env", "/usr/local/bin/srt-run", "/usr/local/bin/shell-run"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Command = %#v, want %#v", got, want)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty (shell ignores tool/yolo)", stderr.String())
	}
}

// --- checklist box 7: sandbox wrapper prefix, dropped by --no-sandbox ------

func TestCommandSandboxWrapper(t *testing.T) {
	cases := []struct {
		name       string
		shell      bool
		srtDisable bool
		want       []string
	}{
		{"tool, sandboxed", false, false, []string{"/usr/local/bin/tool-env", "/usr/local/bin/srt-run", "/opt/claude/claude"}},
		{"tool, no-sandbox", false, true, []string{"/usr/local/bin/tool-env", "/opt/claude/claude"}},
		{"shell, sandboxed", true, false, []string{"/usr/local/bin/tool-env", "/usr/local/bin/srt-run", "/usr/local/bin/shell-run"}},
		{"shell, no-sandbox", true, true, []string{"/usr/local/bin/tool-env", "/usr/local/bin/shell-run"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest(t)
			req.Shell = tc.shell
			req.SrtDisable = tc.srtDisable

			got := Command(req)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Command = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// --- checklist box 8: yolo lands after the tool, behind the wrapper --------

func TestCommandYoloPlacement(t *testing.T) {
	cases := []struct {
		tool       string
		wantCmd    []string
		wantStderr string
	}{
		{"claude", []string{"/usr/local/bin/tool-env", "/usr/local/bin/srt-run", "/opt/claude/claude", "--dangerously-skip-permissions"}, ""},
		{"codex", []string{"/usr/local/bin/tool-env", "/usr/local/bin/srt-run", "codex", "--yolo"}, ""},
		{"copilot", []string{"/usr/local/bin/tool-env", "/usr/local/bin/srt-run", "copilot", "--yolo"}, ""},
		{"gemini", []string{"/usr/local/bin/tool-env", "/usr/local/bin/srt-run", "gemini", "--yolo"}, ""},
		{"vibe", []string{"/usr/local/bin/tool-env", "/usr/local/bin/srt-run", "vibe"}, "Warning: vibe does not support yolo mode\n"},
		{"bogus", []string{"/usr/local/bin/tool-env", "/usr/local/bin/srt-run", "bogus"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			var stderr strings.Builder
			req := baseRequest(t)
			req.Stderr = &stderr
			req.Tool = tc.tool
			req.Yolo = true

			got := Command(req)
			if !reflect.DeepEqual(got, tc.wantCmd) {
				t.Errorf("Command = %#v, want %#v", got, tc.wantCmd)
			}
			if stderr.String() != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

// --- argv env ordering -------------------------------------------------

func TestExecEnvOrder(t *testing.T) {
	req := baseRequest(t)
	req.Name = "live"
	req.Running = []string{"aic-live"}
	req.Env = []env.Pair{{Key: "FOO", Value: "bar"}, {Key: "BAZ", Value: "qux"}}

	dec := Resolve(req)
	if dec.Spec == nil {
		t.Fatal("Spec is nil")
	}

	want := []runtime.EnvArg{
		{Key: "HOME", Value: "/h"},
		{Key: "FOO", Value: "bar"},
		{Key: "BAZ", Value: "qux"},
		{Key: env.ExplicitKeysMarker, Value: "FOO,BAZ"},
	}
	if !reflect.DeepEqual(dec.Spec.Env, want) {
		t.Errorf("Env = %#v, want %#v", dec.Spec.Env, want)
	}
}
