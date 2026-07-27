package host

import (
	"os"
	"path/filepath"
	"testing"
)

// The tolerance of missing paths is the whole point: the launcher applies
// resolution to mount paths with no prior existence check, so failing here --
// as filepath.EvalSymlinks would -- changes when and how invalid paths are
// reported.
func TestResolvePathTolerateMissing(t *testing.T) {
	base := realTempDir(t)
	missing := filepath.Join(base, "nope", "not", "here")

	if got := ResolvePath(missing); got != missing {
		t.Errorf("ResolvePath(%q) = %q, want the path back unchanged", missing, got)
	}
}

func TestResolvePathMissingUnderSymlinkedParent(t *testing.T) {
	base := realTempDir(t)
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	// The existing prefix resolves through the symlink; the missing tail is
	// appended rather than rejected.
	got := ResolvePath(filepath.Join(link, "absent"))
	want := filepath.Join(real, "absent")
	if got != want {
		t.Errorf("ResolvePath through symlinked parent = %q, want %q", got, want)
	}
}

func TestResolvePathSymlinkChain(t *testing.T) {
	base := realTempDir(t)
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	if err := os.Symlink(target, first); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(first, second); err != nil {
		t.Fatal(err)
	}

	if got := ResolvePath(second); got != target {
		t.Errorf("ResolvePath(%q) = %q, want %q", second, got, target)
	}
}

// ".." must mean "parent of what the preceding component resolved to", not
// "textually cancel the preceding name". filepath.Join/Clean does the latter,
// which is why the relative-path branch concatenates instead. Verified against
// python3's os.path.realpath, which is what the bash launcher actually runs.
func TestResolvePathDotDotAfterSymlinkIsNotTextual(t *testing.T) {
	base := realTempDir(t)
	if err := os.MkdirAll(filepath.Join(base, "real", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(base, "work")
	if err := os.Mkdir(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../real/sub", filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}

	// Built by concatenation, not filepath.Join: Join cleans its result, so it
	// would collapse "link/.." before ResolvePath ever saw it -- which is the
	// very mistake under test.
	want := filepath.Join(base, "real")
	if got := ResolvePath(work + "/link/.."); got != want {
		t.Errorf("ResolvePath(abs link/..) = %q, want %q", got, want)
	}

	// Relative form, which is the one Clean would have flattened before the
	// component walk ever saw it.
	restore := chdir(t, work)
	defer restore()
	if got := ResolvePath("link/.."); got != want {
		t.Errorf("ResolvePath(rel link/..) = %q, want %q", got, want)
	}
}

func chdir(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(prev) }
}

func TestResolvePathSymlinkLoopTerminates(t *testing.T) {
	base := realTempDir(t)
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}

	// The assertion is simply that this returns rather than spinning forever.
	_ = ResolvePath(a)
}

func TestPathHash8(t *testing.T) {
	// sha256("/tmp/project") begins with these eight hex characters; the
	// differential harness recomputes the same value to neutralize it, so the
	// two have to agree exactly.
	got := PathHash8("/tmp/project")
	if len(got) != 8 {
		t.Fatalf("PathHash8 returned %q, want 8 characters", got)
	}
	if got != PathHash8("/tmp/project") {
		t.Error("PathHash8 is not deterministic")
	}
	if PathHash8("/tmp/other") == got {
		t.Error("PathHash8 collided on distinct paths")
	}
}

func TestDirIsEmpty(t *testing.T) {
	base := realTempDir(t)

	empty := filepath.Join(base, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if !DirIsEmpty(empty) {
		t.Error("a freshly created directory should be empty")
	}

	// A dotfile counts: the bash globs cover ".[!.]*" as well as "*".
	if err := os.WriteFile(filepath.Join(empty, ".hidden"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if DirIsEmpty(empty) {
		t.Error("a directory holding only a dotfile should not be empty")
	}
}

// realTempDir resolves t.TempDir, because on macOS it sits under /var, which is
// itself a symlink to /private/var -- and comparing an unresolved fixture path
// against a resolved result would fail for the wrong reason.
func realTempDir(t *testing.T) string {
	t.Helper()
	return ResolvePath(t.TempDir())
}
