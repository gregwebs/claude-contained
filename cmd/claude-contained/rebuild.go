package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"claude-contained/internal/cli"
	"claude-contained/internal/diagnostic"
	"claude-contained/internal/host"
	"claude-contained/internal/plan"
	"claude-contained/internal/runtime"
)

// Rebuild modes. "none" is the sentinel for "no rebuild requested" and is also a
// value a user can type: `-R none` therefore starts a normal session, exactly as
// bash's `[[ "$rebuild_mode" != "none" ]]` guard does (claude-contained:893).
const (
	rebuildNone  = "none"
	rebuildTools = "tools"
	rebuildFull  = "full"
)

// cacheBustToken is the AI_TOOLS_CACHE_BUST value: `date -u +%Y%m%d%H%M%S`
// (claude-contained:538). The clock is host state rather than a fresh read, so
// the token is one deterministic input in tests; the golden normalizer
// neutralizes the field regardless, to `<TOKEN>` (N6 in goldenfixture_test.go).
const cacheBustToken = "20060102150405"

// rebuildAttempt is one build plus the stderr lines printed immediately before
// it. Spec.Context is left empty here: the attempts are a pure function of the
// mode and the clock, and the caller fills the context in once it is resolved.
type rebuildAttempt struct {
	Before []string
	Spec   runtime.BuildSpec
}

// rebuildAttempts maps a rebuild mode to the builds to try, in order, or
// ok=false for an unknown mode (run_rebuild's `*)` arm, claude-contained:548).
//
// A failed tools refresh retrying as a full rebuild (:540-541) is expressed as a
// second attempt rather than bash's recursive call, which keeps the whole
// mode/retry/message contract assertable in one table test without running a
// build. The retry notice leads the second attempt's Before lines, which is
// exactly bash's stderr order.
func rebuildAttempts(mode string, now time.Time) ([]rebuildAttempt, bool) {
	full := rebuildAttempt{
		Before: []string{"Rebuilding claude-contained image (full fresh rebuild)..."},
		Spec:   runtime.BuildSpec{Tag: plan.Image, Pull: true, NoCache: true},
	}

	switch mode {
	case rebuildTools:
		tools := rebuildAttempt{
			Before: []string{"Rebuilding claude-contained image (AI tools refresh)..."},
			Spec: runtime.BuildSpec{
				Tag:       plan.Image,
				BuildArgs: []string{"AI_TOOLS_CACHE_BUST=" + now.UTC().Format(cacheBustToken)},
			},
		}
		retry := full
		retry.Before = append([]string{"AI tools refresh failed. Retrying with full rebuild..."}, full.Before...)
		return []rebuildAttempt{tools, retry}, true
	case rebuildFull:
		return []rebuildAttempt{full}, true
	default:
		return nil, false
	}
}

// runRebuild rebuilds the image and returns the exit status. It never starts a
// session: bash rebuilds and exits (claude-contained:892-896), which is why this
// runs before the project directory is resolved, before any placeholder sweep,
// before the env file, and before any lock, mutex, signal handler or deferred
// cleanup exists.
//
// The build goes through the same runner seam as the container run, so a test can
// script build failures -- including the tools-refresh retry -- without a
// container runtime.
func runRebuild(
	ctx context.Context, exec runner, rt runtime.Runtime, mode string,
	src host.BuildContextSources, now time.Time,
	stdin io.Reader, stdout, stderr io.Writer,
) int {
	// The mode is checked before the build context, where bash checks it after
	// (claude-contained:530 vs :536). The two failures are independent and a
	// typo'd mode deserves to be named; the difference is observable only when
	// both are wrong, and every corpus entry resolves a context.
	attempts, ok := rebuildAttempts(mode, now)
	if !ok {
		// Verbatim, prefix-free: the corpus compares stderr byte for byte
		// against claude-contained:549-550.
		_, _ = fmt.Fprintf(stderr, "Unknown rebuild mode: %s\n", mode)
		_, _ = fmt.Fprintf(stderr, "Supported rebuild modes: %s, %s\n", rebuildTools, rebuildFull)
		return cli.ExitFailure
	}

	buildContext, err := host.FindBuildContext(src)
	if err != nil {
		diagnostic.For(ctx, diagnostic.ComponentRebuild).Error("build context resolution failed",
			diagnostic.ErrorAttr(err))
		return reportBuildContextError(err, rt.Profile().Name, stderr)
	}

	for i, attempt := range attempts {
		for _, line := range attempt.Before {
			_, _ = fmt.Fprintln(stderr, line)
		}
		attempt.Spec.Context = buildContext
		argv := rt.RenderBuild(attempt.Spec)
		started := time.Now()
		diagnostic.For(ctx, diagnostic.ComponentRebuild).Info("image rebuild attempt started",
			diagnostic.Int("attempt", i+1), diagnostic.Value("argv", runtime.DiagnosticArgv(argv)))
		if code := exec(ctx, argv, stdin, stdout, stderr); code == 0 {
			diagnostic.For(ctx, diagnostic.ComponentRebuild).Info("image rebuild attempt completed",
				diagnostic.Int("attempt", i+1), diagnostic.Duration("duration", time.Since(started)),
				diagnostic.Int("exit_status", code))
			return cli.ExitOK
		} else if i == len(attempts)-1 {
			diagnostic.For(ctx, diagnostic.ComponentRebuild).Warn("image rebuild attempt failed",
				diagnostic.Int("attempt", i+1), diagnostic.Duration("duration", time.Since(started)),
				diagnostic.Int("exit_status", code))
			// bash runs under `set -e`, so the last build's status is the
			// script's (claude-contained:546).
			return code
		} else {
			diagnostic.For(ctx, diagnostic.ComponentRebuild).Warn("image rebuild attempt failed; retrying",
				diagnostic.Int("attempt", i+1), diagnostic.Duration("duration", time.Since(started)),
				diagnostic.Int("exit_status", code))
		}
	}
	return cli.ExitFailure // unreachable: the loop returns on the last attempt.
}

// reportBuildContextError formats the two ways resolution fails. Both messages
// are Go-only -- bash has neither source and its own wording names only the
// symlink install -- so neither is compared against the oracle.
func reportBuildContextError(err error, progName string, stderr io.Writer) int {
	var bad *host.BadBuildContextError
	if errors.As(err, &bad) {
		source := host.BuildContextEnvVar
		if bad.FromFlag {
			source = cli.BuildContextFlag
		}
		_, _ = fmt.Fprintf(stderr, "error: %s has no Dockerfile: %s\n", source, bad.Dir)
		return cli.ExitUsage
	}
	_, _ = fmt.Fprintln(stderr, "error: rebuild cannot find the checkout that holds the Dockerfile.")
	_, _ = fmt.Fprintf(stderr, "       Pass %s DIR, set %s=DIR, or install\n", cli.BuildContextFlag, host.BuildContextEnvVar)
	_, _ = fmt.Fprintf(stderr, "       %s inside the checkout (a symlink into it counts).\n", progName)
	return cli.ExitFailure
}

// selfPath is where this executable actually lives. os.Executable, not argv[0]:
// see host.BuildContextSources.Self. argv[0] is the fallback only for the rare
// host where os.Executable fails, which is no worse than what bash would do.
func selfPath(argv0 string) string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return argv0
}
