package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// placeholderNames is the fixed list from cleanup_srt_placeholder_files
// (claude-contained:400-423). On Linux, srt protects these by mounting
// /dev/null over them; bubblewrap can leave zero-byte host placeholders behind
// when a run is interrupted.
var placeholderNames = []string{
	".gitconfig",
	".gitmodules",
	".bashrc",
	".bash_profile",
	".zshrc",
	".zprofile",
	".profile",
	".ripgreprc",
	".mcp.json",
}

// CleanupPlaceholderFiles removes leftover srt placeholders from each root.
//
// This is the only mutation that deletes files inside the user's project
// directories, so all four of bash's guards are load-bearing and none may be
// relaxed: the entry must be a regular file, zero bytes, not a symlink, and not
// tracked by git. A tracked or non-empty file of the same name is never
// touched. Errors are swallowed exactly as `rm -f ... || true` does.
func CleanupPlaceholderFiles(roots ...string) {
	for _, root := range roots {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			continue
		}
		for _, name := range placeholderNames {
			file := filepath.Join(root, name)

			// Lstat, not Stat: a symlink to an empty file must survive.
			info, err := os.Lstat(file)
			if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
				continue
			}
			if PlaceholderIsTracked(file) {
				continue
			}
			_ = os.Remove(file)
		}
	}
}

// PlaceholderIsTracked mirrors srt_placeholder_file_is_tracked
// (claude-contained:390-398). A file outside any repository, or one whose path
// does not sit under the repository root, counts as untracked.
func PlaceholderIsTracked(file string) bool {
	dir := filepath.Dir(file)
	root, err := gitIn(dir, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return false
	}

	rel := strings.TrimPrefix(file, root+string(os.PathSeparator))
	if rel == file {
		return false
	}

	cmd := exec.Command("git", "-C", root, "ls-files", "--error-unmatch", "--", rel)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}
