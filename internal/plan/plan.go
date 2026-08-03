// Package plan turns a parsed command line, probed host state and the answers
// given so far into an ordered list of host mutations plus the intended
// container run.
//
// Build is a *resumable pure function*, not a single-shot one. Prompt answers
// are inputs, never outputs, and the interleaving of prompts and mutations is
// preserved rather than flattened: the bash launcher applies some mutations
// before asking a later question, so a user who aborts at the second prompt has
// already had the first batch applied. Building the whole plan up front and
// applying it afterwards would quietly change that.
package plan

import (
	"fmt"
	"path/filepath"
	"strings"

	"claude-contained/internal/cli"
	"claude-contained/internal/env"
	"claude-contained/internal/host"
	"claude-contained/internal/runtime"
	"claude-contained/internal/zellij"
)

// Image is the tag the launcher runs. It is the *product* name, identical for
// both container runtimes, and is the fixed separator between runtime flags and
// the container command.
const Image = "claude-contained:latest"

// shellPath is what -s runs inside the container: a wrapper that gives bash a
// controlling terminal after the sandbox/runtime handoff, not bash itself.
const shellPath = "/usr/local/bin/shell-run"

// Program is the result of one Build call.
type Program struct {
	// Steps are the ordered host mutations determined so far. A later call
	// re-emits the same prefix, so the driver skips what it has already
	// applied by index.
	Steps []Step
	// Pending is non-nil when an answer is required before more can be
	// determined.
	Pending *Prompt
	// Run is non-nil only when Pending is nil: the intended invocation.
	Run *runtime.RunSpec
}

// ToolError reports an unknown -t/--tool value. Bash discovers this late, after
// mounts and mutations have already been applied, so it is returned alongside a
// populated Program rather than short-circuiting the plan.
type ToolError struct{ Tool string }

func (e *ToolError) Error() string { return "unknown tool: " + e.Tool }

// Build is pure: no I/O, no clock, no environment access. It is deterministic
// in its inputs, which is what makes the already-executed prefix safe to replay
// and skip.
func Build(cfg cli.Config, h host.State, f Facts, prof runtime.Profile, ans Answers) (Program, error) {
	var p Program

	// --- The worktree .git question, and the mount it controls -------------
	worktreeRepo := f.WorktreeMainRepo
	if worktreeRepo != "" {
		if cfg.WorktreeMode {
			p.Steps = append(p.Steps, Print{
				Text: "Worktree detected: mounting " + worktreeRepo + "/.git for full git access",
			})
		} else {
			p.Steps = append(p.Steps, Print{
				Text: "Working directory is a git worktree linked to: " + worktreeRepo,
			})
			answer, asked := ans[PromptWorktreeGit]
			if !asked {
				p.Pending = &Prompt{
					ID:      PromptWorktreeGit,
					Text:    "Mount main repository's .git for full git access? [Y/n] ",
					Default: true,
				}
				return p, nil
			}
			if !answer {
				worktreeRepo = ""
			}
		}
	}

	// --- Host paths, and the persistent directory set ----------------------
	paths := newHostPaths(h, cfg.ShareHostClaude)

	// reg is bookkeeping only, mirroring bash's user_mount_* /
	// shared_skill_readonly_mount_* arrays (claude-contained:1580-1586) so
	// --share-skills mounts can be checked against every mount registered
	// before them. It has no bearing on args other than through
	// sharedSkillsMounts below.
	reg := newMountRegistry(f.ProjectDir)

	p.Steps = append(p.Steps,
		MkdirAll{paths.ClaudeDir},
		MkdirAll{paths.ClaudeContained},
		MkdirAll{paths.ClaudeProfileDir},
		MkdirAll{paths.CodexDir},
		MkdirAll{paths.CopilotDir},
		MkdirAll{paths.GeminiDir},
		MkdirAll{paths.VibeDir},
	)

	// The Zellij session store persists across container lifetimes; the runtime
	// socket tree does not, and is pinned inside the container by zellij-run's
	// XDG_RUNTIME_DIR (image/zellij-run.sh:61,79). Creating these on the host is
	// what makes a session survive its container (claude-contained:1522-1524).
	if f.ZellijSession != "" {
		zellijRoot := filepath.Join(paths.ClaudeContained, "zellij")
		p.Steps = append(p.Steps,
			MkdirAll{filepath.Join(zellijRoot, "data")},
			MkdirAll{filepath.Join(zellijRoot, "cache")},
		)
	}

	if !cfg.ShareHostClaude {
		for _, resource := range claudeExtensionResources {
			p.Steps = append(p.Steps,
				MkdirAll{filepath.Join(paths.ClaudeDir, resource)},
				MkdirAll{filepath.Join(paths.ClaudeProfileDir, resource)},
			)
		}
	}

	// --- Container name ----------------------------------------------------
	containerName := cfg.CustomContainerName
	if containerName != "" {
		p.Steps = append(p.Steps, Print{
			Text: "Creating named container: " + strings.TrimPrefix(containerName, "aic-"),
		})
	} else {
		containerName = fmt.Sprintf("aic-%s-%s",
			host.SanitizeFolderName(f.ProjectDir), h.Now.Format("1504"))
	}
	containerName = deduplicateName(containerName, f.RunningContainers)

	// --- The worktree auto-lock offer ---------------------------------------
	// claude-contained:1541 (worktree_lock_repo fallback) through :1563
	// (maybe_offer_worktree_locks). Which candidate set applies depends on the
	// PromptWorktreeGit answer above: accepting it changes both the repository
	// at risk and the mounted-root set the hidden-worktree scan runs against,
	// so the two sets are probed independently rather than derived from one
	// another (see Facts.WorktreeLocks).
	lockCandidates := f.WorktreeLocks
	if worktreeRepo != "" {
		lockCandidates = f.WorktreeLocksWithGitMount
	}
	if lockCandidates.Repo != "" && len(lockCandidates.Hidden) > 0 {
		p.Steps = append(p.Steps, Print{
			Text: fmt.Sprintf("%d linked worktree(s) under %s hidden from container (prune risk).",
				len(lockCandidates.Hidden), lockCandidates.Repo),
		})

		lockThem := cfg.LockWorktrees
		if !lockThem {
			answer, asked := ans[PromptWorktreeLocks]
			if !asked {
				p.Pending = &Prompt{
					ID:      PromptWorktreeLocks,
					Text:    "Auto-lock them while this container runs? [Y/n] ",
					Default: true,
				}
				return p, nil
			}
			lockThem = answer
		}
		if lockThem {
			p.Steps = append(p.Steps, WorktreeAutoLock{
				Repo:      lockCandidates.Repo,
				Worktrees: lockCandidates.Hidden,
				Owner:     containerName,
			})
		}
	}

	// --- The run itself, assembled in the order bash emits it --------------
	var args []runtime.Arg
	add := func(a ...runtime.Arg) { args = append(args, a...) }

	add(
		runtime.MemoryArg{Value: h.Memory},
		runtime.NameArg{Value: containerName},
		runtime.EnvArg{Key: "HOST_HOME", Value: h.Home},
		runtime.EnvArg{Key: "HOST_UID", Value: h.UID},
		runtime.EnvArg{Key: "HOST_GID", Value: h.GID},
		runtime.WorkdirArg{Value: f.ProjectDir},
		runtime.MountArg{Src: paths.ClaudeProfileDir, Dst: paths.ContainerClaudeDir},
		runtime.MountArg{Src: paths.CodexDir, Dst: paths.CodexDir},
		runtime.MountArg{Src: paths.CopilotDir, Dst: paths.CopilotDir},
		runtime.MountArg{Src: paths.GeminiDir, Dst: paths.GeminiDir},
		runtime.MountArg{Src: paths.VibeDir, Dst: paths.VibeDir},
		runtime.MountArg{Src: f.ProjectDir, Dst: f.ProjectDir},
	)

	// The tool process environment, already resolved: command line, then the
	// project env file, then the launcher's built-ins, each key exactly once.
	for _, e := range f.Env {
		add(runtime.EnvArg{Key: e.Key, Value: e.Value})
	}
	add(runtime.EnvArg{Key: env.ExplicitKeysMarker, Value: env.ExplicitKeysValue(f.Env)})

	// Claude extension resources are mounted individually so the contained
	// profile can share them with the host profile.
	if !cfg.ShareHostClaude {
		for _, resource := range claudeExtensionResources {
			if resource == "skills" && cfg.ShareSkillsDir != "" {
				continue
			}
			add(runtime.MountArg{
				Src: filepath.Join(paths.ClaudeDir, resource),
				Dst: filepath.Join(paths.ContainerClaudeDir, resource),
			})
		}
	}

	// Only .git is mounted, not the working tree.
	if worktreeRepo != "" {
		gitDir := filepath.Join(worktreeRepo, ".git")
		add(runtime.MountArg{Src: gitDir, Dst: gitDir})
	}

	// GIT_PROTECT_DIRS keeps AI tools from rewriting remote URLs, and is the
	// only input to the in-container sandbox's writable-path policy -- which is
	// why declining the worktree prompt above changes it.
	protect := append([]string{f.ProjectDir}, f.ExtraMounts...)
	if worktreeRepo != "" {
		protect = append(protect, worktreeRepo)
	}
	add(runtime.EnvArg{Key: "GIT_PROTECT_DIRS", Value: strings.Join(protect, ":")})

	for _, pm := range cfg.PortMaps {
		add(runtime.PortArg{Spec: pm})
	}
	if len(cfg.HostForwards) > 0 {
		add(runtime.EnvArg{Key: "HOST_FORWARD_PORTS", Value: strings.Join(cfg.HostForwards, ",")})
		// Emitted, not refused: -H also serves host services listening on
		// 0.0.0.0, which every runtime reaches, so a refusal would remove working
		// behavior. The notice sits exactly where bash's does
		// (claude-contained:1798-1804) so the two stderr streams agree line for
		// line -- the tool warning (:1882), the shared-skills lines (:1906) and
		// the node_modules notice (:1939) all come after it, and the steps below
		// are emitted in that same order.
		//
		// A Print step rather than an Fprintln here, because Build is a pure
		// resumable function: its prefix is re-emitted on every prompt round, so
		// printing directly would repeat the notice once per round.
		for _, line := range prof.HostForwardNotice {
			p.Steps = append(p.Steps, Print{Text: line, Stderr: true})
		}
	}
	for _, ip := range resolveDNS(cfg, h, prof) {
		add(runtime.DNSArg{Server: ip})
	}
	if cfg.SrtDisable {
		add(runtime.EnvArg{Key: "SRT_DISABLE", Value: "1"})
	}
	if len(cfg.SrtAllowHosts) > 0 {
		add(runtime.EnvArg{Key: "SRT_ALLOW_HOSTS", Value: strings.Join(cfg.SrtAllowHosts, ",")})
	}
	// Both markers, always together: image/srt-settings.sh:96,155 and
	// image/entrypoint.sh:105 each gate on marker==1 AND a non-empty session, so
	// emitting one without the other yields a sandbox policy that blocks Zellij's
	// own socket. The labels are Docker-only (Apple Containers drops LabelArg)
	// and are deliberately never read back: discovery uses the environment,
	// which both runtimes expose (ADR-0002).
	if f.ZellijSession != "" {
		add(
			runtime.EnvArg{Key: zellij.MarkerEnv, Value: "1"},
			runtime.EnvArg{Key: zellij.SessionEnv, Value: f.ZellijSession},
			runtime.LabelArg{Key: zellij.LabelMarker, Value: "1"},
			runtime.LabelArg{Key: zellij.LabelSession, Value: f.ZellijSession},
		)
	}
	if cfg.SSHMode {
		add(runtime.SSHArg{})
	}
	add(runtime.HostGatewayArg{})

	// --- Account state relocation, then the shared-directory mount ---------
	p.Steps = append(p.Steps, accountStateSteps(paths, f)...)
	add(runtime.MountArg{Src: paths.ClaudeContained, Dst: paths.ClaudeContained})

	// --- Tool selection ----------------------------------------------------
	// Bash validates the tool here, not during parsing: by this point every
	// mount and host mutation above has already happened, and corpus entry 07
	// asserts exactly that.
	toolArgv, warn, err := toolCommand(cfg.Tool, cfg.YoloMode)
	if warn != "" {
		p.Steps = append(p.Steps, Print{Text: warn, Stderr: true})
	}
	if err != nil {
		return p, err
	}

	// --- Extra mounts ------------------------------------------------------
	for i, src := range f.ExtraMounts {
		mode := f.ExtraModes[i]
		add(runtime.MountArg{Src: src, Dst: src, ReadOnly: mode == "ro"})
		reg.addUser(src, src, mode)
		// Only claude and codex understand --add-dir.
		if cfg.Tool == "claude" || cfg.Tool == "codex" {
			toolArgv = append(toolArgv, "--add-dir", src)
		}
	}

	// --- Shared skills -------------------------------------------------------
	// claude-contained:1889-1891, run immediately after the extra-mount loop
	// and before the node_modules block below.
	if f.SharedSkills.Dir != "" {
		steps, sharedArgs, err := sharedSkillsMounts(reg, paths, f.SharedSkills)
		p.Steps = append(p.Steps, steps...)
		add(sharedArgs...)
		if err != nil {
			return p, err
		}
	}

	// --- The node_modules overlay question ---------------------------------
	if len(f.NodeOverlayDirs) > 0 {
		overlay := cfg.ContainedNodeModules
		if !overlay {
			answer, asked := ans[PromptNodeModules]
			if !asked {
				p.Pending = &Prompt{
					ID:      PromptNodeModules,
					Text:    "Node.js project detected. Use container-specific node_modules? [Y/n] ",
					Default: true,
				}
				return p, nil
			}
			overlay = answer
		}
		if overlay {
			platform := "linux-" + h.Arch
			for _, dir := range f.NodeOverlayDirs {
				target := filepath.Join(dir, ".claude-contained", "node_modules-"+platform)
				p.Steps = append(p.Steps, MkdirAll{target})
				add(runtime.MountArg{Src: target, Dst: filepath.Join(dir, "node_modules")})
				if f.NodeOverlayTargetEmpty[dir] {
					p.Steps = append(p.Steps,
						Print{Text: "Created " + target, Stderr: true},
						Print{Text: "  Run 'npm install' (or your package manager) inside the container.", Stderr: true},
					)
				}
			}
		}
	}

	// --- The container command ---------------------------------------------
	toolArgv = append(toolArgv, cfg.ToolArgs...)

	command := toolArgv
	if cfg.ShellMode {
		// Under Zellij the debug shell is plain bash: shell-run exists to give
		// bash a controlling terminal after the sandbox/runtime handoff, which
		// Zellij's own pane already provides (claude-contained:1957-1961).
		if f.ZellijSession != "" {
			command = []string{zellij.ShellCommand}
		} else {
			command = []string{shellPath}
		}
	}
	if f.ZellijSession != "" {
		command = zellij.RunCommand(f.ZellijSession, prof.Name, command)
	}

	// A project with a tooling layer runs its derived image in place of the
	// base one. Nothing else about the run changes: same args, same command,
	// only the image operand moves.
	image := Image
	if f.DerivedImage != "" {
		image = f.DerivedImage
	}
	p.Run = &runtime.RunSpec{Args: args, Image: image, Command: command}
	return p, nil
}

// claudeExtensionResources are the shared Claude file resources, in the order
// bash iterates them.
var claudeExtensionResources = []string{"skills", "agents", "commands", "plugins"}

// resolveDNS mirrors the CLAUDE_DNS handling. The distinction between "unset"
// and "set but empty" is the whole point: unset takes the runtime's default
// (a public resolver for Apple Containers, nothing for Docker), while an empty
// value, "system" or "none" opts back into the runtime's own resolver.
func resolveDNS(cfg cli.Config, h host.State, prof runtime.Profile) []string {
	if len(cfg.DNSServers) > 0 {
		return cfg.DNSServers
	}
	if h.DNSEnvSet {
		switch h.DNSEnv {
		case "", "system", "none":
			return nil
		default:
			return strings.Split(h.DNSEnv, ",")
		}
	}
	return prof.DefaultDNS
}

// deduplicateName appends -2, -3, ... while the name is already taken.
func deduplicateName(name string, running []string) string {
	taken := make(map[string]bool, len(running))
	for _, r := range running {
		taken[r] = true
	}
	base := name
	for suffix := 2; taken[name]; suffix++ {
		name = fmt.Sprintf("%s-%d", base, suffix)
	}
	return name
}

// accountStateSteps ports the git config copy and the ~/.claude.json migration.
//
// The container runtime cannot bind-mount an individual file, so the account
// state lives in the shared directory with a symlink at the original location.
// This is the mutation that can destroy credentials, so it follows bash's two
// blocks structurally -- a migrate/repair decision, then a separate
// link-if-missing decision that sees the *result* of the first.
func accountStateSteps(paths hostPaths, f Facts) []Step {
	var steps []Step

	if f.GitConfigExists {
		steps = append(steps, CopyFile{
			Src: paths.GitConfig,
			Dst: filepath.Join(paths.ClaudeContained, ".gitconfig"),
		})
	}

	a := f.AccountState
	// hostExists tracks `-e ~/.claude.json` as the first block mutates it, so
	// the second block tests the post-migration state rather than the original.
	hostExists := a.Exists

	switch {
	case a.IsRegularFile && !a.IsSymlink:
		// A real file that has not been migrated: relocate it and leave a link
		// behind. The `&& !IsSymlink` is what keeps an already-migrated symlink
		// out of this branch -- without it, the rename would point the link at
		// itself and the next run would clean it up as dangling.
		steps = append(steps,
			MoveFile{Src: paths.ClaudeJSON, Dst: paths.SharedClaudeJSON},
			Symlink{Target: paths.SharedClaudeJSON, Link: paths.ClaudeJSON},
		)
		hostExists = true
	case a.IsSymlink && !a.SharedExists:
		// A link with nothing behind it. Note this tests SharedExists, not
		// SharedIsRegularFile: a directory at the shared path means the link is
		// not broken, so it must be left alone.
		steps = append(steps, RemoveFile{Path: paths.ClaudeJSON})
		hostExists = false
	}

	// Link only when nothing is at the original location and the shared path
	// holds an actual file.
	if !hostExists && a.SharedIsRegularFile {
		steps = append(steps, Symlink{Target: paths.SharedClaudeJSON, Link: paths.ClaudeJSON})
	}
	return steps
}

// toolCommand maps the tool name to its command and yolo flag. The warning is
// returned rather than printed so it stays ordered with the other steps.
func toolCommand(tool string, yolo bool) (argv []string, warning string, err error) {
	switch tool {
	case "claude":
		argv = []string{"claude"}
		if yolo {
			argv = append(argv, "--dangerously-skip-permissions")
		}
	case "codex", "copilot", "gemini":
		argv = []string{tool}
		if yolo {
			argv = append(argv, "--yolo")
		}
	case "vibe":
		argv = []string{"vibe"}
		if yolo {
			warning = "Warning: vibe does not support yolo mode (no equivalent flag)"
		}
	default:
		return nil, "", &ToolError{Tool: tool}
	}
	return argv, warning, nil
}
