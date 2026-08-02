package layer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const testBaseID = "sha256:base00"

// writeLayerFile writes one file and chmods it explicitly. The chmod matters:
// os.WriteFile's mode is masked by the process umask, and several cases below
// are *about* what the mode contributes to the hash, so the bits have to be the
// ones the case names rather than the ones the developer's umask allows.
func writeLayerFile(t *testing.T, dir, rel, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// digestOf is the content hash the stream carries for one file or symlink
// target. Written as a call rather than a literal because a 64-character hex
// constant can be neither typed nor reviewed by hand -- the *format* is what
// this file pins literally, field by field.
func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func mustHash(t *testing.T, dir string) string {
	t.Helper()
	digest, _, _, err := hashContext(dir, testBaseID)
	if err != nil {
		t.Fatalf("hashContext: %v", err)
	}
	return digest
}

func mustStream(t *testing.T, dir string) string {
	t.Helper()
	stream, _, _, err := canonicalStream(dir, testBaseID)
	if err != nil {
		t.Fatalf("canonicalStream: %v", err)
	}
	return string(stream)
}

// The specification. A small fixed tree, and the exact bytes it must produce,
// with every NUL spelled out. Everything else in this package is downstream of
// this format; if this test and the code disagree, this test is right.
func TestCanonicalStreamIsExactlyTheDocumentedFormat(t *testing.T) {
	dir := t.TempDir()
	writeLayerFile(t, dir, "Dockerfile", "FROM scratch\n", 0o644)
	writeLayerFile(t, dir, "scripts/a.sh", "echo hi\n", 0o755)
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target-does-not-exist", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	want := "claude-contained-layer\x00v1\x00" +
		"base\x00" + testBaseID + "\x00" +
		// Flat sorted relative paths: Dockerfile, empty, link, scripts,
		// scripts/a.sh. Fields are relPath, kind, mode, size, content.
		"Dockerfile\x00file\x000644\x0013\x00" + digestOf("FROM scratch\n") + "\x00" +
		"empty\x00dir\x00\x00\x00\x00" +
		"link\x00symlink\x00\x0021\x00" + digestOf("target-does-not-exist") + "\x00" +
		"scripts\x00dir\x00\x00\x00\x00" +
		"scripts/a.sh\x00file\x000755\x008\x00" + digestOf("echo hi\n") + "\x00"

	got := mustStream(t, dir)
	if got != want {
		t.Errorf("canonical stream mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestHashIsStableAcrossRepeatedCalls(t *testing.T) {
	dir := t.TempDir()
	writeLayerFile(t, dir, "Dockerfile", "FROM scratch\n", 0o644)

	if first, second := mustHash(t, dir), mustHash(t, dir); first != second {
		t.Errorf("hash is not stable: %s then %s", first, second)
	}
}

// Checklist item 6 at the unit level: the base image's identity is *in* the
// hash, so rebuilding the base invalidates every derived image with no flag.
func TestBaseImageIDParticipatesInTheHash(t *testing.T) {
	dir := t.TempDir()
	writeLayerFile(t, dir, "Dockerfile", "FROM scratch\n", 0o644)

	before, _, _, err := hashContext(dir, "sha256:aaaa")
	if err != nil {
		t.Fatal(err)
	}
	after, _, _, err := hashContext(dir, "sha256:bbbb")
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("changing only the base image ID must change the hash")
	}
}

func TestContentChangesTheHash(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, dir string)
	}{
		{
			"one byte of the Dockerfile",
			func(t *testing.T, dir string) { writeLayerFile(t, dir, "Dockerfile", "FROM scratch \n", 0o644) },
		},
		{
			"nested file content",
			func(t *testing.T, dir string) { writeLayerFile(t, dir, "scripts/a.sh", "echo bye\n", 0o755) },
		},
		{
			"adding a file",
			func(t *testing.T, dir string) { writeLayerFile(t, dir, "new.txt", "x\n", 0o644) },
		},
		{
			// Paths are hashed, so identical content under a different name is
			// a different context.
			"renaming a file with identical content",
			func(t *testing.T, dir string) {
				if err := os.Rename(filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			// A COPY makes an empty directory observable in the image.
			"adding an empty directory",
			func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"setting the execute bit",
			func(t *testing.T, dir string) {
				if err := os.Chmod(filepath.Join(dir, "a.txt"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeLayerFile(t, dir, "Dockerfile", "FROM scratch\n", 0o644)
			writeLayerFile(t, dir, "scripts/a.sh", "echo hi\n", 0o755)
			writeLayerFile(t, dir, "a.txt", "content\n", 0o644)

			before := mustHash(t, dir)
			tc.mutate(t, dir)
			if after := mustHash(t, dir); after == before {
				t.Errorf("%s must change the hash, but it stayed %s", tc.name, before)
			}
		})
	}
}

// The counterpart to the execute-bit case, and the reason gitMode exists:
// permissions vary with the checkout's umask, so anything but the execute bit
// must be invisible or two developers on the same commit get two tags.
func TestNonExecutableModeBitsDoNotChangeTheHash(t *testing.T) {
	dir := t.TempDir()
	writeLayerFile(t, dir, "Dockerfile", "FROM scratch\n", 0o644)
	writeLayerFile(t, dir, "a.txt", "content\n", 0o644)

	before := mustHash(t, dir)
	if err := os.Chmod(filepath.Join(dir, "a.txt"), 0o640); err != nil {
		t.Fatal(err)
	}
	if after := mustHash(t, dir); after != before {
		t.Errorf("chmod 0640 changed the hash (%s -> %s); only the execute bit is tracked", before, after)
	}
}

func TestRemovingAnAddedFileRestoresTheHashExactly(t *testing.T) {
	dir := t.TempDir()
	writeLayerFile(t, dir, "Dockerfile", "FROM scratch\n", 0o644)

	original := mustHash(t, dir)
	writeLayerFile(t, dir, "extra.txt", "x\n", 0o644)
	if mustHash(t, dir) == original {
		t.Fatal("adding a file did not change the hash")
	}
	if err := os.Remove(filepath.Join(dir, "extra.txt")); err != nil {
		t.Fatal(err)
	}
	if restored := mustHash(t, dir); restored != original {
		t.Errorf("hash after removal = %s, want the original %s", restored, original)
	}
}

// Two trees with identical final contents, created in opposite orders, must
// hash identically: nothing may depend on readdir order.
func TestCreationOrderDoesNotChangeTheHash(t *testing.T) {
	forward := t.TempDir()
	writeLayerFile(t, forward, "Dockerfile", "FROM scratch\n", 0o644)
	writeLayerFile(t, forward, "a.txt", "a\n", 0o644)
	writeLayerFile(t, forward, "b.txt", "b\n", 0o644)
	writeLayerFile(t, forward, "z/deep.txt", "z\n", 0o644)

	backward := t.TempDir()
	writeLayerFile(t, backward, "z/deep.txt", "z\n", 0o644)
	writeLayerFile(t, backward, "b.txt", "b\n", 0o644)
	writeLayerFile(t, backward, "a.txt", "a\n", 0o644)
	writeLayerFile(t, backward, "Dockerfile", "FROM scratch\n", 0o644)

	if mustHash(t, forward) != mustHash(t, backward) {
		t.Error("identical trees built in opposite orders must hash identically")
	}
}

// The flat-versus-hierarchical trap, asserted on the ordering directly rather
// than only through the digest: "a.txt" sorts before "a/b" flatly ('.' is
// 0x2E, '/' is 0x2F) and after it hierarchically, and a walk-order-dependent
// implementation would silently pick the other one.
func TestFlatSortOrdersDottedNamesBeforeSiblingDirectories(t *testing.T) {
	dir := t.TempDir()
	writeLayerFile(t, dir, "Dockerfile", "FROM scratch\n", 0o644)
	writeLayerFile(t, dir, "a.txt", "flat\n", 0o644)
	writeLayerFile(t, dir, "a/b", "nested\n", 0o644)

	stream := mustStream(t, dir)
	dotted := strings.Index(stream, "a.txt\x00")
	nested := strings.Index(stream, "a/b\x00")
	if dotted < 0 || nested < 0 {
		t.Fatalf("both entries must appear in the stream: %q", stream)
	}
	if dotted > nested {
		t.Error("a.txt must precede a/b: the enumeration sorts full relative paths flatly, not per directory")
	}

	// And two independently built copies must agree, which is what the flat
	// rule buys: a stateable, reproducible order.
	other := t.TempDir()
	writeLayerFile(t, other, "a/b", "nested\n", 0o644)
	writeLayerFile(t, other, "a.txt", "flat\n", 0o644)
	writeLayerFile(t, other, "Dockerfile", "FROM scratch\n", 0o644)
	if mustHash(t, dir) != mustHash(t, other) {
		t.Error("two copies of the same tree must hash identically")
	}
}

func TestSymlinksAreHashedByTargetStringAndNeverFollowed(t *testing.T) {
	dir := t.TempDir()
	writeLayerFile(t, dir, "Dockerfile", "FROM scratch\n", 0o644)
	writeLayerFile(t, dir, "real.txt", "one\n", 0o644)
	if err := os.Symlink("real.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	before := mustHash(t, dir)

	// Changing the *target's* contents must not move the hash -- but only
	// because the symlink does not follow: real.txt is itself in the context,
	// so it is rewritten to the same bytes to isolate the property.
	writeLayerFile(t, dir, "real.txt", "one\n", 0o644)
	if after := mustHash(t, dir); after != before {
		t.Errorf("rewriting identical bytes changed the hash: %s -> %s", before, after)
	}

	// Retargeting does change it: the target string is the hashed content.
	if err := os.Remove(filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if after := mustHash(t, dir); after == before {
		t.Error("retargeting a symlink must change the hash")
	}
}

func TestDanglingSymlinkIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	writeLayerFile(t, dir, "Dockerfile", "FROM scratch\n", 0o644)
	if err := os.Symlink("nothing-is-here", filepath.Join(dir, "broken")); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := hashContext(dir, testBaseID); err != nil {
		t.Errorf("a dangling symlink must be hashable, not an error: %v", err)
	}
}

// Opening a fifo blocks forever, so it must contribute a line and never be
// read -- the same trap completeEnv documents for the project env file.
func TestFifoIsHashedWithoutBeingRead(t *testing.T) {
	dir := t.TempDir()
	writeLayerFile(t, dir, "Dockerfile", "FROM scratch\n", 0o644)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Skipf("this filesystem does not support fifos: %v", err)
	}

	stream, _, _, err := canonicalStream(dir, testBaseID)
	if err != nil {
		t.Fatalf("a fifo must not be an error: %v", err)
	}
	if !strings.Contains(string(stream), "pipe\x00other\x00") {
		t.Errorf("fifo must contribute an `other` line: %q", stream)
	}
}

// No .dockerignore interpretation: the file is hashed like any other, so
// editing it invalidates. Over-hashing costs a spurious rebuild; under-hashing
// costs a stale toolchain.
func TestDockerignoreIsHashedLikeAnyOtherFile(t *testing.T) {
	dir := t.TempDir()
	writeLayerFile(t, dir, "Dockerfile", "FROM scratch\n", 0o644)
	writeLayerFile(t, dir, "ignored.txt", "content\n", 0o644)
	writeLayerFile(t, dir, ".dockerignore", "ignored.txt\n", 0o644)

	before := mustHash(t, dir)
	writeLayerFile(t, dir, ".dockerignore", "something-else.txt\n", 0o644)
	if after := mustHash(t, dir); after == before {
		t.Error("editing .dockerignore must change the hash: it is hashed, not interpreted")
	}

	// And the supposedly-ignored file still participates.
	before = mustHash(t, dir)
	writeLayerFile(t, dir, "ignored.txt", "changed\n", 0o644)
	if after := mustHash(t, dir); after == before {
		t.Error("a file named in .dockerignore is still hashed")
	}
}

// The size policy lives in the caller (cmd/claude-contained/layer.go warns);
// this package refuses nothing, because the layer directory is writable from
// inside the container and a hard limit would let a contained agent disable its
// own project's launcher.
func TestLargeContextIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	writeLayerFile(t, dir, "Dockerfile", "FROM scratch\n", 0o644)
	const files = 10_001
	for i := range files {
		writeLayerFile(t, dir, fmt.Sprintf("f%05d.txt", i), "x\n", 0o644)
	}

	_, count, hashedBytes, err := hashContext(dir, testBaseID)
	if err != nil {
		t.Fatalf("an oversized context must still hash: %v", err)
	}
	if count <= 10_000 {
		t.Errorf("count = %d, want more than 10000", count)
	}
	if hashedBytes <= 0 {
		t.Errorf("hashedBytes = %d, want the bytes actually read", hashedBytes)
	}
}
