package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireGit skips rather than fails: PlaceholderIsTracked degrades to
// "untracked" without git, which would turn a missing tool into a confusing
// assertion failure about a deleted file.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git(1) not available")
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// exists uses Lstat so a dangling symlink still counts as present -- surviving
// is exactly what a dangling symlink is supposed to do here.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// TestCleanupPlaceholderFiles exercises all four of bash's guards in a single
// sweep, inside a real repository so "tracked" is decided by git itself rather
// than by a stub. This is the only launcher mutation that deletes files inside
// the user's project directory, so every guard gets its own entry.
func TestCleanupPlaceholderFiles(t *testing.T) {
	requireGit(t)

	// ResolvePath, not the raw t.TempDir(): on macOS the temp root sits under
	// the /var -> /private/var symlink and `git rev-parse --show-toplevel`
	// answers with the resolved path, which PlaceholderIsTracked trims off the
	// file path as a literal prefix. An unresolved root makes every file look
	// untracked, and the tracked-file case below would be deleted for a reason
	// that has nothing to do with the guard it checks. Production is already
	// resolved: run.go passes mainHost, i.e. ResolvePath(projectDir).
	root := ResolvePath(t.TempDir())
	mustGit(t, root, "init", "--quiet")

	p := func(name string) string { return filepath.Join(root, name) }

	// Zero-byte, untracked, not a symlink: the only combination that is removed.
	writeFile(t, p(".gitconfig"), "")
	// A second one, late in the name list, so the whole list is proven walked.
	writeFile(t, p(".ripgreprc"), "")

	// Non-empty: bash's `-s` is true, so it survives.
	writeFile(t, p(".mcp.json"), "{}\n")

	// A symlink whose target is itself a zero-byte regular file. bash's `-f`
	// follows and its `! -L` does not, so this survives; a port using Stat
	// where placeholder.go uses Lstat would delete it.
	writeFile(t, p("empty-target"), "")
	if err := os.Symlink(p("empty-target"), p(".bashrc")); err != nil {
		t.Fatal(err)
	}

	// A dangling symlink: nothing to follow, still never removed.
	if err := os.Symlink(p("no-such-file"), p(".zshrc")); err != nil {
		t.Fatal(err)
	}

	// A directory wearing a placeholder's name.
	if err := os.Mkdir(p(".zprofile"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Zero-byte and otherwise indistinguishable from a placeholder, but tracked.
	// `add -f` in case a user's global excludesFile happens to cover the name.
	writeFile(t, p(".profile"), "")
	mustGit(t, root, "add", "-f", ".profile")

	// Not in the name list at all.
	writeFile(t, p("not-a-placeholder"), "")

	CleanupPlaceholderFiles(root)

	cases := []struct {
		name string
		want bool
		why  string
	}{
		{".gitconfig", false, "zero-byte, untracked, not a symlink"},
		{".ripgreprc", false, "zero-byte, untracked, late in the name list"},
		{".mcp.json", true, "not empty"},
		{".bashrc", true, "a symlink, even one pointing at an empty file"},
		{"empty-target", true, "not a placeholder name"},
		{".zshrc", true, "a dangling symlink"},
		{".zprofile", true, "a directory, not a regular file"},
		{".profile", true, "tracked by git"},
		{"not-a-placeholder", true, "not a placeholder name"},
	}
	for _, tc := range cases {
		if got := exists(p(tc.name)); got != tc.want {
			t.Errorf("%s: exists = %v, want %v (%s)", tc.name, got, tc.want, tc.why)
		}
	}
}

// TestCleanupPlaceholderFilesMultipleRoots covers the variadic call shape the
// launcher actually uses (project dir plus every extra mount) and the two
// non-directory arguments bash's `[[ -d "$root" ]]` skips. The roots here are
// deliberately outside any repository, which is the common case.
func TestCleanupPlaceholderFilesMultipleRoots(t *testing.T) {
	base := ResolvePath(t.TempDir())
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	for _, d := range []string{a, b} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(d, ".bashrc"), "")
	}

	notADir := filepath.Join(base, "regular-file")
	writeFile(t, notADir, "x")
	missing := filepath.Join(base, "no-such-dir")

	// The bad roots sit between the good ones: a port that stopped at the first
	// unusable root would leave b's placeholder behind.
	CleanupPlaceholderFiles(a, missing, notADir, b)

	if exists(filepath.Join(a, ".bashrc")) {
		t.Error("first root: .bashrc survived")
	}
	if exists(filepath.Join(b, ".bashrc")) {
		t.Error("root after a missing and a non-directory root: .bashrc survived")
	}
	if !exists(notADir) {
		t.Error("a non-directory root was itself removed")
	}
}

func TestPlaceholderIsTracked(t *testing.T) {
	requireGit(t)

	repo := ResolvePath(t.TempDir())
	mustGit(t, repo, "init", "--quiet")
	writeFile(t, filepath.Join(repo, ".gitconfig"), "")
	writeFile(t, filepath.Join(repo, ".mcp.json"), "")
	mustGit(t, repo, "add", "-f", ".gitconfig")

	if !PlaceholderIsTracked(filepath.Join(repo, ".gitconfig")) {
		t.Error("a file in the index reported as untracked")
	}
	if PlaceholderIsTracked(filepath.Join(repo, ".mcp.json")) {
		t.Error("a file not in the index reported as tracked")
	}

	// Outside any repository. Asserted rather than assumed: if the temp root
	// ever landed inside a repository, this case would silently stop testing
	// anything at all.
	outside := ResolvePath(t.TempDir())
	if _, err := exec.Command("git", "-C", outside, "rev-parse", "--show-toplevel").Output(); err == nil {
		t.Skip("temp dir is inside a git repository; the outside-a-repo case is untestable here")
	}
	writeFile(t, filepath.Join(outside, ".gitconfig"), "")
	if PlaceholderIsTracked(filepath.Join(outside, ".gitconfig")) {
		t.Error("a file outside any repository reported as tracked")
	}
}
