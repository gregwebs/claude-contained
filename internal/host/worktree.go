package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AutoLockPrefix is the owner token the launcher writes into lock reasons it
// created itself, so a user's own lock is never disturbed.
const AutoLockPrefix = "cc-autolocked-by:"

// PathIsAtOrUnder mirrors path_is_at_or_under (claude-contained:1077-1082).
func PathIsAtOrUnder(path, parent string) bool {
	path = strings.TrimSuffix(path, "/")
	parent = strings.TrimSuffix(parent, "/")
	return path == parent || strings.HasPrefix(path, parent+"/")
}

// PathVisibleInContainer reports whether a host path falls inside one of the
// mounted roots, and is therefore reachable from inside the container.
func PathVisibleInContainer(path string, mountedRoots []string) bool {
	for _, root := range mountedRoots {
		if PathIsAtOrUnder(path, root) {
			return true
		}
	}
	return false
}

// HiddenWorktrees reports the linked worktrees of repoRoot that the container
// will not be able to see.
//
// This is the prune hazard the launcher protects against: git inside the
// container sees the repository metadata but not those working trees, so a
// prune would consider them gone. A worktree already locked by the user (with
// a reason that is not one of ours) is left out, because it is already
// protected and we must not touch someone else's lock.
func HiddenWorktrees(repoRoot string, mountedRoots []string) []string {
	if repoRoot == "" {
		return nil
	}
	// If the repository metadata itself is not visible, the container cannot
	// prune anything and there is nothing to protect.
	if !PathVisibleInContainer(filepath.Join(repoRoot, ".git"), mountedRoots) {
		return nil
	}

	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil
	}

	var hidden []string
	var path, lockReason string
	var locked, prunable bool

	flush := func() {
		defer func() { path, lockReason, locked, prunable = "", "", false, false }()

		if path == "" || path == repoRoot || prunable {
			return
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			return
		}
		if PathVisibleInContainer(path, mountedRoots) {
			return
		}
		if locked && !strings.HasPrefix(lockReason, AutoLockPrefix) {
			return
		}
		hidden = append(hidden, path)
	}

	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "locked"):
			locked = true
			lockReason = strings.TrimPrefix(strings.TrimPrefix(line, "locked"), " ")
		case strings.HasPrefix(line, "prunable"):
			prunable = true
		}
	}
	flush()

	return hidden
}
