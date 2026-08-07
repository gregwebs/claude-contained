package plan

import (
	"reflect"
	"testing"

	"claude-contained/internal/zellij"
)

func TestContainerCommandEmptyInYieldsEmptyOut(t *testing.T) {
	got := containerCommand(nil, false, "", "")
	if len(got) != 0 {
		t.Errorf("containerCommand(nil, ...) = %v, want empty: no command means the image CMD runs", got)
	}
}

func TestContainerCommandPassesUserCommandThrough(t *testing.T) {
	got := containerCommand([]string{"npm", "test"}, false, "", "")
	want := []string{"npm", "test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// Shell mode outside Zellij discards the user command entirely and runs
// shell-run instead.
func TestContainerCommandShellModeWithoutZellij(t *testing.T) {
	got := containerCommand([]string{"npm", "test"}, true, "", "")
	want := []string{shellPath}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// Shell mode under Zellij substitutes plain bash, then wraps for Zellij --
// shell-run is not what a Zellij pane runs, since the pane already supplies a
// controlling terminal.
func TestContainerCommandShellModeUnderZellij(t *testing.T) {
	got := containerCommand([]string{"npm", "test"}, true, "sess", "prog")
	want := zellij.RunCommand("sess", "prog", []string{zellij.ShellCommand})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// Non-shell mode under Zellij wraps the user command as given.
func TestContainerCommandZellijWrapsUserCommand(t *testing.T) {
	got := containerCommand([]string{"npm", "test"}, false, "sess", "prog")
	want := zellij.RunCommand("sess", "prog", []string{"npm", "test"})
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}

// An empty command under Zellij still wraps: zellij.RunCommand substitutes
// bash in the pane on its own, with no launcher-side special case.
func TestContainerCommandZellijWrapsEmptyCommand(t *testing.T) {
	got := containerCommand(nil, false, "sess", "prog")
	want := zellij.RunCommand("sess", "prog", nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("containerCommand = %v, want %v", got, want)
	}
}
