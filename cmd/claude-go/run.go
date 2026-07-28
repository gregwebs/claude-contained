package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"claude-contained/internal/cli"
	"claude-contained/internal/env"
	"claude-contained/internal/host"
	"claude-contained/internal/plan"
	"claude-contained/internal/runtime"
)

// run is the single point every exit path returns through.
func run(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	rt := runtime.Select(argv[0], "", "")
	prof := rt.Profile()

	h := host.Probe()

	cfg, err := cli.Parse(argv[1:], prof.Name, h.ShareHostClaude, stderr)
	if err != nil {
		return exitCode(err)
	}
	if cfg.HelpRequested {
		fmt.Fprint(stdout, prof.Help)
		return cli.ExitOK
	}

	// The tool process environment. Command-line variables are validated here,
	// before the runtime-liveness prompt below, so a bad -e fails without first
	// offering to start the container runtime.
	envStore := env.New()
	for _, assignment := range cfg.EnvFlagArgs {
		if err := envStore.Set(assignment, "--env", env.Flag); err != nil {
			fmt.Fprintln(stderr, err.Error())
			return cli.ExitUsage
		}
	}

	// --share-skills is validated here too (claude-contained:872-878): the
	// directory must exist before the runtime-liveness prompt, and once
	// resolved this is the value every downstream mount uses.
	shareSkillsDir := cfg.ShareSkillsDir
	if shareSkillsDir != "" {
		if info, err := os.Stat(shareSkillsDir); err != nil || !info.IsDir() {
			fmt.Fprintf(stderr, "error: --share-skills directory does not exist: %s\n", shareSkillsDir)
			return cli.ExitUsage
		}
		shareSkillsDir = host.ResolvePath(shareSkillsDir)
	}

	ctx := context.Background()
	prompter := newPrompter(stdin, stderr)

	// The runtime-liveness check comes before anything else touches the host,
	// matching bash. Declining is an abort, not a failure.
	if err := rt.EnsureUp(ctx, stdout, prompter.confirm); err != nil {
		if errors.Is(err, runtime.ErrAborted) {
			fmt.Fprintln(stdout, "Aborted.")
			return cli.ExitFailure
		}
		return cli.ExitFailure
	}

	// Bash rebuilds or execs into an attach before it ever looks at the project
	// directory, so these are refused first.
	if err := cli.CheckUnportedEarly(cfg, stderr); err != nil {
		return exitCode(err)
	}

	projectDir := cfg.ProjectDir
	if projectDir == "" {
		projectDir = "."
	}
	// The project directory's mode is always rw -- splitMountMode rejects a :ro
	// suffix on it outright -- so only the stripped path is of interest here.
	projectDir, _, err = splitMountMode(projectDir, true, cfg.ReadonlyExtras, stderr)
	if err != nil {
		return exitCode(err)
	}

	extraMounts := make([]string, 0, len(cfg.ExtraMounts))
	extraModes := make([]string, 0, len(cfg.ExtraMounts))
	for _, m := range cfg.ExtraMounts {
		path, mode, err := splitMountMode(m, false, cfg.ReadonlyExtras, stderr)
		if err != nil {
			return exitCode(err)
		}
		extraMounts = append(extraMounts, host.ResolvePath(path))
		extraModes = append(extraModes, mode)
	}

	mainHost := host.ResolvePath(projectDir)

	// Order here is bash's, and it is observable: the placeholder sweep of the
	// project directory happens at claude-contained:1410, *before* the env file
	// is read at :1416. Reading the file first would leave those stale files on
	// disk when a bad file aborts the run, which the differential harness sees
	// as a filesystem-manifest divergence.
	host.CleanupPlaceholderFiles(mainHost)

	// The project env file and the built-ins complete the environment. This runs
	// before the worktree handling below, so a rejected file fails without first
	// asking the user about locks and taking them -- there is nothing to unwind.
	if code := completeEnv(envStore, h, cfg.NoProjectEnv, mainHost, stderr); code != 0 {
		return code
	}

	// Only now the flags bash handles downstream of the env file. Refusing these
	// any earlier would mask an exit 2 that a bad env file would have produced.
	if err := cli.CheckUnportedLate(cfg, stderr); err != nil {
		return exitCode(err)
	}

	mountedRoots := append([]string{mainHost}, extraMounts...)
	host.CleanupPlaceholderFiles(mountedRoots...)

	facts, err := probeFacts(ctx, rt, h, cfg, mainHost, extraMounts, extraModes, shareSkillsDir)
	facts.Env = envStore.Pairs()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return cli.ExitFailure
	}

	// Worktree auto-locking is ticket 06, and it is reached with no flag at
	// all -- so it needs an explicit guard here rather than a flag-keyed one,
	// or a repository with hidden linked worktrees would silently lose the
	// prune protection the bash launcher offers.
	//
	// Which repository bash ends up checking depends on the worktree prompt:
	// accepting mounts the main repository's .git, which is what makes its
	// worktrees prunable from inside the container, and only then is that
	// repository the lock target. Since the guard has to run before any
	// mutation -- and therefore before the prompt -- it tests both candidates
	// and refuses if either would have triggered the offer. Erring toward
	// refusing is right for an unported path; erring the other way is a silent
	// divergence in exactly the case the protection exists for.
	if worktreeGitWouldBeMounted(facts.WorktreeMainRepo, mountedRoots) ||
		len(host.HiddenWorktrees(host.MainWorktreeRepoRoot(mainHost), mountedRoots)) > 0 {
		fmt.Fprintln(stderr, "error: worktree auto-locking is not yet supported by the Go launcher")
		return cli.ExitUnported
	}

	program, code := buildAndApply(cfg, h, facts, prof, prompter, stdout, stderr)
	if code != 0 {
		return code
	}

	// Bash routes INT/TERM/HUP through `exit` so its EXIT trap always runs, and
	// relies on bash deferring a trapped signal until the foreground container
	// run returns -- so cleanup never fires while the container could still be
	// pruning worktrees. Notify gives the same deferral for free: the container
	// shares this process group and receives the signal directly, so we simply
	// stop the handler from killing us and let the wait below finish first.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)

	argvRun := rt.RenderRun(*program.Run)
	containerExit := execRuntime(ctx, argvRun, stdin, stdout, stderr)

	// Cleanup mirrors the bash EXIT trap.
	host.CleanupPlaceholderFiles(mountedRoots...)

	// A signal that arrived during the run wins over the container's own
	// status, matching bash's 130/143/129, and skips the update check.
	select {
	case sig := <-signals:
		return signalExitCode(sig)
	default:
	}

	checkForUpdates(argv[0], stdout)
	return containerExit
}

// signalExitCode mirrors bash's `trap 'exit N'` handlers: 128 + the signal
// number.
func signalExitCode(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return 128 + int(s)
	}
	return cli.ExitFailure
}

// completeEnv adds the project env file and the launcher's built-ins to the
// store, then reports which variables are being passed in.
//
// The file is read here and handed to the store as bytes: it is writable from
// inside the container, so it is parsed literally and never evaluated. An absent
// file is a silent success, matching bash's `[[ -f "$file" ]] || return 0`.
func completeEnv(store *env.Store, h host.State, noProjectEnv bool, projectDir string, stderr io.Writer) int {
	if !noProjectEnv {
		path := filepath.Join(projectDir, env.FileName)

		// bash gates on `[[ -f "$file" ]]`, which is false for anything that is
		// not a regular file. A directory -- or a fifo, which would otherwise
		// block us forever in ReadFile -- is therefore a silent success, not an
		// error. Stat follows symlinks, as `-f` does.
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				// Regular but unreadable. bash's `done < "$file"` redirection
				// fails, the function returns non-zero, and its caller's
				// `|| exit 2` turns that into status 2 -- which we match.
				//
				// The *message* deliberately differs: bash emits its own
				// redirection diagnostic, which embeds the script's path and a
				// literal source line number ("claude-contained: line 274:").
				// Reproducing that would hardcode a line number in a file this
				// rewrite exists to delete.
				fmt.Fprintf(stderr, "error: cannot read %s: %v\n", env.FileName, err)
				return cli.ExitUsage
			}
			if err := store.LoadFile(content); err != nil {
				fmt.Fprintln(stderr, err.Error())
				return cli.ExitUsage
			}
		}
	}

	// Built-ins only fill gaps, so a user-supplied TZ replaces this one rather
	// than being emitted alongside it.
	if h.Timezone != "" {
		if err := store.Default("TZ="+h.Timezone, "host timezone", env.Builtin); err != nil {
			fmt.Fprintln(stderr, err.Error())
			return cli.ExitUsage
		}
	}
	if h.GHToken != "" {
		if err := store.Default("GH_TOKEN="+h.GHToken, "AI_GH_TOKEN", env.Builtin); err != nil {
			fmt.Fprintln(stderr, err.Error())
			return cli.ExitUsage
		}
	}

	// Names only -- values routinely hold tokens and this lands in scrollback.
	if summary := store.Summary(); summary != "" {
		fmt.Fprintln(stderr, summary)
	}
	return 0
}

// worktreeGitWouldBeMounted reports whether accepting the worktree prompt would
// expose hidden sibling worktrees to pruning. Accepting adds the main
// repository's .git to the mounted roots, so the visibility check has to be
// made against that enlarged set, not the current one.
func worktreeGitWouldBeMounted(worktreeMainRepo string, mountedRoots []string) bool {
	if worktreeMainRepo == "" {
		return false
	}
	roots := append(append([]string{}, mountedRoots...), filepath.Join(worktreeMainRepo, ".git"))
	return len(host.HiddenWorktrees(worktreeMainRepo, roots)) > 0
}

// buildAndApply drives the resumable planner: build, apply the steps that are
// new since the last round, answer any pending prompt, repeat. Because Build is
// deterministic, the re-emitted prefix is identical and is skipped by index.
func buildAndApply(
	cfg cli.Config, h host.State, facts plan.Facts, prof runtime.Profile,
	prompter *prompter, stdout, stderr io.Writer,
) (plan.Program, int) {
	answers := plan.Answers{}
	appliedCount := 0

	for {
		program, err := plan.Build(cfg, h, facts, prof, answers)

		// Steps are applied even when Build reported an error: bash discovers
		// an unknown tool only after these mutations have happened.
		n, applyErr := applySteps(program.Steps, appliedCount, stdout, stderr)
		appliedCount = n
		if applyErr != nil {
			fmt.Fprintf(stderr, "error: %v\n", applyErr)
			return program, cli.ExitFailure
		}

		if err != nil {
			var toolErr *plan.ToolError
			if errors.As(err, &toolErr) {
				fmt.Fprintf(stderr, "Unknown tool: %s\n", toolErr.Tool)
				fmt.Fprintln(stderr, "Supported tools: claude, codex, copilot, gemini, vibe")
				return program, cli.ExitFailure
			}
			// --share-skills conflicts and a missing symlink target are both
			// `exit 2` in bash, with a message that is already fully formatted
			// (claude-contained:1666-1685, :1729-1732) -- printed verbatim
			// rather than through the generic "error: %v" wrapper below, which
			// would double the "error: " prefix on the first line.
			var shareErr *plan.ShareSkillsError
			if errors.As(err, &shareErr) {
				for _, line := range shareErr.Lines {
					fmt.Fprintln(stderr, line)
				}
				return program, cli.ExitUsage
			}
			fmt.Fprintf(stderr, "error: %v\n", err)
			return program, cli.ExitFailure
		}

		if program.Pending == nil {
			return program, 0
		}

		answer, ok := prompter.ask(program.Pending.Text, program.Pending.Default)
		if !ok {
			// EOF on a prompt: bash's `read` returns non-zero and `set -e`
			// kills the script on that line, with no prompt text printed.
			return program, cli.ExitFailure
		}
		answers[program.Pending.ID] = answer
	}
}

// applySteps performs the steps from index `from` onward and returns the new
// applied count.
func applySteps(steps []plan.Step, from int, stdout, stderr io.Writer) (int, error) {
	for i := from; i < len(steps); i++ {
		switch s := steps[i].(type) {
		case plan.MkdirAll:
			// 0777 is masked by the process umask, exactly as `mkdir -p` does.
			// Hardcoding 0755 would diverge for any umask other than 022, and
			// the differential manifest records file modes.
			if err := os.MkdirAll(s.Path, 0o777); err != nil {
				return i, err
			}
		case plan.CopyFile:
			if err := copyFile(s.Src, s.Dst); err != nil {
				return i, err
			}
		case plan.MoveFile:
			if err := os.Rename(s.Src, s.Dst); err != nil {
				return i, err
			}
		case plan.Symlink:
			if err := os.Symlink(s.Target, s.Link); err != nil {
				return i, err
			}
		case plan.RemoveFile:
			if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
				return i, err
			}
		case plan.Print:
			w := stdout
			if s.Stderr {
				w = stderr
			}
			fmt.Fprintln(w, s.Text)
		}
	}
	return len(steps), nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode().Perm())
}

// splitMountMode mirrors the :ro / :rw suffix handling. The project directory
// cannot be read-only: the overlay and the sandbox both need to write into it.
func splitMountMode(spec string, isProject, readonlyDefault bool, stderr io.Writer) (string, string, error) {
	path, mode := spec, ""
	switch {
	case strings.HasSuffix(spec, ":ro"):
		path, mode = strings.TrimSuffix(spec, ":ro"), "ro"
	case strings.HasSuffix(spec, ":rw"):
		path, mode = strings.TrimSuffix(spec, ":rw"), "rw"
	}

	if isProject {
		if mode == "ro" {
			fmt.Fprintln(stderr, "error: project directory cannot be read-only")
			return "", "", &cli.ExitError{Code: cli.ExitUsage}
		}
		return path, "rw", nil
	}
	if mode == "" {
		if readonlyDefault {
			mode = "ro"
		} else {
			mode = "rw"
		}
	}
	return path, mode, nil
}

// execRuntime runs the container in the foreground. This deliberately does not
// exec: the launcher has to regain control afterwards to run its cleanup and
// the update check, then exit with the container's own status.
func execRuntime(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		return cli.ExitFailure
	}
	return cli.ExitOK
}

// checkForUpdates is best-effort and silent on every failure, matching the
// bash subshell that discards all of its own errors. The message names the
// product, so it stays literal for both container runtimes.
func checkForUpdates(argv0 string, stdout io.Writer) {
	scriptDir := filepath.Dir(host.ResolvePath(argv0))

	repoRoot, err := gitOutput(scriptDir, "rev-parse", "--show-toplevel")
	if err != nil || repoRoot == "" {
		return
	}
	if err := exec.Command("git", "-C", repoRoot, "fetch", "--quiet").Run(); err != nil {
		return
	}
	local, err := gitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return
	}
	upstream, err := gitOutput(repoRoot, "rev-parse", "@{u}")
	if err != nil {
		return
	}
	if local != upstream {
		fmt.Fprintln(stdout, "")
		fmt.Fprintf(stdout, "Update available for claude-contained! Run 'git -C %s pull' to update.\n", repoRoot)
	}
}

func gitOutput(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func exitCode(err error) int {
	var e *cli.ExitError
	if errors.As(err, &e) {
		return e.Code
	}
	return cli.ExitFailure
}

// prompter reads yes/no answers, reproducing three observable properties of
// bash's `read -p`:
//
//   - The prompt goes to **standard error**, not standard output (verified
//     under a pty: with stdout redirected to a file, the prompt still appears
//     on the terminal).
//   - It is printed only when stdin is a terminal, so a piped or /dev/null
//     stdin produces no prompt text at all.
//   - EOF makes `read` return non-zero, which under `set -e` ends the script.
type prompter struct {
	reader *bufio.Reader
	out    io.Writer
	isTTY  bool
}

func newPrompter(stdin io.Reader, stderr io.Writer) *prompter {
	tty := false
	if f, ok := stdin.(*os.File); ok {
		if info, err := f.Stat(); err == nil {
			tty = info.Mode()&os.ModeCharDevice != 0
		}
	}
	return &prompter{reader: bufio.NewReader(stdin), out: stderr, isTTY: tty}
}

// ask returns the answer and whether one could be read at all. ok=false means
// EOF, which bash turns into an immediate exit.
func (p *prompter) ask(text string, def bool) (bool, bool) {
	if p.isTTY {
		fmt.Fprint(p.out, text)
	}
	line, err := p.reader.ReadString('\n')
	if err != nil && line == "" {
		return false, false
	}
	answer := strings.TrimRight(line, "\r\n")
	if answer == "" {
		return def, true
	}
	// Bash matches a prefix: `yes`, `Y` and `yolo` all count as yes.
	return answer[0] == 'y' || answer[0] == 'Y', true
}

func (p *prompter) confirm(text string) bool {
	answer, ok := p.ask(text, true)
	return ok && answer
}
