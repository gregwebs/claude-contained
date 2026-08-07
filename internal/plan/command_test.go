package plan

import (
	"errors"
	"reflect"
	"testing"

	"claude-contained/internal/zellij"
)

func TestNewContainerCommandPerTool(t *testing.T) {
	cases := []struct {
		tool string
		yolo bool
		argv []string
		warn string
	}{
		{"claude", false, []string{"claude"}, ""},
		{"claude", true, []string{"claude", "--dangerously-skip-permissions"}, ""},
		{"codex", false, []string{"codex"}, ""},
		{"codex", true, []string{"codex", "--yolo"}, ""},
		{"copilot", true, []string{"copilot", "--yolo"}, ""},
		{"gemini", true, []string{"gemini", "--yolo"}, ""},
		{"vibe", false, []string{"vibe"}, ""},
		{"vibe", true, []string{"vibe"}, "Warning: vibe does not support yolo mode (no equivalent flag)"},
	}
	for _, c := range cases {
		cmd, warn, err := newContainerCommand(c.tool, c.yolo)
		if err != nil {
			t.Errorf("newContainerCommand(%q, %v): %v", c.tool, c.yolo, err)
			continue
		}
		if !reflect.DeepEqual(cmd.argv, c.argv) {
			t.Errorf("newContainerCommand(%q, %v).argv = %v, want %v", c.tool, c.yolo, cmd.argv, c.argv)
		}
		if warn != c.warn {
			t.Errorf("newContainerCommand(%q, %v) warning = %q, want %q", c.tool, c.yolo, warn, c.warn)
		}
	}
}

func TestNewContainerCommandUnknownToolIsToolError(t *testing.T) {
	_, _, err := newContainerCommand("nope", false)
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("newContainerCommand(\"nope\", false) err = %v, want *ToolError", err)
	}
	if toolErr.Tool != "nope" {
		t.Errorf("ToolError.Tool = %q, want %q", toolErr.Tool, "nope")
	}
}

// addExtraMount gates --add-dir to the two tools that understand it; the rest
// are silent no-ops so a -m mount does not leak a flag a tool cannot parse.
func TestAddExtraMountGatesByTool(t *testing.T) {
	cases := []struct {
		tool string
		want []string
	}{
		{"claude", []string{"claude", "--add-dir", "/extra"}},
		{"codex", []string{"codex", "--add-dir", "/extra"}},
		{"copilot", []string{"copilot"}},
		{"gemini", []string{"gemini"}},
		{"vibe", []string{"vibe"}},
	}
	for _, c := range cases {
		cmd, _, err := newContainerCommand(c.tool, false)
		if err != nil {
			t.Fatalf("newContainerCommand(%q): %v", c.tool, err)
		}
		cmd.addExtraMount("/extra")
		if !reflect.DeepEqual(cmd.argv, c.want) {
			t.Errorf("%s.addExtraMount: argv = %v, want %v", c.tool, cmd.argv, c.want)
		}
	}
}

// Two -m mounts each contribute their own --add-dir pair, in mount order.
func TestAddExtraMountAccumulatesInOrder(t *testing.T) {
	cmd, _, err := newContainerCommand("claude", false)
	if err != nil {
		t.Fatalf("newContainerCommand: %v", err)
	}
	cmd.addExtraMount("/a")
	cmd.addExtraMount("/b")
	want := []string{"claude", "--add-dir", "/a", "--add-dir", "/b"}
	if !reflect.DeepEqual(cmd.argv, want) {
		t.Errorf("argv = %v, want %v", cmd.argv, want)
	}
}

func TestFinishPlain(t *testing.T) {
	cmd, _, err := newContainerCommand("claude", false)
	if err != nil {
		t.Fatalf("newContainerCommand: %v", err)
	}
	got := cmd.finish([]string{"--foo"}, false, "", "")
	want := []string{"claude", "--foo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("finish = %v, want %v", got, want)
	}
}

// Shell mode outside Zellij discards the tool argv entirely and runs
// shell-run -- the trailing user args are never reached.
func TestFinishShellModeWithoutZellij(t *testing.T) {
	cmd, _, err := newContainerCommand("claude", false)
	if err != nil {
		t.Fatalf("newContainerCommand: %v", err)
	}
	got := cmd.finish([]string{"--foo"}, true, "", "")
	want := []string{shellPath}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("finish = %v, want %v", got, want)
	}
}

// Shell mode under Zellij substitutes plain bash, then wraps for Zellij --
// shell-run is not what a Zellij pane runs, since the pane already supplies a
// controlling terminal.
func TestFinishShellModeUnderZellij(t *testing.T) {
	cmd, _, err := newContainerCommand("claude", false)
	if err != nil {
		t.Fatalf("newContainerCommand: %v", err)
	}
	got := cmd.finish(nil, true, "sess", "prog")
	want := zellij.RunCommand("sess", "prog", []string{zellij.ShellCommand})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("finish = %v, want %v", got, want)
	}
}

// Non-shell mode under Zellij still wraps the ordinary tool argv, with the
// user's trailing args appended first.
func TestFinishZellijWrapsPlainCommand(t *testing.T) {
	cmd, _, err := newContainerCommand("codex", true)
	if err != nil {
		t.Fatalf("newContainerCommand: %v", err)
	}
	got := cmd.finish([]string{"--foo"}, false, "sess", "prog")
	want := zellij.RunCommand("sess", "prog", []string{"codex", "--yolo", "--foo"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("finish = %v, want %v", got, want)
	}
}
