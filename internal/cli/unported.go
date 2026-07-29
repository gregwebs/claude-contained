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
		_, _ = fmt.Fprintln(stderr, "error: Zellij session name cannot be empty")
		return exitWith(ExitUsage)
	}
	if name[0] == '-' || !zellijSessionNamePattern.MatchString(name) {
		_, _ = fmt.Fprintf(stderr, "error: invalid Zellij session name: %s\n", name)
		_, _ = fmt.Fprintln(stderr, "       Use only letters, numbers, '_', '.', and '-'; do not start with '-'.")
		return exitWith(ExitUsage)
	}
	return nil
}

// CheckUnportedEarly refuses the paths bash takes *before* it resolves the
// project directory: `-R/--rebuild` rebuilds and exits at claude-contained:890.
// This guard never reads the project env file, so refusing cannot hide an
// error bash would have reported.
//
// This guard is keyed to a flag. Paths reachable with *no* flag need their own
// guards at the point they would be taken -- see the worktree auto-locking
// check in the driver -- because a flag-keyed check would let them diverge
// silently instead of failing loudly.
func CheckUnportedEarly(cfg Config, stderr io.Writer) error {
	if cfg.RebuildMode != "none" {
		return unported(stderr, "-R/--rebuild")
	}
	return nil
}

func unported(stderr io.Writer, what string) error {
	_, _ = fmt.Fprintf(stderr, unportedIntro, what)
	return exitWith(ExitUnported)
}
