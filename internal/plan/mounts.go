package plan

import (
	"strings"

	"claude-contained/internal/host"
	"claude-contained/internal/runtime"
)

// mountRecord is one entry in the observable mount bookkeeping the launcher
// keeps solely to resolve --share-skills conflicts. It has nothing to do with
// the arg list Build emits elsewhere -- this is bookkeeping only, mirroring
// bash's parallel arrays rather than the argv itself.
type mountRecord struct {
	src, dst, mode string // mode is "rw" or "ro"; every entry in shared is "ro"
}

// mountRegistry mirrors user_mount_{srcs,dsts,modes} and
// shared_skill_readonly_mount_{srcs,dsts} (claude-contained:1580-1586,
// :1612-1657): every mount recorded so far, in the order it was added, so a
// later --share-skills mount can be checked against everything that came
// before it exactly the way bash checks it.
type mountRegistry struct {
	user   []mountRecord
	shared []mountRecord
}

// newMountRegistry seeds the registry with the project directory, matching
// claude-contained:1581-1583, which seeds user_mount_* before any -m mount is
// considered.
func newMountRegistry(projectDir string) *mountRegistry {
	return &mountRegistry{user: []mountRecord{{src: projectDir, dst: projectDir, mode: "rw"}}}
}

// addUser records one -m mount. Called once per entry, in -m order, mirroring
// claude-contained:1882-1884.
func (r *mountRegistry) addUser(src, dst, mode string) {
	r.user = append(r.user, mountRecord{src: src, dst: dst, mode: mode})
}

// ShareSkillsError reports a --share-skills mount that conflicts with
// an existing one, mirroring the three error branches of
// add_shared_skill_readonly_mount (claude-contained:1666-1685). Lines is the
// exact stderr output, one line per bash `echo`.
type ShareSkillsError struct{ Lines []string }

func (e *ShareSkillsError) Error() string { return strings.Join(e.Lines, "\n") }

// addShared mirrors add_shared_skill_readonly_mount (claude-contained:1659-1696).
//
// It returns the mount to emit, or a nil mount with a nil error when bash
// would have silently skipped it: an identical mount already registered at
// this destination, or -- only when allowCover is set -- an existing
// read-only mount that already covers dst by prefix. label appears only in
// the "Sharing ..." line, which is returned rather than printed so the caller
// can order it against other Steps.
func (r *mountRegistry) addShared(src, dst string, allowCover bool, label string) (*runtime.MountArg, string, error) {
	if rec, ok := indexForDst(r.user, dst); ok {
		switch {
		case rec.mode == "ro" && rec.src == src:
			return nil, "", nil
		case rec.mode == "rw":
			return nil, "", &ShareSkillsError{Lines: []string{
				"error: --share-skills read-only mount conflicts with writable mount: " + dst,
				"       remove the duplicate -m or mark it :ro",
			}}
		default: // ro, but a different source
			return nil, "", &ShareSkillsError{Lines: []string{
				"error: --share-skills read-only mount conflicts with a different mount at: " + dst,
			}}
		}
	}

	if rec, ok := indexForDst(r.shared, dst); ok {
		if rec.src == src {
			return nil, "", nil
		}
		return nil, "", &ShareSkillsError{Lines: []string{
			"error: --share-skills read-only mount conflicts with another shared-skills mount at: " + dst,
		}}
	}

	if allowCover && (readonlyCovers(r.user, dst) || readonlyCovers(r.shared, dst)) {
		return nil, "", nil
	}

	r.shared = append(r.shared, mountRecord{src: src, dst: dst, mode: "ro"})
	return &runtime.MountArg{Src: src, Dst: dst, ReadOnly: true},
		"Sharing " + label + ": " + src + " -> " + dst + " (read-only)", nil
}

func indexForDst(records []mountRecord, dst string) (mountRecord, bool) {
	for _, rec := range records {
		if rec.dst == dst {
			return rec, true
		}
	}
	return mountRecord{}, false
}

// readonlyCovers reports whether some read-only record's dst is dst itself or
// an ancestor of it, mirroring user_readonly_mount_covers_path and
// shared_skill_readonly_mount_covers_path (claude-contained:1625-1634,
// :1649-1657), both built on path_is_equal_or_child -- host.PathIsAtOrUnder is
// that same primitive, reused rather than reimplemented.
func readonlyCovers(records []mountRecord, dst string) bool {
	for _, rec := range records {
		if rec.mode == "ro" && host.PathIsAtOrUnder(dst, rec.dst) {
			return true
		}
	}
	return false
}
