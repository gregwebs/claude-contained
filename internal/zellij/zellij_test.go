package zellij

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// --- 3.1: session naming ----------------------------------------------------

// TestSessionNameGoldenTable pins SessionName against values produced by
// running default_zellij_session_name in bash (plan §3.1), not derived by
// reading it.
func TestSessionNameGoldenTable(t *testing.T) {
	cases := []struct {
		name string
		dir  string
		want string
	}{
		{"baseline", "/Users/me/code/My-App", "cc-my-app-a1e99d08"},
		// Double dash: truncation at 20 leaves the sanitizer's trailing dash,
		// then the "-" joiner adds its own.
		{"double dash from truncation", "/tmp/abcdefghijklmnopqrs-tuv", "cc-abcdefghijklmnopqrs--9251b06a"},
		// The empty-name fallback.
		{"root fallback", "/", "cc-root-8a5edab2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SessionName(tc.dir); got != tc.want {
				t.Errorf("SessionName(%q) = %q, want %q", tc.dir, got, tc.want)
			}
		})
	}
}

// The hash half of a session name is host.PathHash8; its golden value and the
// no-trailing-newline property are pinned by host.TestPathHash8, and the two
// halves together by TestSessionNameGoldenTable above.

// --- 3.3: env-marker discovery ----------------------------------------------

func TestSessionFromEnv(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"marker and session", []string{"CLAUDE_CONTAINED_ZELLIJ=1", "CLAUDE_CONTAINED_ZELLIJ_SESSION=alpha"}, "alpha"},
		{"session only", []string{"CLAUDE_CONTAINED_ZELLIJ_SESSION=alpha"}, ""},
		{"marker only", []string{"CLAUDE_CONTAINED_ZELLIJ=1"}, ""},
		{"marker wrong value", []string{"CLAUDE_CONTAINED_ZELLIJ=0", "CLAUDE_CONTAINED_ZELLIJ_SESSION=alpha"}, ""},
		{"marker not exact 01", []string{"CLAUDE_CONTAINED_ZELLIJ=01", "CLAUDE_CONTAINED_ZELLIJ_SESSION=alpha"}, ""},
		{"marker not exact true", []string{"CLAUDE_CONTAINED_ZELLIJ=true", "CLAUDE_CONTAINED_ZELLIJ_SESSION=alpha"}, ""},
		{"marker trailing space", []string{"CLAUDE_CONTAINED_ZELLIJ=1 ", "CLAUDE_CONTAINED_ZELLIJ_SESSION=alpha"}, ""},
		{"session empty", []string{"CLAUDE_CONTAINED_ZELLIJ=1", "CLAUDE_CONTAINED_ZELLIJ_SESSION="}, ""},
		{"last session wins", []string{
			"CLAUDE_CONTAINED_ZELLIJ=1",
			"CLAUDE_CONTAINED_ZELLIJ_SESSION=first",
			"CLAUDE_CONTAINED_ZELLIJ_SESSION=second",
		}, "second"},
		{"unrelated lines ignored", []string{"FOO=bar", "CLAUDE_CONTAINED_ZELLIJ=1", "CLAUDE_CONTAINED_ZELLIJ_SESSION=alpha", "BAZ=qux"}, "alpha"},
		{"session value containing equals", []string{"CLAUDE_CONTAINED_ZELLIJ=1", "CLAUDE_CONTAINED_ZELLIJ_SESSION=a=b"}, "a=b"},
		{"nothing at all", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SessionFromEnv(tc.lines); got != tc.want {
				t.Errorf("SessionFromEnv(%v) = %q, want %q", tc.lines, got, tc.want)
			}
		})
	}
}

// --- 3.4: the launch gate ----------------------------------------------------

func TestResolveLaunch(t *testing.T) {
	cases := []struct {
		name       string
		session    string
		records    []Record
		newSession bool
		wantCode   int
		wantStderr string
	}{
		{
			name:       "target live, no force: refuse",
			session:    "alpha",
			records:    []Record{{Container: "aic-z1", Session: "alpha"}},
			newSession: false,
			wantCode:   1,
			wantStderr: "Zellij session 'alpha' is already live.\n" +
				"Use --zellij --attach --session alpha, or choose a different session with --session=NAME.\n",
		},
		{
			name:       "target live, force does not override",
			session:    "alpha",
			records:    []Record{{Container: "aic-z1", Session: "alpha"}},
			newSession: true,
			wantCode:   1,
			wantStderr: "Zellij session 'alpha' is already live.\n" +
				"Use --zellij --attach --session alpha, or choose a different session with --session=NAME.\n",
		},
		{
			name:       "target not live, none live, no force: proceed",
			session:    "alpha",
			records:    nil,
			newSession: false,
			wantCode:   0,
			wantStderr: "",
		},
		{
			name:       "target not live, none live, force: proceed",
			session:    "alpha",
			records:    nil,
			newSession: true,
			wantCode:   0,
			wantStderr: "",
		},
		{
			name:       "target not live, others live, no force: refuse",
			session:    "alpha",
			records:    []Record{{Container: "aic-z1", Session: "beta"}, {Container: "aic-z2", Session: "gamma"}},
			newSession: false,
			wantCode:   1,
			wantStderr: "A Zellij-backed container is already running:\n" +
				"  beta\n" +
				"  gamma\n" +
				"Use --zellij --attach [--session NAME] to reconnect, or --zellij --new-session [--session NAME] to start another session.\n",
		},
		{
			name:       "target not live, others live, force: proceed",
			session:    "alpha",
			records:    []Record{{Container: "aic-z1", Session: "beta"}},
			newSession: true,
			wantCode:   0,
			wantStderr: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := ResolveLaunch(tc.session, tc.records, tc.newSession, &stderr)
			if code != tc.wantCode {
				t.Errorf("code = %d, want %d", code, tc.wantCode)
			}
			if stderr.String() != tc.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

// --- 3.5: the attach decision -----------------------------------------------

func TestResolveAttachSingleGoesStraightIn(t *testing.T) {
	var stdout, stderr bytes.Buffer
	req := AttachRequest{
		Session: "",
		Home:    "/h",
		Records: []Record{{Container: "aic-z1", Session: "alpha"}},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Prompt: func(string) (string, bool) {
			t.Fatal("Prompt should not be called when exactly one session is live")
			return "", false
		},
	}
	dec := ResolveAttach(req)
	if dec.Spec == nil {
		t.Fatal("Spec is nil, want an ExecSpec")
	}
	if dec.Spec.Container != "aic-z1" {
		t.Errorf("Container = %q, want %q", dec.Spec.Container, "aic-z1")
	}
	if dec.Spec.User != "dev" {
		t.Errorf("User = %q, want %q", dec.Spec.User, "dev")
	}
	if !dec.Spec.TTY {
		t.Error("TTY = false, want true")
	}
	wantCmd := []string{"srt-run", "/usr/local/bin/zellij-attach", "alpha"}
	if !reflect.DeepEqual(dec.Spec.Command, wantCmd) {
		t.Errorf("Command = %v, want %v", dec.Spec.Command, wantCmd)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestResolveAttachPicker(t *testing.T) {
	records := []Record{
		{Container: "aic-z1", Session: "alpha"},
		{Container: "aic-z2", Session: "beta"},
	}

	t.Run("valid choice execs the chosen record", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		var promptText string
		req := AttachRequest{
			Records: records,
			Stdout:  &stdout,
			Stderr:  &stderr,
			Prompt: func(p string) (string, bool) {
				promptText = p
				return "2\n", true
			},
		}
		dec := ResolveAttach(req)
		if dec.Spec == nil {
			t.Fatal("Spec is nil, want an ExecSpec")
		}
		if dec.Spec.Container != "aic-z2" {
			t.Errorf("Container = %q, want %q", dec.Spec.Container, "aic-z2")
		}
		wantOut := "Live Zellij sessions:\n" +
			"  1) alpha (z1)\n" +
			"  2) beta (z2)\n" +
			"\n"
		if stdout.String() != wantOut {
			t.Errorf("stdout = %q, want %q", stdout.String(), wantOut)
		}
		if want := "Select session (1-2, q to quit): "; promptText != want {
			t.Errorf("prompt = %q, want %q", promptText, want)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr = %q, want empty", stderr.String())
		}
	})

	quitInputs := []string{"", "q", "Q", "  q  \n"}
	for _, in := range quitInputs {
		in := in
		t.Run("quit on "+in, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			req := AttachRequest{
				Records: records,
				Stdout:  &stdout,
				Stderr:  &stderr,
				Prompt:  func(string) (string, bool) { return in, true },
			}
			dec := ResolveAttach(req)
			if dec.Spec != nil {
				t.Error("Spec should be nil on quit")
			}
			if dec.Code != 0 {
				t.Errorf("Code = %d, want 0", dec.Code)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}

	invalidInputs := []string{"0", "3", "x", "2x", "-1", "2\r"}
	for _, in := range invalidInputs {
		in := in
		t.Run("invalid "+in, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			req := AttachRequest{
				Records: records,
				Stdout:  &stdout,
				Stderr:  &stderr,
				Prompt:  func(string) (string, bool) { return in, true },
			}
			dec := ResolveAttach(req)
			if dec.Spec != nil {
				t.Error("Spec should be nil on invalid selection")
			}
			if dec.Code != 1 {
				t.Errorf("Code = %d, want 1", dec.Code)
			}
			if stderr.String() != "Invalid selection\n" {
				t.Errorf("stderr = %q, want %q", stderr.String(), "Invalid selection\n")
			}
		})
	}

	t.Run("EOF on stdin", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		req := AttachRequest{
			Records: records,
			Stdout:  &stdout,
			Stderr:  &stderr,
			Prompt:  func(string) (string, bool) { return "", false },
		}
		dec := ResolveAttach(req)
		if dec.Spec != nil {
			t.Error("Spec should be nil on EOF")
		}
		if dec.Code != 1 {
			t.Errorf("Code = %d, want 1", dec.Code)
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr = %q, want empty", stderr.String())
		}
	})
}

func TestResolveAttachNoSessions(t *testing.T) {
	var stdout, stderr bytes.Buffer
	req := AttachRequest{
		Stdout: &stdout,
		Stderr: &stderr,
		Prompt: func(string) (string, bool) {
			t.Fatal("Prompt should not be called when no sessions are live")
			return "", false
		},
	}
	dec := ResolveAttach(req)
	if dec.Spec != nil {
		t.Error("Spec should be nil when no sessions are live")
	}
	if dec.Code != 0 {
		t.Errorf("Code = %d, want 0", dec.Code)
	}
	if want := "No live Zellij sessions\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// TestResolveAttachNilPrompt guards the nil-Prompt deref: no prompt to read
// from is the same as an immediate EOF (internal/attach/attach.go:143-146).
func TestResolveAttachNilPrompt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	req := AttachRequest{
		Records: []Record{
			{Container: "aic-z1", Session: "alpha"},
			{Container: "aic-z2", Session: "beta"},
		},
		Stdout: &stdout,
		Stderr: &stderr,
	}
	dec := ResolveAttach(req)
	if dec.Spec != nil {
		t.Error("Spec should be nil with no Prompt")
	}
	if dec.Code != 1 {
		t.Errorf("Code = %d, want 1", dec.Code)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestResolveAttachByName(t *testing.T) {
	t.Run("hit", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		req := AttachRequest{
			Session: "alpha",
			Records: []Record{{Container: "aic-z1", Session: "alpha"}},
			Stdout:  &stdout,
			Stderr:  &stderr,
		}
		dec := ResolveAttach(req)
		if dec.Spec == nil {
			t.Fatal("Spec is nil, want an ExecSpec")
		}
		if dec.Spec.Container != "aic-z1" {
			t.Errorf("Container = %q, want %q", dec.Spec.Container, "aic-z1")
		}
	})

	t.Run("miss", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		req := AttachRequest{
			Session: "alpha",
			Records: []Record{{Container: "aic-z1", Session: "other"}},
			Stdout:  &stdout,
			Stderr:  &stderr,
		}
		dec := ResolveAttach(req)
		if dec.Spec != nil {
			t.Error("Spec should be nil on a miss")
		}
		if dec.Code != 1 {
			t.Errorf("Code = %d, want 1", dec.Code)
		}
		if want := "No live Zellij session named alpha\n"; stderr.String() != want {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
	})

	t.Run("ambiguous", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		req := AttachRequest{
			Session: "alpha",
			Records: []Record{
				{Container: "aic-z1", Session: "alpha"},
				{Container: "aic-z2", Session: "alpha"},
			},
			Stdout: &stdout,
			Stderr: &stderr,
		}
		dec := ResolveAttach(req)
		if dec.Spec != nil {
			t.Error("Spec should be nil on ambiguity")
		}
		if dec.Code != 1 {
			t.Errorf("Code = %d, want 1", dec.Code)
		}
		want := "Multiple live containers report Zellij session alpha; refusing ambiguous attach\n"
		if stderr.String() != want {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
	})
}

// --- build_zellij_attach_cmd / the container command wrapper ---------------

func TestAttachCommand(t *testing.T) {
	cases := []struct {
		name       string
		srtDisable bool
		want       []string
	}{
		{"sandbox on", false, []string{"srt-run", "/usr/local/bin/zellij-attach", "alpha"}},
		{"sandbox off", true, []string{"/usr/local/bin/zellij-attach", "alpha"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AttachCommand("alpha", tc.srtDisable)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("AttachCommand = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunCommand(t *testing.T) {
	got := RunCommand("my-session", "claude-contained", []string{"claude"})

	wantGuard := "if ! command -v \"$0\" >/dev/null 2>&1; then\n" +
		"       echo \"error: claude-contained image is missing Zellij support (zellij-run).\" >&2\n" +
		"       echo \"       Rebuild it with: claude-contained --rebuild=full\" >&2\n" +
		"       exit 127\n" +
		"     fi\n" +
		"     exec \"$0\" \"$@\""
	want := []string{"bash", "-lc", wantGuard, "zellij-run", "my-session", "--", "claude"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RunCommand = %#v, want %#v", got, want)
	}

	// The hint template's %s is the launcher's program name and the product
	// line is literal, regardless of what's passed for progName. Every real
	// caller now passes runtime.ProgName for both runtimes (ticket 11 dropped
	// the second launcher name), but the parameter itself is not stripped
	// (rebuild.go's reportBuildContextError and cli.Parse keep it too, for the
	// same reason), so this exercises the templating with an arbitrary value
	// rather than asserting anything about which names are actually in use.
	arbitrary := RunCommand("s", "some-other-name", nil)
	if !strings.Contains(arbitrary[2], "claude-contained image") || !strings.Contains(arbitrary[2], "some-other-name --rebuild=full") {
		t.Errorf("guard = %q, want the product line literal and the hint naming the passed-in program", arbitrary[2])
	}
}
