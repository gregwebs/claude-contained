package plan

import (
	"reflect"
	"testing"

	"claude-contained/internal/zellij"
)

func TestContainerCommandEmptyInYieldsEmptyOut(t *testing.T) {
	got := containerCommand(nil, nil, nil, false, "", "")
	if len(got) != 0 {
		t.Errorf("containerCommand(nil, ...) = %v, want empty: no command means the image CMD runs", got)
	}
}

func TestContainerCommandPassesUserCommandThrough(t *testing.T) {
	got := containerCommand([]string{"npm", "test"}, nil, nil, false, "", "")
	want := []string{"npm", "test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// Shell mode outside Zellij discards the user command entirely and runs
// shell-run instead.
func TestContainerCommandShellModeWithoutZellij(t *testing.T) {
	got := containerCommand([]string{"npm", "test"}, nil, nil, true, "", "")
	want := []string{shellPath}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// Shell mode under Zellij substitutes plain bash, then wraps for Zellij --
// shell-run is not what a Zellij pane runs, since the pane already supplies
// a controlling terminal.
func TestContainerCommandShellModeUnderZellij(t *testing.T) {
	got := containerCommand([]string{"npm", "test"}, nil, nil, true, "sess", "prog")
	want := zellij.RunCommand("sess", "prog", []string{zellij.ShellCommand})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// Non-shell mode under Zellij wraps the user command as given.
func TestContainerCommandZellijWrapsUserCommand(t *testing.T) {
	got := containerCommand([]string{"npm", "test"}, nil, nil, false, "sess", "prog")
	want := zellij.RunCommand("sess", "prog", []string{"npm", "test"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// An empty command under Zellij still wraps: zellij.RunCommand substitutes
// bash in the pane on its own, with no launcher-side special case.
func TestContainerCommandZellijWrapsEmptyCommand(t *testing.T) {
	got := containerCommand(nil, nil, nil, false, "sess", "prog")
	want := zellij.RunCommand("sess", "prog", nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// --- mount-flag injection (#39) --------------------------------------------

func TestContainerCommandInjectsOneMount(t *testing.T) {
	injection := map[string]string{"claude": "--add-dir"}
	got := containerCommand([]string{"claude"}, []string{"/a"}, injection, false, "", "")
	want := []string{"claude", "--add-dir", "/a"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

func TestContainerCommandInjectsTwoMountsInOrder(t *testing.T) {
	injection := map[string]string{"claude": "--add-dir"}
	got := containerCommand([]string{"claude", "--model", "sonnet"}, []string{"/a", "/b"}, injection, false, "", "")
	want := []string{"claude", "--model", "sonnet", "--add-dir", "/a", "--add-dir", "/b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

func TestContainerCommandUnmatchedTokenNotInjected(t *testing.T) {
	injection := map[string]string{"claude": "--add-dir"}
	got := containerCommand([]string{"npm", "test"}, []string{"/a"}, injection, false, "", "")
	want := []string{"npm", "test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// The match is on the literal first token: a path to the same program is not
// the configured key. The launcher performs no path sniffing (ADR-0003).
func TestContainerCommandLiteralTokenMatchOnly(t *testing.T) {
	injection := map[string]string{"claude": "--add-dir"}
	got := containerCommand([]string{"/usr/local/bin/claude"}, []string{"/a"}, injection, false, "", "")
	want := []string{"/usr/local/bin/claude"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// An empty command has no first token to match, so the image CMD stays
// opaque even with a non-empty injection map.
func TestContainerCommandEmptyCommandNotInjected(t *testing.T) {
	injection := map[string]string{"claude": "--add-dir"}
	got := containerCommand(nil, []string{"/a"}, injection, false, "", "")
	if len(got) != 0 {
		t.Errorf("containerCommand(empty, ...) = %v, want empty", got)
	}
}

// -s replaces the command outright, so injection is skipped even for a
// matching token.
func TestContainerCommandShellModeSkipsInjection(t *testing.T) {
	injection := map[string]string{"claude": "--add-dir"}
	got := containerCommand([]string{"claude"}, []string{"/a"}, injection, true, "", "")
	want := []string{shellPath}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// Injection happens before the Zellij wrap: the wrapped command includes the
// injected flag/path pair.
func TestContainerCommandZellijWrapsInjectedCommand(t *testing.T) {
	injection := map[string]string{"claude": "--add-dir"}
	got := containerCommand([]string{"claude"}, []string{"/a"}, injection, false, "sess", "prog")
	want := zellij.RunCommand("sess", "prog", []string{"claude", "--add-dir", "/a"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// A nil/empty injection map is the common no-config path and must be a
// no-op regression guard against ever panicking on a nil map lookup.
func TestContainerCommandNilInjectionMapIsNoOp(t *testing.T) {
	got := containerCommand([]string{"claude"}, []string{"/a"}, nil, false, "", "")
	want := []string{"claude"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}
