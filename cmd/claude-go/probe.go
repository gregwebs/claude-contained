package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"claude-contained/internal/cli"
	"claude-contained/internal/host"
	"claude-contained/internal/plan"
	rt "claude-contained/internal/runtime"
)

// probeFacts collects every filesystem and runtime observation plan.Build needs
// but is not allowed to make itself. Doing it in one place, up front, is what
// keeps Build pure and therefore safely replayable across prompt rounds.
func probeFacts(
	ctx context.Context, r rt.Runtime, h host.State, cfg cli.Config,
	mainHost string, extraMounts, extraModes []string,
) (plan.Facts, error) {
	facts := plan.Facts{
		ProjectDir:             mainHost,
		ExtraMounts:            extraMounts,
		ExtraModes:             extraModes,
		WorktreeMainRepo:       host.WorktreeMainRepo(mainHost),
		NodeOverlayTargetEmpty: map[string]bool{},
	}

	home := h.Home
	if _, err := os.Stat(filepath.Join(home, ".gitconfig")); err == nil {
		facts.GitConfigExists = true
	}

	// Each predicate is captured with the stat call bash's corresponding test
	// uses: Lstat for `-L` (does not follow), Stat for `-e` and `-f` (do). Using
	// one call for all of them is how the migration destroys credentials.
	claudeJSON := filepath.Join(home, ".claude.json")
	shared := filepath.Join(home, ".claude-contained", ".claude.json")

	if info, err := os.Lstat(claudeJSON); err == nil {
		facts.AccountState.IsSymlink = info.Mode()&os.ModeSymlink != 0
	}
	if info, err := os.Stat(claudeJSON); err == nil {
		facts.AccountState.Exists = true
		facts.AccountState.IsRegularFile = info.Mode().IsRegular()
	}
	if info, err := os.Stat(shared); err == nil {
		facts.AccountState.SharedExists = true
		facts.AccountState.SharedIsRegularFile = info.Mode().IsRegular()
	}

	// The overlay exists because macOS-native binaries do not run on Linux, so
	// it is pointless when the host is already Linux.
	if runtime.GOOS != "linux" {
		platform := "linux-" + h.Arch
		candidates := []string{mainHost}
		for i, dir := range extraMounts {
			// Read-only extras are skipped: the overlay has to write a
			// .claude-contained directory inside the mount.
			if extraModes[i] == "ro" {
				continue
			}
			candidates = append(candidates, dir)
		}
		for _, dir := range candidates {
			if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
				continue
			}
			facts.NodeOverlayDirs = append(facts.NodeOverlayDirs, dir)
			target := filepath.Join(dir, ".claude-contained", "node_modules-"+platform)
			facts.NodeOverlayTargetEmpty[dir] = dirWillBeEmpty(target)
		}
	}

	names, err := r.List(ctx)
	if err != nil {
		return facts, err
	}
	facts.RunningContainers = names

	return facts, nil
}

// dirWillBeEmpty reports whether the overlay directory is empty, evaluated
// before it is created -- creating it is what changes the answer.
func dirWillBeEmpty(path string) bool {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return true
	}
	return host.DirIsEmpty(path)
}
