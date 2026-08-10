package main

// probe_test.go pins the node_modules overlay's host-independent contract
// (nodeOverlayCandidates takes hostGOOS as a parameter rather than reading
// runtime.GOOS, so both branches are reachable from either host) and the
// isRegularFile guard that keeps a directory from masquerading as either a
// gitconfig or a package.json. This is the focused replacement for the
// retired goldens 49-node-modules-overlay and (its gitconfig half)
// 51-stat-semantics-regular-file-guards -- see docs/adr/0012.

import (
	"path/filepath"
	"testing"
)

func TestNodeOverlayCandidatesSkippedOnLinuxHost(t *testing.T) {
	proj := t.TempDir()
	extra := t.TempDir()
	mustWriteFile(t, filepath.Join(proj, "package.json"), `{"name":"proj"}`+"\n")
	mustWriteFile(t, filepath.Join(extra, "package.json"), `{"name":"extra"}`+"\n")

	dirs, targetEmpty := nodeOverlayCandidates("linux", "arm64", proj, []string{extra}, []string{"rw"})

	if len(dirs) != 0 {
		t.Errorf("dirs = %v, want none: the overlay is pointless when the host is already Linux", dirs)
	}
	if len(targetEmpty) != 0 {
		t.Errorf("targetEmpty = %v, want empty", targetEmpty)
	}
}

func TestNodeOverlayCandidatesOnDarwinHost(t *testing.T) {
	proj := t.TempDir()
	rwExtra := t.TempDir()
	roExtra := t.TempDir()
	// A read-write extra whose package.json is a *directory*, not a regular
	// file: the `-f` guard must reject it as a candidate rather than treat it
	// as a Node project and create an overlay bash never would. It is a real
	// candidate root (an rw extra), so this genuinely exercises the guard --
	// unlike a non-candidate subdirectory, which nodeOverlayCandidates never
	// inspects.
	dirPkgExtra := t.TempDir()

	mustWriteFile(t, filepath.Join(proj, "package.json"), `{"name":"proj"}`+"\n")
	mustWriteFile(t, filepath.Join(rwExtra, "package.json"), `{"name":"rw-extra"}`+"\n")
	mustWriteFile(t, filepath.Join(roExtra, "package.json"), `{"name":"ro-extra"}`+"\n")
	mustMkdirAll(t, filepath.Join(dirPkgExtra, "package.json"))

	dirs, targetEmpty := nodeOverlayCandidates(
		"darwin", "arm64", proj,
		[]string{rwExtra, roExtra, dirPkgExtra},
		[]string{"rw", "ro", "rw"},
	)

	want := []string{proj, rwExtra}
	if len(dirs) != len(want) {
		t.Fatalf("dirs = %v, want %v", dirs, want)
	}
	for i, d := range want {
		if dirs[i] != d {
			t.Errorf("dirs[%d] = %q, want %q (ro extras skip, order preserved)", i, dirs[i], d)
		}
	}

	if !targetEmpty[proj] {
		t.Errorf("targetEmpty[proj] = false, want true: the overlay dir does not exist yet")
	}
	if !targetEmpty[rwExtra] {
		t.Errorf("targetEmpty[rwExtra] = false, want true: the overlay dir does not exist yet")
	}
	if _, ok := targetEmpty[roExtra]; ok {
		t.Errorf("targetEmpty has an entry for the skipped read-only extra: %v", targetEmpty)
	}

	// A pre-existing, non-empty overlay dir reports targetEmpty == false.
	prebuilt := filepath.Join(rwExtra, ".claude-contained", "node_modules-linux-arm64", "prebuilt")
	mustMkdirAll(t, prebuilt)
	mustWriteFile(t, filepath.Join(prebuilt, "index.js"), "x\n")

	_, targetEmpty = nodeOverlayCandidates("darwin", "arm64", proj, []string{rwExtra, roExtra}, []string{"rw", "ro"})
	if targetEmpty[rwExtra] {
		t.Errorf("targetEmpty[rwExtra] = true, want false: the overlay dir already exists and is non-empty")
	}
}

func TestGitConfigRegularFileGuard(t *testing.T) {
	regularHome := t.TempDir()
	mustWriteFile(t, filepath.Join(regularHome, ".gitconfig"), "[user]\n\tname = Test\n")
	if !isRegularFile(filepath.Join(regularHome, ".gitconfig")) {
		t.Error("isRegularFile on a real ~/.gitconfig = false, want true")
	}

	dirHome := t.TempDir()
	mustMkdirAll(t, filepath.Join(dirHome, ".gitconfig"))
	if isRegularFile(filepath.Join(dirHome, ".gitconfig")) {
		t.Error("isRegularFile on a directory named ~/.gitconfig = true, want false: a directory is not a file to copy")
	}

	missingHome := t.TempDir()
	if isRegularFile(filepath.Join(missingHome, ".gitconfig")) {
		t.Error("isRegularFile on a missing ~/.gitconfig = true, want false")
	}
}
