package imagescript

// Tests for image/claude-native-link.sh, which creates the native-shaped Claude
// launcher link Claude Code expects: ~/.local/bin/claude -> versions/<v> ->
// the real claude binary, deriving <v> from `claude --version`. An existing
// launcher link must be left untouched.

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeClaude writes a stand-in claude binary that reports a version. The script
// execs it for `--version`, so a real executable (not a Go value) is the
// contract; the shell content is a single printf.
func fakeClaude(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := "#!/usr/bin/env bash\nprintf '" + version + " (Claude Code)\\n'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClaudeNativeLinkCreatesVersionedSymlinkChain(t *testing.T) {
	home := t.TempDir()
	claude := fakeClaude(t, "1.2.3")

	res := runScript(t, scriptOpts{
		Script: scriptPath(t, "claude-native-link.sh"),
		Args:   []string{home},
		Env:    []string{"CLAUDE_CONTAINED_CLAUDE_BIN=" + claude},
		Home:   home,
	})
	if res.Code != 0 {
		t.Fatalf("exit %d, want 0\nstderr:\n%s", res.Code, res.Stderr)
	}

	binLink := filepath.Join(home, ".local", "bin", "claude")
	versionLink := filepath.Join(home, ".local", "share", "claude", "versions", "1.2.3")

	if got := readlink(t, binLink); got != versionLink {
		t.Errorf("~/.local/bin/claude -> %q, want the versioned link %q", got, versionLink)
	}
	if got := readlink(t, versionLink); got != claude {
		t.Errorf("versions/1.2.3 -> %q, want the claude binary %q", got, claude)
	}
}

func TestClaudeNativeLinkPreservesExistingLink(t *testing.T) {
	home := t.TempDir()
	claude := fakeClaude(t, "1.2.3")

	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(t.TempDir(), "existing-claude")
	if err := os.WriteFile(existing, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	binLink := filepath.Join(binDir, "claude")
	if err := os.Symlink(existing, binLink); err != nil {
		t.Fatal(err)
	}

	res := runScript(t, scriptOpts{
		Script: scriptPath(t, "claude-native-link.sh"),
		Args:   []string{home},
		Env:    []string{"CLAUDE_CONTAINED_CLAUDE_BIN=" + claude},
		Home:   home,
	})
	if res.Code != 0 {
		t.Fatalf("exit %d, want 0\nstderr:\n%s", res.Code, res.Stderr)
	}

	if got := readlink(t, binLink); got != existing {
		t.Errorf("existing launcher link was rewritten to %q, want it preserved as %q", got, existing)
	}
}

func readlink(t *testing.T, path string) string {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatalf("readlink %s: %v", path, err)
	}
	return target
}
