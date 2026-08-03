package plan

import (
	"errors"
	"strings"
	"testing"

	"claude-contained/internal/host"
	"claude-contained/internal/runtime"
)

// testPaths builds hostPaths the way Build does, from a fixed fake home.
func testPaths() hostPaths {
	return newHostPaths(host.State{Home: "/home/u"}, false)
}

// hasSystemRemount reports whether args carries the Codex system-skills remount
// (dst <codex>/skills/.system over itself, read-only).
func hasSystemRemount(paths hostPaths, args []runtime.Arg) bool {
	want := runtime.MountArg{
		Src:      paths.CodexDir + "/skills/.system",
		Dst:      paths.CodexDir + "/skills/.system",
		ReadOnly: true,
	}
	for _, a := range args {
		if m, ok := a.(runtime.MountArg); ok && m == want {
			return true
		}
	}
	return false
}

// On a runtime that cannot create a mount point under a read-only parent, a
// shared dir with no `.system` mount point makes the Codex system remount
// impossible -- the launcher must refuse it up front, before emitting any step
// or mount, with the actionable mkdir fix rather than a cryptic runtime errno.
func TestSharedSkillsPreflightRefusesUnlandableSystemRemount(t *testing.T) {
	paths := testPaths()
	ss := SharedSkills{Dir: "/share", CodexSystemDir: true, DirHasSystem: false}

	steps, args, err := sharedSkillsMounts(newMountRegistry("/proj"), paths, ss, true)

	var shareErr *ShareSkillsError
	if !errors.As(err, &shareErr) {
		t.Fatalf("err = %v, want *ShareSkillsError", err)
	}
	if steps != nil || args != nil {
		t.Fatalf("preflight must fail before emitting anything: steps=%v args=%v", steps, args)
	}
	joined := strings.Join(shareErr.Lines, "\n")
	for _, want := range []string{"mkdir -p /share/.system", "/share", paths.CodexDir + "/skills/.system"} {
		if !strings.Contains(joined, want) {
			t.Errorf("error message missing %q:\n%s", want, joined)
		}
	}
}

// The same runtime, once the shared dir carries a `.system` mount point (the
// documented workaround), lands the remount normally -- no error, and the
// remount is emitted.
func TestSharedSkillsRemountLandsWhenSharedDirHasMountpoint(t *testing.T) {
	paths := testPaths()
	ss := SharedSkills{Dir: "/share", CodexSystemDir: true, DirHasSystem: true}

	_, args, err := sharedSkillsMounts(newMountRegistry("/proj"), paths, ss, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasSystemRemount(paths, args) {
		t.Error("Codex system remount was not emitted despite a present mount point")
	}
}

// A runtime that creates mount destinations itself (Docker) never needs the
// mount point, so a missing `.system` in the shared dir is not an error and the
// remount is still emitted.
func TestSharedSkillsRemountEmittedWhenRuntimeCreatesMountpoint(t *testing.T) {
	paths := testPaths()
	ss := SharedSkills{Dir: "/share", CodexSystemDir: true, DirHasSystem: false}

	_, args, err := sharedSkillsMounts(newMountRegistry("/proj"), paths, ss, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasSystemRemount(paths, args) {
		t.Error("Codex system remount was not emitted on a runtime that creates mount points")
	}
}

// With no Codex `.system` on the host there is no remount to land, so the
// preflight never fires even on the strict runtime with a mount-point-less
// shared dir.
func TestSharedSkillsPreflightInertWithoutCodexSystemDir(t *testing.T) {
	paths := testPaths()
	ss := SharedSkills{Dir: "/share", CodexSystemDir: false, DirHasSystem: false}

	_, _, err := sharedSkillsMounts(newMountRegistry("/proj"), paths, ss, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
