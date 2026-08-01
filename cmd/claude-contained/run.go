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
	"time"

	"claude-contained/internal/cli"
	"claude-contained/internal/diagnostic"
	"claude-contained/internal/env"
	"claude-contained/internal/host"
	"claude-contained/internal/plan"
	"claude-contained/internal/runtime"
)

// runner executes the container in the foreground. It is a seam so a test can
// observe launcher state *while the container is up*: release ordering is the
// property that matters and the end state cannot show it.
type runner func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int

// run is the single point every exit path returns through.
func run(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runWith(execRuntime, runtime.HostPlatform(), argv, stdin, stdout, stderr)
}

func diagnosticDestinationKind(path string) string {
	if path != "" {
		return "file"
	}
	return "stderr"
}

func diagnosticToolName(tool string) string {
	switch tool {
	case "claude", "codex", "copilot", "gemini", "vibe":
		return tool
	default:
		return "invalid"
	}
}

// plat is a parameter for the same reason exec is: it is the only way a test can
// exercise the Docker-on-Linux and Docker-on-macOS configurations from either
// host. Without it, every test here would silently change which runtime it
// selects depending on the machine it runs on -- and CI is Linux while
// development is macOS.
func runWith(exec runner, plat runtime.Platform, argv []string, stdin io.Reader, stdout, stderr io.Writer) (status int) {
	terminalStdout, terminalStderr := stdout, stderr

	// Probe first: the selection reads CLAUDE_CONTAINED_RUNTIME out of host
	// state, like every other environment variable the launcher honors. Probe
	// itself is unobservable here -- it reads env, `uname -m` and /etc/localtime.
	h := host.Probe()

	cfg := cli.Parse(argv[1:], runtime.ProgName, h.ShareHostClaude)
	sel := runtime.Selection{
		Flag:     cfg.ContainerRuntime,
		Env:      h.ContainerRuntime,
		Argv0:    argv[0],
		Platform: plat,
	}
	rt := runtime.Select(sel)
	prof := rt.Profile()

	if cfg.HelpRequested {
		_, _ = fmt.Fprint(terminalStdout, prof.Help)
		return cli.ExitOK
	}

	resolution, err := diagnostic.ResolveLevel(cfg.LogLevel, cfg.LogLevelSet, h.LogLevel, cfg.LogOnly)
	if err != nil {
		_, _ = fmt.Fprintf(terminalStderr, "error: %v\n", err)
		return cli.ExitUsage
	}
	stream, err := diagnostic.Open(diagnostic.Options{
		Resolution: resolution,
		FilePath:   cfg.LogFile,
		LogOnly:    cfg.LogOnly,
	}, terminalStderr)
	if err != nil {
		_, _ = fmt.Fprintf(terminalStderr, "error: %v\n", err)
		return cli.ExitUsage
	}
	// Registered before host cleanup defers below: LIFO ordering keeps the
	// stream available for cleanup diagnostics and closes it last.
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			_, _ = fmt.Fprintf(terminalStderr, "error: diagnostic stream failed: %v\n", closeErr)
			if status == cli.ExitOK {
				status = cli.ExitFailure
			}
		}
	}()

	ctx := stream.Context(context.Background())
	stdout, stderr = stream.Writers(stdout, stderr)
	diagnostic.For(ctx, diagnostic.ComponentCLI).Info("diagnostic stream configured",
		diagnostic.String("log_level", resolution.Level.String()),
		diagnostic.String("log_level_source", resolution.Source.String()),
		diagnostic.String("destination_kind", diagnosticDestinationKind(cfg.LogFile)),
		diagnostic.Bool("log_only", cfg.LogOnly),
	)
	diagnostic.For(ctx, diagnostic.ComponentCLI).Debug("launcher configuration parsed",
		diagnostic.String("tool", diagnosticToolName(cfg.Tool)),
		diagnostic.Bool("shell", cfg.ShellMode),
		diagnostic.Bool("attach", cfg.AttachMode),
		diagnostic.Bool("zellij", cfg.ZellijMode),
		diagnostic.Int("environment_assignments", len(cfg.EnvFlagArgs)),
	)
	diagnostic.For(ctx, diagnostic.ComponentHost).Debug("host state probed",
		diagnostic.Value("state", h))

	err = cli.ValidateContext(ctx, &cfg, stderr)
	if err != nil {
		return exitCode(err)
	}

	// After help and CLI validation, and before anything interacts with the
	// selected runtime. That ordering lets Apple.EnsureUp assume a macOS host
	// and preserves CLI diagnostics ahead of runtime-selection diagnostics.
	selection := runtime.DiagnosticSelection(sel)
	selectionLogger := diagnostic.For(ctx, diagnostic.ComponentRuntime)
	if err := runtime.ValidateSelection(sel, stderr); err != nil {
		selectionLogger.Warn("container runtime selection invalid",
			diagnostic.Value("selection", selection))
		return cli.ExitUsage
	}
	selectionLogger.Info("container runtime selected", diagnostic.Value("selection", selection))

	// The tool process environment. Command-line variables are validated here,
	// before the runtime-liveness prompt below, so a bad -e fails without first
	// offering to start the container runtime.
	envStore := env.New()
	for _, assignment := range cfg.EnvFlagArgs {
		if err := envStore.Set(assignment, "--env", env.Flag); err != nil {
			diagnostic.For(ctx, diagnostic.ComponentEnv).Warn("command-line environment assignment validation failed",
				diagnostic.ErrorAttr(err))
			_, _ = fmt.Fprintln(stderr, err.Error())
			return cli.ExitUsage
		}
	}

	// --share-skills is validated here too (claude-contained:872-878): the
	// directory must exist before the runtime-liveness prompt, and once
	// resolved this is the value every downstream mount uses.
	shareSkillsDir := cfg.ShareSkillsDir
	if shareSkillsDir != "" {
		if info, err := os.Stat(shareSkillsDir); err != nil || !info.IsDir() {
			_, _ = fmt.Fprintf(stderr, "error: --share-skills directory does not exist: %s\n", shareSkillsDir)
			return cli.ExitUsage
		}
		shareSkillsDir = host.ResolvePath(shareSkillsDir)
	}

	prompter := newPrompter(stdin, terminalStderr, isTerminal(stdin))

	// The runtime-liveness check comes before anything else touches the host,
	// matching bash. Declining is an abort, not a failure.
	livenessStarted := time.Now()
	if err := rt.EnsureUp(ctx, stdout, stderr, prompter.confirm); err != nil {
		diagnostic.For(ctx, diagnostic.ComponentRuntime).Warn("container runtime liveness check failed",
			diagnostic.Duration("duration", time.Since(livenessStarted)), diagnostic.ErrorAttr(err))
		if errors.Is(err, runtime.ErrAborted) {
			_, _ = fmt.Fprintln(stdout, "Aborted.")
			return cli.ExitFailure
		}
		return cli.ExitFailure
	}
	diagnostic.For(ctx, diagnostic.ComponentRuntime).Debug("container runtime liveness check completed",
		diagnostic.Duration("duration", time.Since(livenessStarted)))

	// Bash rebuilds and exits before it ever looks at the project directory
	// (claude-contained:892-896), so this sits between the runtime-liveness
	// check and the attach dispatch: nothing below has run yet, so there is
	// nothing to unwind and no cleanup to skip.
	if cfg.RebuildMode != rebuildNone {
		return runRebuild(ctx, exec, rt, cfg.RebuildMode,
			host.BuildContextSources{Flag: cfg.BuildContext, Env: h.BuildContext, Self: selfPath(argv[0])},
			h.Now, stdin, stdout, stderr)
	}

	// Attach reconnects to a running container and *replaces* this process,
	// exactly where bash execs (claude-contained:952-1030). It sits here, not
	// lower, because everything below acquires something: the project
	// directory, the placeholder sweep, the env file, the worktree lock and
	// mutex, the signal handlers and the deferred cleanup. Attach must stay
	// ahead of all of it.
	if cfg.AttachMode {
		// Bash execs the Zellij attach at claude-contained:896-950, before the
		// plain attach block at :952. Both replace this process; neither ever
		// creates a container.
		if cfg.ZellijMode {
			return zellijAttachAndExec(ctx, exec, cfg.LogOnly, rt, cfg, h, prompter, stdout, stderr)
		}
		return attachAndExec(ctx, exec, cfg.LogOnly, rt, cfg, h, envStore.Pairs(), prompter, stdout, stderr)
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
	// disk when a bad file aborts the run, which the golden tests' filesystem
	// manifest would see as a divergence.
	host.CleanupPlaceholderFiles(mainHost)

	// The project env file and the built-ins complete the environment. This runs
	// before the worktree handling below, so a rejected file fails without first
	// asking the user about locks and taking them -- there is nothing to unwind.
	if code := completeEnv(ctx, envStore, h, cfg.NoProjectEnv, mainHost, stderr); code != 0 {
		return code
	}
	for _, pair := range envStore.DiagnosticPairs() {
		diagnostic.For(ctx, diagnostic.ComponentEnv).Debug("environment assignment resolved",
			diagnostic.Value("assignment", pair))
	}

	// The Zellij launch gate sits exactly where bash's does
	// (claude-contained:1439-1471): after the project env file has had its say,
	// so a rejected file still fails with exit 2 first, and before the second
	// placeholder sweep, the worktree prompt and every mkdir, so a refusal
	// leaves the host as it found it.
	zellijSession := ""
	if cfg.ZellijMode {
		var code int
		if zellijSession, code = zellijLaunchGate(ctx, rt, cfg, mainHost, stderr); code != 0 {
			return code
		}
	}

	mountedRoots := append([]string{mainHost}, extraMounts...)
	host.CleanupPlaceholderFiles(mountedRoots...)

	facts, err := probeFacts(ctx, rt, h, cfg, mainHost, extraMounts, extraModes, shareSkillsDir, mountedRoots)
	facts.Env = envStore.Pairs()
	facts.ZellijSession = zellijSession
	if err != nil {
		diagnostic.For(ctx, diagnostic.ComponentHost).Error("host and runtime facts probe failed",
			diagnostic.ErrorAttr(err))
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return cli.ExitFailure
	}
	diagnostic.For(ctx, diagnostic.ComponentHost).Info("project directory resolved",
		diagnostic.String("project_dir", facts.ProjectDir),
		diagnostic.Bool("worktree", facts.WorktreeMainRepo != ""),
	)

	// interrupts is installed here, immediately before the worktree-lock offer
	// and the mutations that follow it, mirroring bash's trap installation at
	// claude-contained:1560-1562 (which sits right before
	// maybe_offer_worktree_locks at :1563). signal.Notify, never
	// signal.Ignore: see the interrupts doc comment.
	ints := catchInterrupts()
	defer ints.stop()

	e := &executor{ctx: ctx, stdout: stdout, stderr: stderr}
	// cleanup_on_exit (claude-contained:1551-1554): locks first, then
	// placeholders. This also runs on every early return below -- including
	// the unknown-tool and --share-skills-conflict paths -- closing a gap the
	// previous version had: bash's EXIT trap sweeps placeholders on every
	// exit after :1555, but the inline call below only ran after a completed
	// container run.
	defer func() {
		e.releaseLocks()
		host.CleanupPlaceholderFiles(mountedRoots...)
	}()

	program, code := buildAndApply(e, cfg, h, facts, prof, prompter, ints)
	if code != 0 {
		return code
	}

	// A signal caught while building/applying the plan (e.g. at the
	// worktree-lock or node-modules prompt) wins over launching the container
	// at all, and skips the update check -- matching bash, where the trapped
	// signal would have already run `exit N` before the container line.
	if sig, ok := ints.pending(); ok {
		return signalExitCode(sig)
	}

	argvRun := rt.RenderRun(*program.Run)
	diagnostic.For(ctx, diagnostic.ComponentRuntime).Info("container runtime argv rendered",
		diagnostic.Value("argv", runtime.DiagnosticArgv(argvRun)))
	containerExit := exec(ctx, argvRun, stdin, stdout, stderr)

	// A signal that arrived during the run wins over the container's own
	// status, matching bash's 130/143/129, and skips the update check. Bash
	// defers the trapped signal until the foreground `container run` returns,
	// which is exactly what happens here too: the container shares this
	// process group and receives the signal directly, so the handler above
	// only has to stop *this* process from dying before the wait finishes.
	if sig, ok := awaitPendingSignal(ints, containerExit); ok {
		return signalExitCode(sig)
	}

	return containerExit
}

// executor applies plan steps. It holds the one piece of state that outlives
// the plan: the worktree locks, which cleanup must release after the
// container exits.
type executor struct {
	ctx                 context.Context
	stdout, stderr      io.Writer
	lockRepo, lockOwner string
	locked              []string
}

// releaseLocks releases this run's worktree auto-locks, if any were taken. A
// no-op when nothing was locked, mirroring cleanup_auto_worktree_locks'
// `[[ -n "$auto_worktree_lock_repo" && ${#auto_locked_worktrees[@]} -gt 0 ]]`
// guard.
func (e *executor) releaseLocks() {
	if e.lockRepo == "" || len(e.locked) == 0 {
		return
	}
	host.ReleaseWorktreeLocksContext(e.ctx, e.lockRepo, e.locked, e.lockOwner, e.stderr)
	e.locked = nil
}

// interrupts catches the termination signals bash traps
// (claude-contained:1560-1562) so the launcher can finish the container run
// and release its worktree locks before dying.
//
// signal.Notify, never signal.Ignore: an *ignored* disposition survives
// execve and would be inherited by the container runtime child, which would
// then not die on the signal at all -- the wait would never return and the
// locks would leak permanently. A *caught* disposition is reset to the
// default on exec, so the child dies normally. The handler deliberately does
// nothing; catching is only ever used to defer, never to act.
type interrupts struct {
	ch chan os.Signal
}

func catchInterrupts() *interrupts {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	return &interrupts{ch: ch}
}

func (i *interrupts) stop() {
	signal.Stop(i.ch)
}

// pending performs a non-blocking check for a signal already caught.
func (i *interrupts) pending() (os.Signal, bool) {
	select {
	case sig := <-i.ch:
		return sig, true
	default:
		return nil, false
	}
}

// c exposes the channel so a prompt read can select against it.
func (i *interrupts) c() <-chan os.Signal {
	return i.ch
}

// awaitPendingSignal is ints.pending() with one narrow allowance: `kill(2)`
// to a process group delivers to every member independently, with no
// ordering guarantee between them. When the container shares our process
// group (the common case: a real terminal signals the whole foreground
// group), the child can die from the very same signal we are catching before
// the Go runtime has finished delivering it to our channel -- so an
// immediate, non-blocking check can race and lose. exitCode == -1 is Go's
// unambiguous marker that the child was killed by a signal rather than
// exiting on its own (exec.ExitError.ExitCode's documented behavior), so only
// that case gets a short bounded poll; an ordinary exit returns instantly, as
// before.
func awaitPendingSignal(ints *interrupts, exitCode int) (os.Signal, bool) {
	if sig, ok := ints.pending(); ok {
		return sig, true
	}
	if exitCode != -1 {
		return nil, false
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
		if sig, ok := ints.pending(); ok {
			return sig, true
		}
	}
	return nil, false
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
func completeEnv(
	ctx context.Context, store *env.Store, h host.State,
	noProjectEnv bool, projectDir string, stderr io.Writer,
) int {
	if !noProjectEnv {
		path := filepath.Join(projectDir, env.FileName)

		// bash gates on `[[ -f "$file" ]]`, which is false for anything that is
		// not a regular file. A directory -- or a fifo, which would otherwise
		// block us forever in ReadFile -- is therefore a silent success, not an
		// error. Stat follows symlinks, as `-f` does.
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				diagnostic.For(ctx, diagnostic.ComponentEnv).Error("project env file read failed",
					diagnostic.String("path", path), diagnostic.ErrorAttr(err))
				// Regular but unreadable. bash's `done < "$file"` redirection
				// fails, the function returns non-zero, and its caller's
				// `|| exit 2` turns that into status 2 -- which we match.
				//
				// The *message* deliberately differs: bash emits its own
				// redirection diagnostic, which embeds the script's path and a
				// literal source line number ("claude-contained: line 274:").
				// Reproducing that would hardcode a line number in a file this
				// rewrite exists to delete.
				_, _ = fmt.Fprintf(stderr, "error: cannot read %s: %v\n", env.FileName, err)
				return cli.ExitUsage
			}
			if err := store.LoadFile(content); err != nil {
				diagnostic.For(ctx, diagnostic.ComponentEnv).Warn("project env file validation failed",
					diagnostic.String("path", path), diagnostic.ErrorAttr(err))
				_, _ = fmt.Fprintln(stderr, err.Error())
				return cli.ExitUsage
			}
		}
	}

	// Built-ins only fill gaps, so a user-supplied TZ replaces this one rather
	// than being emitted alongside it.
	if h.Timezone != "" {
		if err := store.Default("TZ="+h.Timezone, "host timezone", env.Builtin); err != nil {
			diagnostic.For(ctx, diagnostic.ComponentEnv).Warn("host timezone environment default failed",
				diagnostic.ErrorAttr(err))
			_, _ = fmt.Fprintln(stderr, err.Error())
			return cli.ExitUsage
		}
	}
	if h.GHToken != "" {
		if err := store.Default("GH_TOKEN="+h.GHToken, "AI_GH_TOKEN", env.Builtin); err != nil {
			diagnostic.For(ctx, diagnostic.ComponentEnv).Warn("GitHub token environment default failed",
				diagnostic.ErrorAttr(err))
			_, _ = fmt.Fprintln(stderr, err.Error())
			return cli.ExitUsage
		}
	}

	// Names only -- values routinely hold tokens and this lands in scrollback.
	if summary := store.Summary(); summary != "" {
		_, _ = fmt.Fprintln(stderr, summary)
	}
	return 0
}

// buildAndApply drives the resumable planner: build, apply the steps that are
// new since the last round, answer any pending prompt, repeat. Because Build is
// deterministic, the re-emitted prefix is identical and is skipped by index.
func buildAndApply(
	e *executor, cfg cli.Config, h host.State, facts plan.Facts, prof runtime.Profile,
	prompter *prompter, ints *interrupts,
) (plan.Program, int) {
	answers := plan.Answers{}
	appliedCount := 0

	for {
		program, err := plan.Build(cfg, h, facts, prof, answers)
		diagnostic.For(e.ctx, diagnostic.ComponentPlan).Debug("execution plan built",
			diagnostic.Value("summary", plan.Summarize(program, cfg)))

		// Steps are applied even when Build reported an error: bash discovers
		// an unknown tool only after these mutations have happened.
		n, applyErr := e.apply(program.Steps, appliedCount)
		appliedCount = n
		if applyErr != nil {
			diagnostic.For(e.ctx, diagnostic.ComponentPlan).Error("execution plan step failed",
				diagnostic.Int("index", appliedCount),
				diagnostic.String("step_kind", plan.DiagnosticStepKind(program.Steps[appliedCount])),
				diagnostic.ErrorAttr(applyErr))
			_, _ = fmt.Fprintf(e.stderr, "error: %v\n", applyErr)
			return program, cli.ExitFailure
		}

		if err != nil {
			var toolErr *plan.ToolError
			if errors.As(err, &toolErr) {
				_, _ = fmt.Fprintf(e.stderr, "Unknown tool: %s\n", toolErr.Tool)
				_, _ = fmt.Fprintln(e.stderr, "Supported tools: claude, codex, copilot, gemini, vibe")
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
					_, _ = fmt.Fprintln(e.stderr, line)
				}
				return program, cli.ExitUsage
			}
			_, _ = fmt.Fprintf(e.stderr, "error: %v\n", err)
			diagnostic.For(e.ctx, diagnostic.ComponentPlan).Error("execution plan build failed",
				diagnostic.ErrorAttr(err))
			return program, cli.ExitFailure
		}

		if program.Pending == nil {
			return program, 0
		}

		answer, ok := prompter.ask(program.Pending.Text, program.Pending.Default, ints)
		if !ok {
			// Either EOF (bash's `read` returns non-zero and `set -e` kills the
			// script on that line, with no prompt text printed) or a caught
			// signal aborted the read -- the caller distinguishes the two via
			// ints.pending().
			return program, cli.ExitFailure
		}
		answers[program.Pending.ID] = answer
	}
}

// apply performs the steps from index `from` onward and returns the new
// applied count.
func (e *executor) apply(steps []plan.Step, from int) (int, error) {
	for i := from; i < len(steps); i++ {
		switch s := steps[i].(type) {
		case plan.MkdirAll:
			// 0777 is masked by the process umask, exactly as `mkdir -p` does.
			// Hardcoding 0755 would diverge for any umask other than 022, and
			// the golden manifest records file modes.
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
			w := e.stdout
			if s.Stderr {
				w = e.stderr
			}
			_, _ = fmt.Fprintln(w, s.Text)
		case plan.WorktreeAutoLock:
			e.lockRepo = s.Repo
			e.lockOwner = s.Owner
			e.locked = host.LockWorktreesContext(e.ctx, s.Repo, s.Worktrees, s.Owner, e.stdout, e.stderr)
		}
		plan.RecordAppliedStep(e.ctx, i, steps[i])
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
			_, _ = fmt.Fprintln(stderr, "error: project directory cannot be read-only")
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

func isTerminal(stdin io.Reader) bool {
	if f, ok := stdin.(*os.File); ok {
		if info, err := f.Stat(); err == nil {
			return info.Mode()&os.ModeCharDevice != 0
		}
	}
	return false
}

func newPrompter(stdin io.Reader, terminal io.Writer, isTTY bool) *prompter {
	return &prompter{reader: bufio.NewReader(stdin), out: terminal, isTTY: isTTY}
}

// ask returns the answer and whether one could be read at all. ok=false means
// either EOF (bash turns that into an immediate exit) or, when ints is
// non-nil, a caught signal aborting the read -- the caller distinguishes the
// two via ints.pending().
//
// ints may be nil: the runtime-liveness confirm() call happens before the
// launcher installs its signal handling at all (bash's own traps are not
// installed that early either), so that read is a plain blocking one.
func (p *prompter) ask(text string, def bool, ints *interrupts) (bool, bool) {
	if p.isTTY {
		_, _ = fmt.Fprint(p.out, text)
	}

	if ints == nil {
		line, err := p.reader.ReadString('\n')
		return parseAnswer(line, err, def)
	}

	// The read runs in a goroutine so a caught signal can abort it: bash's
	// `read -p` is itself interrupted by a trapped signal, and without this a
	// signal.Notify'd Ctrl-C at a prompt would do nothing at all -- worse than
	// bash, since Notify has already taken over the default disposition. The
	// goroutine leaks only on the path where the process exits within
	// milliseconds of the signal, which is harmless.
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := p.reader.ReadString('\n')
		ch <- result{line, err}
	}()

	select {
	case r := <-ch:
		return parseAnswer(r.line, r.err, def)
	case <-ints.c():
		return false, false
	}
}

// parseAnswer mirrors bash's `read -p` result handling: an empty line (bare
// Enter) takes the default, and only the first character is matched --
// `yes`, `Y` and `yolo` all count as yes.
func parseAnswer(line string, err error, def bool) (bool, bool) {
	if err != nil && line == "" {
		return false, false
	}
	answer := strings.TrimRight(line, "\r\n")
	if answer == "" {
		return def, true
	}
	return answer[0] == 'y' || answer[0] == 'Y', true
}

// askLine reads one raw line, for prompts that are not yes/no. Like ask, the
// prompt goes to stderr and only when stdin is a terminal (bash prints a
// `read -p` prompt only when input comes from a terminal). Any read error --
// including EOF after a partial line -- is ok=false: bash's `read` returns
// non-zero on EOF regardless of what it managed to read, and `set -e` ends the
// script there without using the value.
func (p *prompter) askLine(text string) (string, bool) {
	if p.isTTY {
		_, _ = fmt.Fprint(p.out, text)
	}
	line, err := p.reader.ReadString('\n')
	if err != nil {
		return "", false
	}
	return line, true
}

func (p *prompter) confirm(text string) bool {
	answer, ok := p.ask(text, true, nil)
	return ok && answer
}
