package host

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// maxSymlinkHops bounds the symlink chase the way the kernel does, so a symlink
// loop returns a value instead of hanging.
const maxSymlinkHops = 40

// ResolvePath implements Python's os.path.realpath, which is what resolve_path
// (claude-contained:336-347) prefers and therefore what the bash launcher's
// observable behavior is built on.
//
// The critical property is *tolerance*: it resolves as far as it can and
// returns a result for a path that does not exist. The launcher applies it to
// mount paths with no prior existence check (claude-contained:1878), so a
// stricter version would change when and how invalid paths are reported.
// filepath.EvalSymlinks is exactly that stricter version -- it errors on a
// missing component -- so it is deliberately not used here.
//
// Components are walked left to right against the already-resolved prefix,
// rather than cleaned up front, because ".." has to mean "parent of what this
// resolved to" and not "textually cancel the previous name".
func ResolvePath(path string) string {
	if path == "" {
		path = "."
	}
	if !filepath.IsAbs(path) {
		if cwd, err := os.Getwd(); err == nil {
			// Concatenated, not filepath.Join: Join calls Clean, which cancels
			// ".." against the preceding name textually. That is precisely what
			// the component walk below exists to avoid -- for "link/.." where
			// link points elsewhere, ".." must mean the parent of the resolved
			// target, not the directory holding the link.
			path = cwd + string(os.PathSeparator) + path
		}
	}

	resolved := string(os.PathSeparator)
	queue := splitComponents(path)
	hops := 0

	for len(queue) > 0 {
		comp := queue[0]
		queue = queue[1:]

		switch comp {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}

		next := filepath.Join(resolved, comp)
		target, err := os.Readlink(next)
		if err != nil {
			// Either not a symlink, or the component does not exist. Both mean
			// "stop resolving this component, keep walking" -- the tolerance
			// described above.
			resolved = next
			continue
		}

		hops++
		if hops > maxSymlinkHops {
			resolved = next
			continue
		}

		// A relative target resolves against the symlink's parent, which is
		// exactly the current `resolved`, so only an absolute target restarts.
		if filepath.IsAbs(target) {
			resolved = string(os.PathSeparator)
		}
		queue = append(splitComponents(target), queue...)
	}

	return resolved
}

func splitComponents(path string) []string {
	parts := strings.Split(path, string(os.PathSeparator))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// PathHash8 mirrors path_hash_8 (claude-contained:357-367): the first 8 hex
// characters of the SHA-256 of the path. It feeds default Zellij session names,
// and the differential harness recomputes it to neutralize the value, so it has
// to agree exactly.
func PathHash8(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:8]
}

// DirIsEmpty mirrors dir_is_empty (claude-contained:483-489), including its
// treatment of dotfiles: the bash globs cover "*", ".[!.]*" and "..?*", so a
// directory containing only "." and ".." is empty and anything else is not.
func DirIsEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(entries) == 0
}
