package plan

import (
	"errors"
	"testing"

	"claude-contained/internal/runtime"
)

func TestMountRegistrySeedsProjectDir(t *testing.T) {
	r := newMountRegistry("/proj")
	if len(r.user) != 1 || r.user[0] != (mountRecord{src: "/proj", dst: "/proj", mode: "rw"}) {
		t.Fatalf("seed = %+v, want a single rw record for the project dir", r.user)
	}
}

// A read-only user mount at the same dst with the same src is already exactly
// what --share-skills would add: silent no-op, not an error.
func TestAddSharedNoopWhenUserMountAlreadyMatches(t *testing.T) {
	r := newMountRegistry("/proj")
	r.addUser("/share", "/share", "ro")

	mount, line, err := r.addShared("/share", "/share", false, "shared skills source")
	if err != nil || mount != nil || line != "" {
		t.Fatalf("addShared = (%v, %q, %v), want (nil, \"\", nil)", mount, line, err)
	}
}

// A writable user mount at the same dst is a hard conflict, with the
// two-line message and the "remove the duplicate" hint.
func TestAddSharedConflictsWithWritableUserMount(t *testing.T) {
	r := newMountRegistry("/proj")
	r.addUser("/share", "/share", "rw")

	_, _, err := r.addShared("/share", "/share", false, "shared skills source")
	var confErr *ShareSkillsError
	if !errors.As(err, &confErr) {
		t.Fatalf("addShared err = %v, want *ShareSkillsError", err)
	}
	want := []string{
		"error: --share-skills read-only mount conflicts with writable mount: /share",
		"       remove the duplicate -m or mark it :ro",
	}
	if len(confErr.Lines) != len(want) || confErr.Lines[0] != want[0] || confErr.Lines[1] != want[1] {
		t.Errorf("Lines = %#v, want %#v", confErr.Lines, want)
	}
}

// A read-only user mount at the same dst but a different src is still a
// conflict -- just a different message, and no hint line.
func TestAddSharedConflictsWithDifferentReadonlyUserMount(t *testing.T) {
	r := newMountRegistry("/proj")
	r.addUser("/other", "/share", "ro")

	_, _, err := r.addShared("/share", "/share", false, "shared skills source")
	var confErr *ShareSkillsError
	if !errors.As(err, &confErr) {
		t.Fatalf("addShared err = %v, want *ShareSkillsError", err)
	}
	want := "error: --share-skills read-only mount conflicts with a different mount at: /share"
	if len(confErr.Lines) != 1 || confErr.Lines[0] != want {
		t.Errorf("Lines = %#v, want [%q]", confErr.Lines, want)
	}
}

// Two calls with the same src/dst -- e.g. two tools' skills dirs both backed
// by the same --share-skills dir -- succeed once and then no-op, never
// double-mounting or erroring on the repeat.
func TestAddSharedDedupsIdenticalRepeat(t *testing.T) {
	r := newMountRegistry("/proj")

	mount1, line1, err := r.addShared("/share", "/dst", false, "skills")
	if err != nil {
		t.Fatalf("first addShared: %v", err)
	}
	wantMount := &runtime.MountArg{Src: "/share", Dst: "/dst", ReadOnly: true}
	if *mount1 != *wantMount {
		t.Errorf("mount1 = %+v, want %+v", mount1, wantMount)
	}
	wantLine := "Sharing skills: /share -> /dst (read-only)"
	if line1 != wantLine {
		t.Errorf("line1 = %q, want %q", line1, wantLine)
	}

	mount2, line2, err := r.addShared("/share", "/dst", false, "skills")
	if err != nil || mount2 != nil || line2 != "" {
		t.Fatalf("repeat addShared = (%v, %q, %v), want (nil, \"\", nil)", mount2, line2, err)
	}
}

// A different src at an already-registered shared dst is a conflict distinct
// from the user-mount conflicts above.
func TestAddSharedConflictsWithDifferentSharedMount(t *testing.T) {
	r := newMountRegistry("/proj")
	if _, _, err := r.addShared("/share-a", "/dst", false, "skills"); err != nil {
		t.Fatalf("first addShared: %v", err)
	}

	_, _, err := r.addShared("/share-b", "/dst", false, "skills")
	var confErr *ShareSkillsError
	if !errors.As(err, &confErr) {
		t.Fatalf("addShared err = %v, want *ShareSkillsError", err)
	}
	want := "error: --share-skills read-only mount conflicts with another shared-skills mount at: /dst"
	if len(confErr.Lines) != 1 || confErr.Lines[0] != want {
		t.Errorf("Lines = %#v, want [%q]", confErr.Lines, want)
	}
}

// allowCover lets a dst already inside an existing read-only user mount's
// tree skip silently -- this is how the shared-dir self-mount and symlink
// targets avoid re-mounting something already exposed read-only.
func TestAddSharedCoveredByReadonlyUserMountAncestor(t *testing.T) {
	r := newMountRegistry("/proj")
	r.addUser("/share", "/share", "ro")

	mount, line, err := r.addShared("/share/nested", "/share/nested", true, "symlinked skills target")
	if err != nil || mount != nil || line != "" {
		t.Fatalf("addShared = (%v, %q, %v), want (nil, \"\", nil)", mount, line, err)
	}
}

// The same covering relationship also applies against previously registered
// shared mounts, not just user mounts.
func TestAddSharedCoveredByReadonlySharedMountAncestor(t *testing.T) {
	r := newMountRegistry("/proj")
	if _, _, err := r.addShared("/share", "/share", true, "shared skills source"); err != nil {
		t.Fatalf("seeding addShared: %v", err)
	}

	mount, line, err := r.addShared("/share/nested", "/share/nested", true, "symlinked skills target")
	if err != nil || mount != nil || line != "" {
		t.Fatalf("addShared = (%v, %q, %v), want (nil, \"\", nil)", mount, line, err)
	}
}

// The exact same ancestor relationship, but with allowCover false, must NOT
// be treated as covered -- the flag actually gates the behavior rather than
// being redundant with the dst check.
func TestAddSharedNotCoveredWhenCoverNotAllowed(t *testing.T) {
	r := newMountRegistry("/proj")
	r.addUser("/share", "/share", "ro")

	mount, _, err := r.addShared("/share/nested", "/share/nested", false, "skills")
	if err != nil {
		t.Fatalf("addShared: %v", err)
	}
	if mount == nil {
		t.Fatal("addShared returned nil mount, want a mount emitted since allowCover was false")
	}
}

func TestMountRegistryPathParityCoverage(t *testing.T) {
	tests := []struct {
		name    string
		project string
		path    string
		seed    func(*mountRegistry)
		want    bool
	}{
		{name: "equal writable parity user mount", project: "/share", path: "/share", want: true},
		{name: "descendant writable parity user mount", project: "/share", path: "/share/nested", want: true},
		{name: "unrelated sibling", project: "/share", path: "/other", want: false},
		{
			name:    "non-parity user mount does not cover host path",
			project: "/project",
			path:    "/share/nested",
			seed:    func(r *mountRegistry) { r.addUser("/other", "/share", "ro") },
			want:    false,
		},
		{
			name:    "readonly shared mount",
			project: "/project",
			path:    "/share/nested",
			seed:    func(r *mountRegistry) { _, _, _ = r.addShared("/share", "/share", true, "shared skills source") },
			want:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newMountRegistry(tt.project)
			if tt.seed != nil {
				tt.seed(r)
			}
			if got := r.pathParityCovers(tt.path); got != tt.want {
				t.Errorf("pathParityCovers(%q) = %t, want %t", tt.path, got, tt.want)
			}
		})
	}
}

func TestReadonlyCoversRequiresReadonlyPathParity(t *testing.T) {
	tests := []struct {
		name   string
		record mountRecord
		want   bool
	}{
		{name: "readonly parity", record: mountRecord{src: "/share", dst: "/share", mode: "ro"}, want: true},
		{name: "writable parity", record: mountRecord{src: "/share", dst: "/share", mode: "rw"}},
		{name: "readonly non-parity", record: mountRecord{src: "/other", dst: "/share", mode: "ro"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := readonlyCovers([]mountRecord{tt.record}, "/share/nested"); got != tt.want {
				t.Errorf("readonlyCovers() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestMountRegistryWouldShadowExisting(t *testing.T) {
	r := newMountRegistry("/project")
	r.addUser("/source", "/mount/nested", "ro")
	if !r.wouldShadowExisting("/mount") {
		t.Fatal("parent mount must be rejected when it would shadow a nested destination")
	}
	if !r.wouldShadowExisting("/mount/nested") {
		t.Fatal("equal destination must be rejected when it would shadow an existing mount")
	}
	if r.wouldShadowExisting("/unrelated") {
		t.Fatal("unrelated mount must not be treated as shadowing")
	}
}

func TestMountRegistryDetectsReadonlyNonParityAncestor(t *testing.T) {
	r := newMountRegistry("/project")
	r.addUser("/elsewhere/skills", "/home/u/.agents/skills", "ro")
	if !r.isNestedUnderReadonlyNonParityDestination("/home/u/.agents/skills/repo") {
		t.Fatal("nested target root must be rejected beneath a read-only non-parity destination")
	}
	if r.isNestedUnderReadonlyNonParityDestination("/home/u/.agents/other") {
		t.Fatal("sibling must not be treated as nested beneath the non-parity destination")
	}
	r.addUser("/home/u/.agents", "/home/u/.agents", "ro")
	if r.isNestedUnderReadonlyNonParityDestination("/home/u/.agents/other") {
		t.Fatal("path-parity mounts must not trigger the non-parity safety fallback")
	}
}
