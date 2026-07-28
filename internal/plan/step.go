package plan

// Step is one ordered side effect on the host. Steps are inert value types --
// they carry paths and text, never behavior -- so a test can assert an entire
// plan by comparing values, with no process started and no filesystem touched.
//
// Output is modeled as a Step rather than done inline because it is ordered
// against the mutations and is compared byte for byte by the differential
// harness. A plan that got the mutations right but printed at the wrong moment
// would still be a divergence.
type Step interface{ isStep() }

type (
	// MkdirAll creates a directory and its parents.
	//
	// It deliberately carries no mode: `mkdir -p` uses 0777 masked by the
	// process umask, and the harness records file modes in its manifest, so
	// hardcoding 0755 would diverge for anyone running a umask other than 022.
	MkdirAll struct{ Path string }

	// CopyFile copies Src over Dst, preserving nothing but the contents --
	// matching `cp` for the one file the launcher copies.
	CopyFile struct{ Src, Dst string }

	// MoveFile renames Src to Dst.
	MoveFile struct{ Src, Dst string }

	// Symlink creates Link pointing at Target.
	Symlink struct{ Target, Link string }

	// RemoveFile deletes Path, ignoring a missing file.
	RemoveFile struct{ Path string }

	// Print writes a line of user-facing output.
	Print struct {
		Text   string
		Stderr bool
	}

	// WorktreeAutoLock locks the launcher's own auto-locks for the run. It is
	// the one step whose output is not fully determined by the plan: the
	// "Auto-locked N" line counts successes, which is an I/O result, so the
	// applier emits it and returns the list the driver must release. The step
	// itself stays an inert value -- Repo, Worktrees and Owner are exactly
	// what the applier needs and nothing it has to derive.
	WorktreeAutoLock struct {
		Repo      string
		Worktrees []string
		Owner     string // the deduplicated container name
	}
)

func (MkdirAll) isStep()         {}
func (CopyFile) isStep()         {}
func (MoveFile) isStep()         {}
func (Symlink) isStep()          {}
func (RemoveFile) isStep()       {}
func (Print) isStep()            {}
func (WorktreeAutoLock) isStep() {}

// PromptID identifies a question. IDs are stable so an answer recorded on one
// Build call is still recognized on the next.
type PromptID string

const (
	// PromptWorktreeGit asks whether to mount a linked worktree's main
	// repository .git. Declining removes a mount *and* changes the value that
	// drives the sandbox's writable-path policy.
	PromptWorktreeGit PromptID = "worktree-git"
	// PromptNodeModules asks whether to use a container-specific node_modules.
	PromptNodeModules PromptID = "node-modules"
	// PromptWorktreeLocks is the auto-lock offer for linked worktrees the
	// container cannot see: "N linked worktree(s) ... hidden ... (prune
	// risk)." followed by "Auto-lock them while this container runs? [Y/n] ".
	PromptWorktreeLocks PromptID = "worktree-locks"
)

// Prompt is a question that must be answered before planning can continue.
type Prompt struct {
	ID   PromptID
	Text string
	// Default is the answer an empty line means. Every prompt in the launcher
	// defaults to yes.
	Default bool
}

// Answers maps prompts to the answers already given. A prompt that is absent
// has not been asked yet.
type Answers map[PromptID]bool
