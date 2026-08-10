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

func TestSharedSkillsMountsExternalTargetRoot(t *testing.T) {
	paths := testPaths()
	ss := SharedSkills{
		Dir: "/repo/skills",
		Links: []SharedSkillLink{{
			Path:     "/repo/skills/implement",
			Resolved: "/repo/.agents/skills/implement",
			IsDir:    true,
		}},
	}

	_, args, err := sharedSkillsMounts(newMountRegistry("/proj"), paths, ss, false)
	if err != nil {
		t.Fatalf("sharedSkillsMounts: %v", err)
	}

	root := runtime.MountArg{Src: "/repo", Dst: "/repo", ReadOnly: true}
	if !hasMountArg(args, root) {
		t.Errorf("args do not contain shared skills target root mount %+v: %#v", root, args)
	}
	mounts := mountArgs(args)
	if len(mounts) < 7 || mounts[6] != root {
		t.Errorf("target root must follow the six tool mounts, got %#v", mounts)
	}
	for _, unexpected := range []runtime.MountArg{
		{Src: "/repo/skills", Dst: "/repo/skills", ReadOnly: true},
		{Src: "/repo/.agents/skills/implement", Dst: "/repo/.agents/skills/implement", ReadOnly: true},
	} {
		if hasMountArg(args, unexpected) {
			t.Errorf("args contain redundant covered mount %+v: %#v", unexpected, args)
		}
	}
}

func TestSharedSkillsTargetRootFallsBackWhenItWouldShadowExistingMount(t *testing.T) {
	paths := testPaths()
	reg := newMountRegistry("/repo/project")
	ss := SharedSkills{Dir: "/repo/skills", Links: []SharedSkillLink{{
		Resolved: "/repo/.agents/skills/implement", IsDir: true,
	}}}

	_, args, err := sharedSkillsMounts(reg, paths, ss, false)
	if err != nil {
		t.Fatalf("sharedSkillsMounts: %v", err)
	}
	if hasMountArg(args, runtime.MountArg{Src: "/repo", Dst: "/repo", ReadOnly: true}) {
		t.Fatal("target root must not be emitted after a nested project mount")
	}
	for _, want := range []runtime.MountArg{
		{Src: "/repo/skills", Dst: "/repo/skills", ReadOnly: true},
		{Src: "/repo/.agents/skills/implement", Dst: "/repo/.agents/skills/implement", ReadOnly: true},
	} {
		if !hasMountArg(args, want) {
			t.Errorf("fallback omitted existing leaf replay mount %+v", want)
		}
	}
}

func TestSharedSkillsTargetRootDoesNotHideExactLeafConflict(t *testing.T) {
	paths := testPaths()
	reg := newMountRegistry("/proj")
	reg.addUser("/different", "/repo/.agents/skills/implement", "rw")
	ss := SharedSkills{Dir: "/repo/skills", Links: []SharedSkillLink{{
		Resolved: "/repo/.agents/skills/implement", IsDir: true,
	}}}

	steps, args, err := sharedSkillsMounts(reg, paths, ss, false)
	var shareErr *ShareSkillsError
	if !errors.As(err, &shareErr) {
		t.Fatalf("error = %v, want *ShareSkillsError", err)
	}
	want := "error: --share-skills read-only mount conflicts with writable mount: /repo/.agents/skills/implement"
	if len(shareErr.Lines) == 0 || shareErr.Lines[0] != want {
		t.Errorf("error lines = %#v, want first %q", shareErr.Lines, want)
	}
	if got := mountArgs(args); len(got) != 7 || got[6] != (runtime.MountArg{Src: ss.Dir, Dst: ss.Dir, ReadOnly: true}) {
		t.Errorf("exact conflict mount prefix = %#v, want six tool mounts then shared source", got)
	}
	if len(steps) != 13 {
		t.Errorf("exact conflict step prefix has %d steps, want six mkdir/print pairs then source print", len(steps))
	}
}

func TestSharedSkillsTargetRootSafetyAndMissingBoundary(t *testing.T) {
	paths := testPaths()
	tests := []struct {
		name      string
		registry  *mountRegistry
		ss        SharedSkills
		wantRoot  string
		wantError bool
		wantSelf  bool
		wantLeaf  bool
	}{
		{
			name:     "sibling targets converge on one root",
			registry: newMountRegistry("/proj"),
			ss: SharedSkills{Dir: "/repo/skills", Links: []SharedSkillLink{
				{Resolved: "/repo/.agents/skills/implement", IsDir: true},
				{Resolved: "/repo/.config/skills/review", IsDir: true},
			}},
			wantRoot: "/repo",
		},
		{
			name:     "target below shared directory does not widen",
			registry: newMountRegistry("/proj"),
			ss: SharedSkills{Dir: "/repo/skills", Links: []SharedSkillLink{
				{Resolved: "/repo/skills/implement", IsDir: true},
			}},
			wantSelf: true,
		},
		{
			name:     "missing link stops later target from widening root",
			registry: newMountRegistry("/proj"),
			ss: SharedSkills{Dir: "/repo/skills", Links: []SharedSkillLink{
				{Resolved: "/repo/.agents/skills/implement", IsDir: true},
				{Path: "/repo/skills/broken", Resolved: "/repo/missing", Missing: true},
				{Resolved: "/outside/skills/ignored", IsDir: true},
			}},
			wantRoot:  "",
			wantError: true,
			wantSelf:  true,
			wantLeaf:  true,
		},
		{
			name:     "filesystem root is never mounted automatically",
			registry: newMountRegistry("/proj"),
			ss: SharedSkills{Dir: "/repo/skills", Links: []SharedSkillLink{
				{Resolved: "/other/skills/implement", IsDir: true},
			}},
			wantSelf: true,
			wantLeaf: true,
		},
		{
			name:     "writable project coverage skips root but preserves leaves",
			registry: newMountRegistry("/repo"),
			ss: SharedSkills{Dir: "/repo/skills", Links: []SharedSkillLink{
				{Resolved: "/repo/.agents/skills/implement", IsDir: true},
			}},
			wantSelf: true,
			wantLeaf: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, args, err := sharedSkillsMounts(tt.registry, paths, tt.ss, false)
			if (err != nil) != tt.wantError {
				t.Fatalf("sharedSkillsMounts error = %v, want error=%t", err, tt.wantError)
			}
			root := runtime.MountArg{Src: tt.wantRoot, Dst: tt.wantRoot, ReadOnly: true}
			if got := hasMountArg(args, root); got != (tt.wantRoot != "") {
				t.Errorf("target root mount present = %t, want %t: %#v", got, tt.wantRoot != "", args)
			}
			self := runtime.MountArg{Src: tt.ss.Dir, Dst: tt.ss.Dir, ReadOnly: true}
			if got := hasMountArg(args, self); got != tt.wantSelf {
				t.Errorf("shared source mount present = %t, want %t", got, tt.wantSelf)
			}
			leaf := runtime.MountArg{Src: "/repo/.agents/skills/implement", Dst: "/repo/.agents/skills/implement", ReadOnly: true}
			if tt.name == "filesystem root is never mounted automatically" {
				leaf = runtime.MountArg{Src: "/other/skills/implement", Dst: "/other/skills/implement", ReadOnly: true}
			}
			if got := hasMountArg(args, leaf); got != tt.wantLeaf {
				t.Errorf("external target mount present = %t, want %t", got, tt.wantLeaf)
			}
		})
	}
}

func TestSharedSkillsTargetRootFallsBackBeneathReadonlyNonParityToolMount(t *testing.T) {
	paths := testPaths()
	ss := SharedSkills{Dir: "/home/u/.agents/skills/repo/skills", Links: []SharedSkillLink{{
		Resolved: "/home/u/.agents/skills/repo/.agents/skills/implement", IsDir: true,
	}}}

	_, args, err := sharedSkillsMounts(newMountRegistry("/proj"), paths, ss, true)
	if err != nil {
		t.Fatalf("sharedSkillsMounts: %v", err)
	}
	root := runtime.MountArg{Src: "/home/u/.agents/skills/repo", Dst: "/home/u/.agents/skills/repo", ReadOnly: true}
	if hasMountArg(args, root) {
		t.Fatal("strict-runtime safety fallback must not emit a root beneath the tool's non-parity read-only destination")
	}
	for _, want := range []runtime.MountArg{
		{Src: ss.Dir, Dst: ss.Dir, ReadOnly: true},
		{Src: "/home/u/.agents/skills/repo/.agents/skills/implement", Dst: "/home/u/.agents/skills/repo/.agents/skills/implement", ReadOnly: true},
	} {
		if !hasMountArg(args, want) {
			t.Errorf("fallback omitted existing replay mount %+v", want)
		}
	}
}

func TestSharedSkillsFileTargetFallsBackToParentDirWhenNoSafeRootExists(t *testing.T) {
	paths := testPaths()
	ss := SharedSkills{Dir: "/repo/skills", Links: []SharedSkillLink{{
		Resolved: "/other/skills/guide.md", ParentDir: "/other/skills",
	}}}

	_, args, err := sharedSkillsMounts(newMountRegistry("/proj"), paths, ss, false)
	if err != nil {
		t.Fatalf("sharedSkillsMounts: %v", err)
	}
	if hasMountArg(args, runtime.MountArg{Src: "/", Dst: "/", ReadOnly: true}) {
		t.Fatal("filesystem root must never be emitted")
	}
	want := runtime.MountArg{Src: "/other/skills", Dst: "/other/skills", ReadOnly: true}
	if !hasMountArg(args, want) {
		t.Errorf("file target parent mount = %#v, want %+v", mountArgs(args), want)
	}
}

func TestSharedSkillsMissingTargetRetainsOrderedPrefix(t *testing.T) {
	paths := testPaths()
	ss := SharedSkills{Dir: "/repo/skills", Links: []SharedSkillLink{
		{Resolved: "/repo/.agents/skills/implement", IsDir: true},
		{Path: "/repo/skills/broken", Resolved: "/repo/missing", Missing: true},
	}}

	steps, args, err := sharedSkillsMounts(newMountRegistry("/proj"), paths, ss, false)
	if err == nil {
		t.Fatal("missing target must fail")
	}
	wantArgs := []runtime.MountArg{
		{Src: ss.Dir, Dst: paths.ContainerClaudeDir + "/skills", ReadOnly: true},
		{Src: ss.Dir, Dst: paths.CodexDir + "/skills", ReadOnly: true},
		{Src: ss.Dir, Dst: paths.AgentsDir + "/skills", ReadOnly: true},
		{Src: ss.Dir, Dst: paths.CopilotDir + "/skills", ReadOnly: true},
		{Src: ss.Dir, Dst: paths.GeminiDir + "/skills", ReadOnly: true},
		{Src: ss.Dir, Dst: paths.VibeDir + "/skills", ReadOnly: true},
		{Src: ss.Dir, Dst: ss.Dir, ReadOnly: true},
		{Src: "/repo/.agents/skills/implement", Dst: "/repo/.agents/skills/implement", ReadOnly: true},
	}
	if got := mountArgs(args); !sameMountArgs(got, wantArgs) {
		t.Errorf("missing target mount prefix = %#v, want %#v", got, wantArgs)
	}
	if len(steps) != 14 {
		t.Errorf("missing target step prefix has %d steps, want six mkdir/print pairs and two prints", len(steps))
	}
}

func TestSharedSkillsReadonlyCoverageDeduplicatesRootAndLeaves(t *testing.T) {
	paths := testPaths()
	reg := newMountRegistry("/proj")
	reg.addUser("/repo", "/repo", "ro")
	ss := SharedSkills{Dir: "/repo/skills", Links: []SharedSkillLink{{Resolved: "/repo/.agents/skills/implement", IsDir: true}}}

	_, args, err := sharedSkillsMounts(reg, paths, ss, false)
	if err != nil {
		t.Fatalf("sharedSkillsMounts: %v", err)
	}
	for _, absent := range []runtime.MountArg{
		{Src: "/repo", Dst: "/repo", ReadOnly: true},
		{Src: "/repo/skills", Dst: "/repo/skills", ReadOnly: true},
		{Src: "/repo/.agents/skills/implement", Dst: "/repo/.agents/skills/implement", ReadOnly: true},
	} {
		if hasMountArg(args, absent) {
			t.Errorf("covered mount unexpectedly emitted: %+v", absent)
		}
	}
}

func hasMountArg(args []runtime.Arg, want runtime.MountArg) bool {
	for _, arg := range args {
		if got, ok := arg.(runtime.MountArg); ok && got == want {
			return true
		}
	}
	return false
}

func mountArgs(args []runtime.Arg) []runtime.MountArg {
	var mounts []runtime.MountArg
	for _, arg := range args {
		if mount, ok := arg.(runtime.MountArg); ok {
			mounts = append(mounts, mount)
		}
	}
	return mounts
}

func sameMountArgs(got, want []runtime.MountArg) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
