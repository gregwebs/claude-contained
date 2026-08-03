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
	mainHost string, extraMounts, extraModes []string, shareSkillsDir string, mountedRoots []string,
) (plan.Facts, error) {
	worktreeMainRepo := host.WorktreeMainRepo(mainHost)

	facts := plan.Facts{
		ProjectDir:             mainHost,
		ExtraMounts:            extraMounts,
		ExtraModes:             extraModes,
		WorktreeMainRepo:       worktreeMainRepo,
		NodeOverlayTargetEmpty: map[string]bool{},
		SharedSkills:           scanSharedSkills(h.Home, shareSkillsDir),
		WorktreeLocks:          worktreeLockCandidates(mainHost, "", mountedRoots),
	}
	// The two candidate sets are identical by construction when the project
	// directory is not itself a linked worktree -- copy rather than reprobe.
	if worktreeMainRepo == "" {
		facts.WorktreeLocksWithGitMount = facts.WorktreeLocks
	} else {
		facts.WorktreeLocksWithGitMount = worktreeLockCandidates(mainHost, worktreeMainRepo, mountedRoots)
	}

	home := h.Home
	// bash's `-f`: exists, follows symlinks, and is a regular file. A bare
	// os.Stat check is only `-e`, and a directory (or fifo) at this path would
	// then reach copyFile, whose ReadFile fails outright (or blocks forever)
	// where bash simply skips the copy. Same test as completeEnv's below.
	if info, err := os.Stat(filepath.Join(home, ".gitconfig")); err == nil && info.Mode().IsRegular() {
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
			// `-f` again, not `-e`: a directory named package.json is not a
			// Node project, and treating it as one creates an overlay directory
			// and prints a notice bash never prints.
			if info, err := os.Stat(filepath.Join(dir, "package.json")); err != nil || !info.Mode().IsRegular() {
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

// worktreeLockCandidates mirrors the worktree_lock_repo fallback
// (claude-contained:1547-1550) and maybe_offer_worktree_locks' hidden-worktree
// scan (:1313-1357) for one value of worktree_main_repo.
//
// worktreeRepo is the PromptWorktreeGit answer's effect: "" when the .git
// mount is not (or not yet) in play, in which case the repository falls back
// to host.MainWorktreeRepoRoot. When non-empty, the main repository's .git is
// added to the mounted-root set before the hidden-worktree scan runs, since
// that mount is what makes those worktrees visible to an in-container prune.
func worktreeLockCandidates(mainHost, worktreeRepo string, mountedRoots []string) plan.WorktreeLockCandidates {
	repo := worktreeRepo
	if repo == "" {
		repo = host.MainWorktreeRepoRoot(mainHost)
	}
	if repo == "" {
		return plan.WorktreeLockCandidates{}
	}

	roots := mountedRoots
	if worktreeRepo != "" {
		roots = append(append([]string{}, mountedRoots...), filepath.Join(worktreeRepo, ".git"))
	}

	return plan.WorktreeLockCandidates{
		Repo:   repo,
		Hidden: host.HiddenWorktrees(repo, roots),
	}
}

// dirWillBeEmpty reports whether the overlay directory is empty, evaluated
// before it is created -- creating it is what changes the answer.
func dirWillBeEmpty(path string) bool {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return true
	}
	return host.DirIsEmpty(path)
}

// scanSharedSkills builds the --share-skills facts, replicating
// add_shared_skills_mounts' traversal (claude-contained:1745-1760) so
// plan.Build can replay it without touching the filesystem.
//
// dir is the already-resolved --share-skills directory; empty means the flag
// was not given.
func scanSharedSkills(home, dir string) plan.SharedSkills {
	ss := plan.SharedSkills{Dir: dir}
	if dir == "" {
		return ss
	}

	systemDir := filepath.Join(home, ".codex", "skills", ".system")
	if info, err := os.Stat(systemDir); err == nil && info.IsDir() {
		ss.CodexSystemDir = true
	}

	// A `.system` mountpoint inside the shared dir is what lets the Codex
	// system-skills remount land on a runtime that cannot create it under the
	// read-only shared mount -- see plan.SharedSkills.DirHasSystem.
	if info, err := os.Stat(filepath.Join(dir, ".system")); err == nil && info.IsDir() {
		ss.DirHasSystem = true
	}

	// One seen-set shared across both scans, matching bash's single global
	// shared_skill_seen_scan_dirs array.
	seen := map[string]bool{}

	if ss.CodexSystemDir {
		links := scanSkillSymlinkTree(systemDir, seen)
		ss.Links = append(ss.Links, links...)
		// Bash's `exit 2` on a missing target ends the whole script, so the
		// second scan (of dir) never runs at all -- match that rather than
		// gathering Links plan.Build could never reach anyway.
		if anyMissing(links) {
			return ss
		}
	}
	ss.Links = append(ss.Links, scanSkillSymlinkTree(dir, seen)...)
	return ss
}

func anyMissing(links []plan.SharedSkillLink) bool {
	for _, l := range links {
		if l.Missing {
			return true
		}
	}
	return false
}

// scanSkillSymlinkTree mirrors scan_shared_skill_symlink_tree
// (claude-contained:1716-1743): resolve scanDir, bail out silently if it is
// not a directory or has already been scanned, then walk its symlinks in find
// order, recursing into every directory target immediately -- before moving
// on to the next symlink at the current level, matching bash's self-recursive
// for loop rather than gathering a level at a time.
func scanSkillSymlinkTree(scanDir string, seen map[string]bool) []plan.SharedSkillLink {
	resolved := host.ResolvePath(scanDir)
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return nil
	}
	if seen[resolved] {
		return nil
	}
	seen[resolved] = true

	links, err := host.ScanSymlinks(resolved)
	if err != nil {
		return nil
	}

	var out []plan.SharedSkillLink
	for _, link := range links {
		target := host.ResolvePath(link)
		info, err := os.Stat(target)
		if err != nil {
			out = append(out, plan.SharedSkillLink{Path: link, Resolved: target, Missing: true})
			return out
		}
		if info.IsDir() {
			out = append(out, plan.SharedSkillLink{Path: link, Resolved: target, IsDir: true})
			// A Missing entry from this recursive call is not specially
			// propagated -- this frame keeps scanning its own remaining
			// siblings after it. That is safe only because the consumer
			// (sharedSkillsMounts) processes Links strictly in order and stops
			// at the first Missing entry: anything appended after one, at any
			// depth, is inert. Bash's `exit 2` inside the recursive call would
			// terminate the whole script instead, but the two are
			// observationally identical since neither ever looks past the
			// first Missing entry.
			out = append(out, scanSkillSymlinkTree(target, seen)...)
			continue
		}
		parent := host.ResolvePath(filepath.Dir(target))
		out = append(out, plan.SharedSkillLink{Path: link, Resolved: target, ParentDir: parent})
	}
	return out
}
