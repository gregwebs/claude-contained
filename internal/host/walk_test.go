package host

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// TestScanSymlinksMatchesFind is the seam: it builds a tree with several
// symlinks per directory, nested real directories and a symlinked directory
// that must not be descended into, then compares ScanSymlinks against the
// system find(1) run against the very same tree. Hardcoding an expected order
// would only encode an assumption about readdir order; running find on the
// tree this test just built is what actually pins the order.
func TestScanSymlinksMatchesFind(t *testing.T) {
	if _, err := exec.LookPath("find"); err != nil {
		t.Skip("find(1) not available")
	}

	root := t.TempDir()
	target := filepath.Join(root, "target-file")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	mustMkdir := func(p string) string {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		return full
	}
	mustSymlink := func(dir, name string) {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}

	a := mustMkdir("a")
	a1 := mustMkdir("a/a1")
	b := mustMkdir("b")
	b1 := mustMkdir("b/b1")

	// Several links per directory, at more than one nesting depth.
	mustSymlink(root, "root-link-1")
	mustSymlink(root, "root-link-2")
	mustSymlink(a, "a-link-1")
	mustSymlink(a, "a-link-2")
	mustSymlink(a1, "a1-link")
	mustSymlink(b, "b-link")
	mustSymlink(b1, "b1-link-1")
	mustSymlink(b1, "b1-link-2")

	// A symlinked directory. find -type l reports the symlink itself but does
	// not descend into it (no -L), so nothing under realDir should appear
	// under this alias, and a self-referential loop would otherwise hang.
	realDir := mustMkdir("real-dir")
	mustSymlink(realDir, "inside-real-dir-link")
	if err := os.Symlink(realDir, filepath.Join(root, "dir-link")); err != nil {
		t.Fatal(err)
	}

	// A plain file and an empty directory, neither of which find -type l
	// should ever report.
	if err := os.WriteFile(filepath.Join(root, "plain-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustMkdir("empty-dir")

	got, err := ScanSymlinks(root)
	if err != nil {
		t.Fatalf("ScanSymlinks: %v", err)
	}

	// find is invoked via os/exec, which resolves the binary directly with no
	// shell in between -- so a shell function or alias named "find" in the
	// caller's interactive environment cannot shadow it here.
	var stdout bytes.Buffer
	cmd := exec.Command("find", root, "-type", "l", "-print0")
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("find: %v", err)
	}
	var want []string
	for _, p := range bytes.Split(bytes.TrimSuffix(stdout.Bytes(), []byte{0}), []byte{0}) {
		if len(p) > 0 {
			want = append(want, string(p))
		}
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScanSymlinks order diverges from find:\n got:  %#v\n want: %#v", got, want)
	}
}

func TestScanSymlinksEmptyDir(t *testing.T) {
	root := t.TempDir()
	got, err := ScanSymlinks(root)
	if err != nil {
		t.Fatalf("ScanSymlinks: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ScanSymlinks(empty dir) = %#v, want empty", got)
	}
}

func TestScanSymlinksMissingRoot(t *testing.T) {
	if _, err := ScanSymlinks(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("ScanSymlinks(missing root) = nil error, want an error")
	}
}
