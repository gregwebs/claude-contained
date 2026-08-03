package plan

import "claude-contained/internal/runtime"

// sharedSkillsSpec is one call to add_shared_skills_mount
// (claude-contained:1698-1704): mount dst backed by the shared dir, after
// mkdir -p'ing dstParent on the host first.
type sharedSkillsSpec struct{ dst, dstParent string }

// sharedSkillsMounts replays add_shared_skills_mounts (claude-contained:1745-1760)
// against reg and the probed symlink scan in ss.
//
// It returns whatever Steps and Args it produced before any error. That
// matters because bash's `exit 2` happens mid-function, after several
// `mkdir -p` calls and possibly some `--mount` registrations have already
// run -- those mutations survive the abort, and so does this replay's prefix.
func sharedSkillsMounts(reg *mountRegistry, paths hostPaths, ss SharedSkills, remountNeedsMountpoint bool) ([]Step, []runtime.Arg, error) {
	// Fail fast, before any mkdir or mount is emitted: on a runtime that cannot
	// create a bind destination under an already-applied read-only mount, the
	// Codex system-skills remount below (dst <codex>/skills/.system, nested
	// under the read-only shared mount at <codex>/skills) has nowhere to land
	// unless the shared dir itself carries a `.system` mountpoint. Left to the
	// runtime this surfaces as a cryptic "mount failed ... failed to create
	// directory '.system'" (EROFS) at container start; caught here it is an
	// actionable usage error with the one-line fix. See
	// github.com/gregwebs/claude-contained/issues/25 for the root-cause
	// analysis and the repair options this deliberately leaves open.
	if ss.CodexSystemDir && remountNeedsMountpoint && !ss.DirHasSystem {
		return nil, nil, &ShareSkillsError{Lines: []string{
			"error: --share-skills cannot expose Codex's built-in system skills here",
			"       This container runtime cannot create a mount point beneath a read-only mount,",
			"       and " + ss.Dir + " has no '.system' entry to hold " + paths.CodexDir + "/skills/.system.",
			"       Create the mount point in the shared skills directory, then re-run:",
			"           mkdir -p " + ss.Dir + "/.system",
		}}
	}

	var steps []Step
	var args []runtime.Arg

	addMount := func(mount *runtime.MountArg, line string) {
		if mount == nil {
			return
		}
		args = append(args, *mount)
		steps = append(steps, Print{Text: line, Stderr: true})
	}

	// The six per-tool skills mounts, in bash's order (claude-contained:1746-1751).
	specs := []sharedSkillsSpec{
		{dst: paths.ContainerClaudeDir + "/skills", dstParent: paths.ClaudeProfileDir},
		{dst: paths.CodexDir + "/skills", dstParent: paths.CodexDir},
		{dst: paths.AgentsDir + "/skills", dstParent: paths.AgentsDir},
		{dst: paths.CopilotDir + "/skills", dstParent: paths.CopilotDir},
		{dst: paths.GeminiDir + "/skills", dstParent: paths.GeminiDir},
		{dst: paths.VibeDir + "/skills", dstParent: paths.VibeDir},
	}
	for _, s := range specs {
		// mkdir happens unconditionally, before the mount attempt that might
		// error -- so it survives even when this spec is the one that aborts.
		steps = append(steps, MkdirAll{s.dstParent})
		mount, line, err := reg.addShared(ss.Dir, s.dst, false, "skills")
		if err != nil {
			return steps, args, err
		}
		addMount(mount, line)
	}

	// The shared dir mounted over itself at path parity, so absolute paths
	// under it keep resolving inside the container (claude-contained:1752).
	mount, line, err := reg.addShared(ss.Dir, ss.Dir, true, "shared skills source")
	if err != nil {
		return steps, args, err
	}
	addMount(mount, line)

	// Codex's built-in system skills stay visible underneath the shared
	// mount: re-mount .system back over its own path (claude-contained:1754-1757).
	if ss.CodexSystemDir {
		systemDir := paths.CodexDir + "/skills/.system"
		mount, line, err := reg.addShared(systemDir, systemDir, false, "Codex system skills")
		if err != nil {
			return steps, args, err
		}
		addMount(mount, line)
	}

	// The flattened symlink scan: .system's tree first (if present), then the
	// shared dir's, in the exact DFS order scan_shared_skill_symlink_tree
	// would visit them (claude-contained:1716-1743). Recursion into directory
	// targets is already flattened into this slice by the probe, so this loop
	// only has to replay it in order.
	for _, link := range ss.Links {
		if link.Missing {
			return steps, args, &ShareSkillsError{Lines: []string{
				"error: --share-skills symlink target does not exist: " + link.Path + " -> " + link.Resolved,
			}}
		}
		dst, label := link.ParentDir, "symlinked skills file parent"
		if link.IsDir {
			dst, label = link.Resolved, "symlinked skills target"
		}
		mount, line, err := reg.addShared(dst, dst, true, label)
		if err != nil {
			return steps, args, err
		}
		addMount(mount, line)
	}

	return steps, args, nil
}
