package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"claude-contained/internal/cli"
	"claude-contained/internal/diagnostic"
	"claude-contained/internal/host"
	"claude-contained/internal/layer"
	"claude-contained/internal/runtime"
)

// Thresholds for the large-context warning. They are advisory and nothing more:
// exceeding them only decides whether the user is told why runs got slow, never
// whether a run proceeds, which is why their arbitrariness costs nothing.
//
// A hard limit was rejected. The layer directory is writable from inside the
// container, so a contained agent dropping node_modules there would, under a
// hard limit, permanently break its own project's launcher -- the only escape
// being --no-layer, i.e. a container that looks healthy while missing its
// toolchain, the exact outcome this feature exists to prevent. A legitimate
// layer vendoring a large tarball hits the same wall.
// Package variables only so a test can shrink them, the same reason
// dockerPollInterval is one: reaching the warning otherwise means writing ten
// thousand files in a test that is about a log line.
var (
	largeLayerFileCount   = 10_000
	largeLayerHashedBytes = int64(64 << 20)
	stageSuffix           = func() (string, error) {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", err
		}
		return hex.EncodeToString(raw[:]), nil
	}
)

// resolveLayerImage is the whole tooling-layer decision for one run: which
// image the container must start, or a failure that has already reported
// itself. It returns ("", 0) for "no layer, run the base image".
//
// The build goes through the same runner seam as the container run and the base
// rebuild, so a test can script a build failure without a container runtime.
// The image probes do not: they go through the Runtime seam and are answered in
// tests by the on-PATH stub binaries.
func resolveLayerImage(
	ctx context.Context, exec runner, rt runtime.Runtime, cfg cli.Config,
	src host.LayerSources, baseRef string, prompter *prompter,
	stdin io.Reader, stdout, stderr io.Writer,
) (string, int) {
	logger := diagnostic.For(ctx, diagnostic.ComponentLayer)

	if cfg.NoLayer {
		logger.Debug("tooling layer disabled by flag")
		return "", cli.ExitOK
	}

	// Resolving the layer directory stays *ahead* of every runtime probe, and
	// that ordering is what makes checklist item 12 hold: a project with no
	// layer returns here, after two stat calls, having never touched the
	// container runtime -- no probe, no prompt, no new argv, byte-identical to
	// before this feature existed.
	//
	// Swapping the two steps would not merely be untidy, it would break
	// silently in the tests' own favour: every runtime stub in this repository
	// ends in `*) exit 0 ;;`, so `image inspect` would exit 0 with no output,
	// which probeImageID classifies as a fault -- and every no-layer golden
	// would fail with a runtime error it has no business producing.
	dir, err := host.FindLayer(src)
	if err != nil {
		if errors.Is(err, host.ErrNoLayer) {
			path, reason := layerAbsence(src.ProjectDir)
			logger.Debug("tooling layer absent",
				diagnostic.String("path", path),
				diagnostic.String("reason", reason))
			return "", cli.ExitOK
		}
		logger.Warn("tooling layer directory resolution failed", diagnostic.ErrorAttr(err))
		return "", reportLayerError(err, stderr)
	}
	logger.Debug("tooling layer directory resolved",
		diagnostic.String("source", layerSource(src)),
		diagnostic.String("path", dir))

	// The base image is probed before the context is hashed: a hash taken
	// against an ID that does not exist names an image nobody can build, and
	// "run --rebuild=full first" is the message the user actually needs.
	base, present, err := rt.DescribeImage(ctx, baseRef)
	if err != nil {
		logger.Error("base image probe failed", diagnostic.ErrorAttr(err))
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return "", cli.ExitFailure
	}
	if !present {
		logger.Warn("base image absent", diagnostic.String("image", baseRef))
		_, _ = fmt.Fprintf(stderr, "error: the base image %s is not built.\n", baseRef)
		_, _ = fmt.Fprintf(stderr, "       Run '%s --rebuild=full' first; a tooling layer builds on top of it.\n",
			rt.Profile().Name)
		return "", cli.ExitFailure
	}
	logger.Debug("base image resolved",
		diagnostic.String("image", baseRef), diagnostic.Bool("present", present))

	id, err := layer.Resolve(dir, src.ProjectDir, base.Identity)
	if err != nil {
		logger.Error("tooling layer identity failed", diagnostic.ErrorAttr(err))
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return "", cli.ExitFailure
	}
	logger.Info("derived image identity computed",
		diagnostic.String("tag", id.Tag), diagnostic.Int("file_count", id.FileCount))
	if id.FileCount > largeLayerFileCount || id.HashedBytes > largeLayerHashedBytes {
		logger.Warn("tooling layer build context is large",
			diagnostic.Int("file_count", id.FileCount),
			diagnostic.Int64("hashed_bytes", id.HashedBytes))
	}

	// The derived image's own ID is discarded: nothing needs it. The same
	// method serves as the existence probe because both jobs need the same
	// command and the same absence semantics, and a separate existence probe
	// would be a second call that can disagree with the first.
	_, present, err = rt.DescribeImage(ctx, id.Tag)
	if err != nil {
		logger.Error("derived image probe failed", diagnostic.ErrorAttr(err))
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return "", cli.ExitFailure
	}
	if present {
		// The tag *is* the staleness check: it exists, so it was built from
		// this layer against this base, and there is nothing to do.
		logger.Info("derived image present", diagnostic.String("tag", id.Tag))
		return id.Tag, cli.ExitOK
	}

	if !cfg.BuildLayer {
		if !prompter.isTTY {
			logger.Warn("tooling layer build required without a terminal",
				diagnostic.String("tag", id.Tag))
			_, _ = fmt.Fprintln(stderr, "error: a tooling layer must be built and there is no terminal to confirm on.")
			_, _ = fmt.Fprintf(stderr, "       Pass %s to build it, or %s to run the base image.\n",
				cli.BuildLayerFlag, cli.NoLayerFlag)
			return "", cli.ExitFailure
		}
		// The context and the question are one string handed to ask, so both
		// land on the prompter's own terminal writer. Printing the context to
		// the stream-wrapped stderr and the question to the terminal would,
		// under --log-only, relocate the context and leave a bare [y/N] with
		// nothing to answer.
		//
		// The default is *no*, unlike every other prompt in the launcher. The
		// others offer reversible mutations of the user's own host state; this
		// one executes third-party build steps with unrestricted network
		// egress. Do not "fix" the inconsistency.
		//
		// ints is nil because signal handling is not installed yet at this
		// point in runWith (the same reason EnsureUp's confirm passes nil).
		// Consequence worth knowing: Ctrl-C here kills the process outright
		// rather than being caught, unlike every prompt further down. Correct
		// here -- nothing is held yet -- but it differs from its neighbours.
		question := fmt.Sprintf(
			"Tooling layer found: %s\n"+
				"It has not been built for the current base image. Building runs its\n"+
				"instructions on this host with unrestricted network access.\n"+
				"Build the tooling layer for this project? [y/N] ", id.Dockerfile)
		answer, ok := prompter.ask(question, false, nil)
		if !ok || !answer {
			logger.Warn("tooling layer build confirmation declined", diagnostic.String("tag", id.Tag))
			_, _ = fmt.Fprintln(stdout, "Aborted.")
			_, _ = fmt.Fprintf(stderr, "       Use %s to run the base image without the tooling layer.\n",
				cli.NoLayerFlag)
			return "", cli.ExitFailure
		}
	}

	build := func(tag string) int {
		// Identity names the derived image; BuildRef is the builder input. They
		// differ on Apple Containers, whose local manifest digest cannot always
		// be resolved by FROM.
		spec := runtime.BuildSpec{
			Tag:       tag,
			Context:   id.Dir,
			BuildArgs: []string{layer.BaseImageArg + "=" + base.BuildRef},
			Labels: []runtime.LabelArg{
				{Key: layer.LabelLayer, Value: "1"},
				{Key: layer.LabelProject, Value: src.ProjectDir},
				{Key: layer.LabelDockerfile, Value: id.Dockerfile},
				{Key: layer.LabelBase, Value: base.Identity},
			},
		}
		argv := rt.RenderBuild(spec)
		started := time.Now()
		logger.Info("derived image build started", diagnostic.String("tag", tag), diagnostic.Value("argv", runtime.DiagnosticArgv(argv)))
		if code := exec(ctx, argv, stdin, stdout, stderr); code != 0 {
			logger.Warn("derived image build failed", diagnostic.String("tag", tag), diagnostic.Duration("duration", time.Since(started)), diagnostic.Int("exit_status", code))
			_, _ = fmt.Fprintf(stderr, "error: the tooling layer build failed (exit %d).\n", code)
			_, _ = fmt.Fprintf(stderr, "       Fix %s and retry; the base image was not used as a fallback.\n", id.Dockerfile)
			return cli.ExitFailure
		}
		return cli.ExitOK
	}

	if base.BuildRefImmutable {
		if code := build(id.Tag); code != cli.ExitOK {
			return "", code
		}
		return id.Tag, cli.ExitOK
	}

	suffix, err := stageSuffix()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: could not generate a tooling-layer staging tag: %v\n", err)
		return "", cli.ExitFailure
	}
	stage := id.Tag + "-stage-" + suffix
	pre, ok, probeErr := rt.DescribeImage(ctx, baseRef)
	if probeErr != nil {
		_, _ = fmt.Fprintf(stderr, "error: could not verify the base image before building the tooling layer: %v\n", probeErr)
		return "", cli.ExitFailure
	}
	if !ok || pre.Identity != base.Identity {
		_, _ = fmt.Fprintln(stderr, "error: the base image changed before the tooling layer could be built; retry.")
		return "", cli.ExitFailure
	}

	buildCode := build(stage)
	cleanup := func(primary bool) {
		if code := exec(ctx, rt.RenderRemove(stage), stdin, stdout, stderr); code != 0 {
			// Docker can still reject an absent reference despite --force. A failed
			// removal is harmless only when a fresh runtime probe establishes that
			// this exact stage is already absent (for example after concurrent cleanup).
			_, present, probeErr := rt.DescribeImage(ctx, stage)
			if probeErr == nil && !present {
				return
			}
			if primary {
				_, _ = fmt.Fprintf(stderr, "warning: could not remove tooling-layer staging image %s (exit %d).\n", stage, code)
			} else {
				_, _ = fmt.Fprintf(stderr, "warning: tooling-layer staging image %s remains (exit %d).\n", stage, code)
			}
		}
	}
	if buildCode != cli.ExitOK {
		cleanup(true)
		return "", buildCode
	}
	post, ok, probeErr := rt.DescribeImage(ctx, baseRef)
	if probeErr != nil {
		_, _ = fmt.Fprintf(stderr, "error: could not verify the base image after building the tooling layer: %v\n", probeErr)
		cleanup(true)
		return "", cli.ExitFailure
	}
	if !ok || post.Identity != base.Identity {
		_, _ = fmt.Fprintln(stderr, "error: the base image changed while the tooling layer was building; retry.")
		cleanup(true)
		return "", cli.ExitFailure
	}
	if code := exec(ctx, rt.RenderTag(stage, id.Tag), stdin, stdout, stderr); code != 0 {
		_, _ = fmt.Fprintf(stderr, "error: could not promote the tooling-layer staging image (exit %d).\n", code)
		cleanup(true)
		return "", cli.ExitFailure
	}
	cleanup(false)
	return id.Tag, cli.ExitOK
}

// layerSource names which of FindLayer's inputs supplied the directory, for the
// diagnostic record. It reproduces FindLayer's precedence rather than being
// reported by it, because an explicit source either returns a directory or
// fails outright -- there is no case where the two could disagree.
func layerSource(src host.LayerSources) string {
	switch {
	case src.Flag != "":
		return "flag"
	case src.Env != "":
		return "environment"
	default:
		return "default"
	}
}

// layerAbsence describes *why* the default directory yielded no layer. The spec
// makes both cases silent on stderr -- adding user-facing output to a path the
// ticket requires to stay byte-identical is not available -- so this exists so
// that a user who created .claude-contained/layer/ and forgot the Dockerfile
// can find out at --log-level=debug instead of guessing.
func layerAbsence(projectDir string) (path, reason string) {
	if projectDir == "" {
		return "", "missing"
	}
	path = filepath.Join(projectDir, host.LayerDirName)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path, "no-dockerfile"
	}
	return path, "missing"
}

// reportLayerError formats the one way resolution fails, structurally identical
// to reportBuildContextError but without the program-name argument: this
// message does not name the program.
func reportLayerError(err error, stderr io.Writer) int {
	var bad *host.BadLayerError
	if errors.As(err, &bad) {
		source := host.LayerEnvVar
		if bad.FromFlag {
			source = cli.LayerFlag
		}
		_, _ = fmt.Fprintf(stderr, "error: %s has no Dockerfile: %s\n", source, bad.Dir)
		return cli.ExitUsage
	}
	_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
	return cli.ExitFailure
}
