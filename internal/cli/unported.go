package cli

import (
	"fmt"
	"io"
	"regexp"
)

var zellijSessionNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ValidateZellijSessionName mirrors validate_zellij_session_name
// (claude-contained:375-388).
func ValidateZellijSessionName(name string, stderr io.Writer) error {
	if name == "" {
		fmt.Fprintln(stderr, "error: Zellij session name cannot be empty")
		return exitWith(ExitUsage)
	}
	if name[0] == '-' || !zellijSessionNamePattern.MatchString(name) {
		fmt.Fprintf(stderr, "error: invalid Zellij session name: %s\n", name)
		fmt.Fprintln(stderr, "       Use only letters, numbers, '_', '.', and '-'; do not start with '-'.")
		return exitWith(ExitUsage)
	}
	return nil
}

// CheckUnported refuses the code paths this ticket has not ported yet.
//
// The guards are split into two phases because *where* bash leaves the script
// decides which of its own errors can still fire first. Refusing too early
// would mask an exit 2 that bash would have produced; refusing too late would
// let an unported path run.
//
// Every guard here is keyed to a flag. Paths reachable with *no* flag need
// their own guards at the point they would be taken -- see the worktree
// auto-locking check in the driver -- because a flag-keyed check would let them
// diverge silently instead of failing loudly.

// CheckUnportedEarly refuses the paths bash takes *before* it resolves the
// project directory: `-R/--rebuild` rebuilds and exits at claude-contained:890,
// and the attach paths exec at :945-:1010. Neither ever reads the project env
// file, so refusing here cannot hide an error bash would have reported.
func CheckUnportedEarly(cfg Config, stderr io.Writer) error {
	switch {
	case cfg.AttachMode:
		return unported(stderr, "-a/--attach")
	case cfg.RebuildMode != "none":
		return unported(stderr, "-R/--rebuild")
	}
	return nil
}

// CheckUnportedLate refuses the paths bash reaches only *after* the project env
// file has had its say (claude-contained:1416). `--zellij` is handled at :1423
// and worktree locking at :1563 — both downstream — so a project env file that
// bash rejects with exit 2 must still do so here.
func CheckUnportedLate(cfg Config, stderr io.Writer) error {
	switch {
	case cfg.ZellijMode:
		return unported(stderr, "--zellij")
	case cfg.LockWorktrees:
		return unported(stderr, "-W/--lock-worktrees")
	}
	return nil
}

func unported(stderr io.Writer, what string) error {
	fmt.Fprintf(stderr, unportedIntro, what)
	return exitWith(ExitUnported)
}
