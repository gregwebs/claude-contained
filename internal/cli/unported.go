package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// It runs *after* the full bash validation, so a command line that bash rejects
// with exit 2 still exits 2 rather than being masked by exit 3, and *before*
// any host mutation, so nothing is left half-applied.
//
// Two of these guards are not keyed to a flag, which is the point: those paths
// are reached with no flag at all, so a flag-keyed check would let them diverge
// silently instead of failing loudly.
func CheckUnported(cfg Config, projectDir string, stderr io.Writer) error {
	unported := func(what string) error {
		fmt.Fprintf(stderr, unportedIntro, what)
		return exitWith(ExitUnported)
	}

	switch {
	case cfg.AttachMode:
		return unported("-a/--attach")
	case cfg.ZellijMode:
		return unported("--zellij")
	case cfg.RebuildMode != "none":
		return unported("-R/--rebuild")
	case cfg.ShareSkillsDir != "":
		return unported("--share-skills")
	case len(cfg.EnvFlagArgs) > 0:
		return unported("-e/--env")
	case cfg.LockWorktrees:
		return unported("-W/--lock-worktrees")
	}

	// Reached with no flag: the project env file is loaded unless
	// --no-project-env was given, so its mere presence is an unported path.
	if !cfg.NoProjectEnv {
		envFile := filepath.Join(projectDir, ".claude-contained", "env")
		if info, err := os.Stat(envFile); err == nil && !info.IsDir() {
			return unported("the project env file")
		}
	}

	return nil
}
