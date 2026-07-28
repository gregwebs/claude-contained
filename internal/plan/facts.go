package plan

import "claude-contained/internal/env"

// AccountStateFacts mirrors, one predicate per field, the exact stat tests the
// bash migration performs (claude-contained:1827-1841).
//
// They are kept as separate predicates rather than collapsed into one enum
// because bash deliberately mixes following and non-following tests, and on
// both files: `-f` and `-e` follow symlinks while `-L` does not, and the two
// checks on the shared file are not the same test (`-e` in the repair branch,
// `-f` in the link branch). Collapsing any pair of them is how a careless port
// either renames a symlink over its own target or deletes a healthy one --
// either way taking the user's credentials with it.
type AccountStateFacts struct {
	// Exists is `-e ~/.claude.json`: follows symlinks, so a dangling symlink
	// reports false even though something is there.
	Exists bool
	// IsRegularFile is `-f ~/.claude.json`: also follows symlinks.
	IsRegularFile bool
	// IsSymlink is `-L ~/.claude.json`: does not follow.
	IsSymlink bool
	// SharedExists is `-e ~/.claude-contained/.claude.json`, of any file type.
	SharedExists bool
	// SharedIsRegularFile is `-f` on the same path. Distinct from SharedExists:
	// a directory there satisfies one and not the other.
	SharedIsRegularFile bool
}

// Facts is everything about the filesystem that plan building needs but is not
// allowed to look up itself. Probing happens once, in the impure layer; Build
// is then a pure function of Config, host.State, Facts, Profile and Answers.
type Facts struct {
	// ProjectDir is the resolved project directory.
	ProjectDir string
	// ExtraMounts are the resolved extra mount paths, parallel to ExtraModes.
	ExtraMounts []string
	// ExtraModes is "rw" or "ro" per extra mount.
	ExtraModes []string

	// WorktreeMainRepo is the main repository root when the project directory
	// is a linked worktree, else empty.
	WorktreeMainRepo string

	// GitConfigExists reports whether the host has a ~/.gitconfig to copy.
	GitConfigExists bool
	// AccountState is the stat picture of the Claude account state file.
	AccountState AccountStateFacts

	// NodeOverlayDirs are the mount roots that look like Node projects, in the
	// order bash checks them. Empty on a Linux host, where the overlay is
	// unnecessary.
	NodeOverlayDirs []string
	// NodeOverlayTargetEmpty reports, per NodeOverlayDirs entry, whether the
	// overlay directory will be empty once created. Captured before any mkdir,
	// because creating it changes the answer.
	NodeOverlayTargetEmpty map[string]bool

	// RunningContainers are the names the runtime currently reports, used to
	// deduplicate the generated container name.
	RunningContainers []string

	// Env is the finished tool-process environment, in emission order, with
	// every key already deduplicated and validated. Precedence between the
	// command line, the project env file and the launcher's built-ins is
	// settled before planning, so Build only has to emit this list.
	Env []env.Pair
}
