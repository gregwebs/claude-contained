package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-contained/internal/cli"
	"claude-contained/internal/host"
	"claude-contained/internal/layer"
	"claude-contained/internal/plan"
	"claude-contained/internal/runtime"
)

const (
	layerTestBaseID     = "sha256:base00"
	layerTestDerivedID  = "sha256:derived00"
	layerTestDockerfile = "ARG BASE_IMAGE=claude-contained:latest\n" +
		"FROM ${BASE_IMAGE}\n" +
		"RUN echo layer-marker > /usr/local/share/layer-marker\n"
)

// layerFixture is one driver test's isolated world: a stubbed HOME and PATH, a
// project directory with a fixed basename (the derived tag embeds it), and the
// two files the stub `image` arm is driven by.
type layerFixture struct {
	project  string
	layerDir string
	idDir    string
	imageLog string
}

// newLayerFixture builds the world. withLayer decides whether the project
// checks in a tooling layer at the default location.
func newLayerFixture(t *testing.T, withLayer bool) layerFixture {
	t.Helper()
	base := host.ResolvePath(t.TempDir())
	fx := layerFixture{
		project:  filepath.Join(base, "project"),
		layerDir: filepath.Join(base, "project", host.LayerDirName),
		idDir:    filepath.Join(base, "image-ids"),
		imageLog: filepath.Join(base, "image-probes.log"),
	}
	if err := os.MkdirAll(fx.project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fx.idDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if withLayer {
		if err := os.MkdirAll(fx.layerDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fx.layerDir, "Dockerfile"), []byte(layerTestDockerfile), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	withStubbedHostAndPath(t)
	// A developer's own exported layer directory must not reach any of these
	// results, the same reason the shell suites unset it. The runtime selection
	// is pinned for the same reason: these tests choose their runtime through
	// the injected platform, and an ambient CLAUDE_CONTAINED_RUNTIME=apple would
	// make the Docker cases exit 2 on a Linux host.
	t.Setenv(host.LayerEnvVar, "")
	t.Setenv("CLAUDE_CONTAINED_RUNTIME", "")
	t.Setenv("STUB_IMAGE_ID_DIR", fx.idDir)
	t.Setenv("STUB_IMAGE_LOG", fx.imageLog)
	return fx
}

// setImageID presents ref as a locally present image carrying id. A reference
// never passed here is absent from the stub's image store.
func (fx layerFixture) setImageID(t *testing.T, ref, id string) {
	t.Helper()
	name := strings.NewReplacer(":", "_", "/", "_").Replace(ref) + ".id"
	if err := os.WriteFile(filepath.Join(fx.idDir, name), []byte(id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// derivedTag is the tag the launcher will compute for this fixture's layer
// against baseID. Computed here rather than hardcoded because it is a content
// hash; the point of a test knowing it is to present the image as *already
// built*, which no literal could do.
func (fx layerFixture) derivedTag(t *testing.T, baseID string) string {
	t.Helper()
	id, err := layer.Resolve(fx.layerDir, fx.project, baseID)
	if err != nil {
		t.Fatalf("resolving the fixture layer: %v", err)
	}
	return id.Tag
}

// imageProbes reports every `image ...` invocation the stub saw. An absent log
// file means none, which is the assertion a no-layer run needs.
func (fx layerFixture) imageProbes(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(fx.imageLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// forceTerminal makes the launcher believe stdin is a terminal, so a test can
// reach the build confirmation rather than the fails-closed branch.
func forceTerminal(t *testing.T) {
	t.Helper()
	orig := isTerminal
	isTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { isTerminal = orig })
}

func layerArgv(project string, extra ...string) []string {
	return append([]string{"claude-contained", "-N", "-s", "-C", project}, extra...)
}

// The §1.5 ordering property, and checklist item 12's mechanism: a project with
// no tooling layer resolves the directory, finds nothing, and returns without
// ever asking the runtime about an image.
func TestNoLayerNeverProbesAnImage(t *testing.T) {
	fx := newLayerFixture(t, false)
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin, layerArgv(fx.project), strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("recorded %d runner calls, want exactly the container run: %#v", len(*calls), *calls)
	}
	if (*calls)[0][1] != "run" {
		t.Errorf("argv[1] = %q, want run", (*calls)[0][1])
	}
	if probes := fx.imageProbes(t); len(probes) != 0 {
		t.Errorf("a project with no layer must not probe any image, saw: %v", probes)
	}
	if !contains(t, (*calls)[0], plan.Image) {
		t.Errorf("run argv must carry the base image %q: %v", plan.Image, (*calls)[0])
	}
}

func TestLayerBuildsAndRunsTheDerivedImage(t *testing.T) {
	fx := newLayerFixture(t, true)
	fx.setImageID(t, plan.Image, layerTestBaseID)
	tag := fx.derivedTag(t, layerTestBaseID)
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin, layerArgv(fx.project, "--build-layer"),
		strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if len(*calls) != 2 {
		t.Fatalf("recorded %d runner calls, want a build then a run: %#v", len(*calls), *calls)
	}

	build := (*calls)[0]
	if build[1] != "build" {
		t.Errorf("first call argv[1] = %q, want build", build[1])
	}
	for _, want := range []string{
		"--build-arg", layer.BaseImageArg + "=" + layerTestBaseID,
		"-t", tag,
		fx.layerDir,
	} {
		if !contains(t, build, want) {
			t.Errorf("build argv missing %q: %v", want, build)
		}
	}

	runCall := (*calls)[1]
	if runCall[1] != "run" {
		t.Errorf("second call argv[1] = %q, want run", runCall[1])
	}
	if !contains(t, runCall, tag) {
		t.Errorf("run argv must carry the derived tag %q: %v", tag, runCall)
	}
	if contains(t, runCall, plan.Image) {
		t.Errorf("run argv must not carry the base image once a layer resolved: %v", runCall)
	}
}

// Labels are Docker-only, chosen before the risk could fire rather than
// discovered after it did. Same fixture, both runtimes.
func TestLayerBuildLabelsAreDockerOnly(t *testing.T) {
	cases := []struct {
		name       string
		plat       runtime.Platform
		wantBin    string
		wantLabels bool
	}{
		{"apple", runtime.Darwin, "container", false},
		{"docker", runtime.Linux, "docker", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newLayerFixture(t, true)
			fx.setImageID(t, plan.Image, layerTestBaseID)
			calls, run := recordingRunner(0)
			var stdout, stderr bytes.Buffer

			code := runWith(run, tc.plat, layerArgv(fx.project, "--build-layer"),
				strings.NewReader(""), &stdout, &stderr)
			if code != cli.ExitOK {
				t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
			}

			build := (*calls)[0]
			if build[0] != tc.wantBin {
				t.Fatalf("argv[0] = %q, want %q", build[0], tc.wantBin)
			}
			gotLabels := contains(t, build, "--label")
			if gotLabels != tc.wantLabels {
				t.Errorf("--label present = %v, want %v: %v", gotLabels, tc.wantLabels, build)
			}
			if tc.wantLabels && !contains(t, build, layer.LabelLayer+"=1") {
				t.Errorf("build argv missing the layer marker label: %v", build)
			}
		})
	}
}

func TestDerivedImageAlreadyPresentSkipsTheBuild(t *testing.T) {
	fx := newLayerFixture(t, true)
	fx.setImageID(t, plan.Image, layerTestBaseID)
	tag := fx.derivedTag(t, layerTestBaseID)
	fx.setImageID(t, tag, layerTestDerivedID)
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin, layerArgv(fx.project), strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("recorded %d runner calls, want only the run: %#v", len(*calls), *calls)
	}
	if !contains(t, (*calls)[0], tag) {
		t.Errorf("run argv must carry the derived tag %q: %v", tag, (*calls)[0])
	}
	if strings.Contains(stderr.String(), "[y/N]") {
		t.Errorf("an already-built layer must not prompt:\n%s", stderr.String())
	}
}

func TestUnbuiltLayerWithoutATerminalFailsClosed(t *testing.T) {
	fx := newLayerFixture(t, true)
	fx.setImageID(t, plan.Image, layerTestBaseID)
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin, layerArgv(fx.project), strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitFailure {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", code, cli.ExitFailure, stderr.String())
	}
	if len(*calls) != 0 {
		t.Errorf("nothing may be built or run: %#v", *calls)
	}
	for _, want := range []string{cli.BuildLayerFlag, cli.NoLayerFlag} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr must name %s:\n%s", want, stderr.String())
		}
	}
}

func TestNoLayerFlagRunsTheBaseImage(t *testing.T) {
	fx := newLayerFixture(t, true)
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin, layerArgv(fx.project, "--no-layer"),
		strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("recorded %d runner calls, want only the run: %#v", len(*calls), *calls)
	}
	if !contains(t, (*calls)[0], plan.Image) {
		t.Errorf("run argv must carry the base image: %v", (*calls)[0])
	}
	if probes := fx.imageProbes(t); len(probes) != 0 {
		t.Errorf("--no-layer must not touch the filesystem or the runtime, saw: %v", probes)
	}
}

func TestLayerBuildConfirmationAccepted(t *testing.T) {
	fx := newLayerFixture(t, true)
	fx.setImageID(t, plan.Image, layerTestBaseID)
	forceTerminal(t)
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin, layerArgv(fx.project), strings.NewReader("y\n"), &stdout, &stderr)

	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if len(*calls) != 2 {
		t.Fatalf("recorded %d runner calls, want a build then a run: %#v", len(*calls), *calls)
	}
	// The context and the question are one string on one stream, so a
	// --log-only run cannot relocate the context and leave a bare [y/N].
	out := stderr.String()
	for _, want := range []string{"Tooling layer found:", "unrestricted network access", "[y/N]"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt is missing %q:\n%s", want, out)
		}
	}
}

func TestLayerBuildConfirmationDeclined(t *testing.T) {
	fx := newLayerFixture(t, true)
	fx.setImageID(t, plan.Image, layerTestBaseID)
	forceTerminal(t)
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin, layerArgv(fx.project), strings.NewReader("n\n"), &stdout, &stderr)

	if code != cli.ExitFailure {
		t.Fatalf("exit = %d, want %d", code, cli.ExitFailure)
	}
	if len(*calls) != 0 {
		t.Errorf("declining must build and run nothing: %#v", *calls)
	}
	if !strings.Contains(stdout.String(), "Aborted.") {
		t.Errorf("stdout must carry Aborted.:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), cli.NoLayerFlag) {
		t.Errorf("stderr must name %s:\n%s", cli.NoLayerFlag, stderr.String())
	}
}

// A failed layer build is a hard error carrying the builder's own status. It
// never falls back to the base image: a fallback starts a container that looks
// healthy while its toolchain is missing.
func TestFailedLayerBuildNeverFallsBackToTheBaseImage(t *testing.T) {
	fx := newLayerFixture(t, true)
	fx.setImageID(t, plan.Image, layerTestBaseID)
	calls, run := recordingRunner(3)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin, layerArgv(fx.project, "--build-layer"),
		strings.NewReader(""), &stdout, &stderr)

	if code != 3 {
		t.Fatalf("exit = %d, want the builder's own status 3\nstderr:\n%s", code, stderr.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("recorded %d runner calls, want only the failed build: %#v", len(*calls), *calls)
	}
	if (*calls)[0][1] != "build" {
		t.Errorf("the only call must be the build: %v", (*calls)[0])
	}
	if !strings.Contains(stderr.String(), "was not used as a fallback") {
		t.Errorf("stderr must say the base image was not used as a fallback:\n%s", stderr.String())
	}
}

func TestMissingBaseImageIsReportedRatherThanBuilt(t *testing.T) {
	fx := newLayerFixture(t, true)
	// No id file for plan.Image, and --help exits 0: a genuine absence.
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin, layerArgv(fx.project, "--build-layer"),
		strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitFailure {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", code, cli.ExitFailure, stderr.String())
	}
	if len(*calls) != 0 {
		t.Errorf("a missing base image must not be built implicitly: %#v", *calls)
	}
	if !strings.Contains(stderr.String(), "--rebuild=full") {
		t.Errorf("stderr must point at --rebuild=full:\n%s", stderr.String())
	}
}

// The failure mode the capability probe exists to prevent: a runtime CLI that
// does not have `image inspect` must produce a named fault, never the
// unhelpable "the base image is not built" on a machine where it is right
// there.
func TestBaseProbeFaultIsNotReportedAsAMissingBaseImage(t *testing.T) {
	fx := newLayerFixture(t, true)
	t.Setenv("STUB_IMAGE_HELP_EXIT", "64")
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin, layerArgv(fx.project, "--build-layer"),
		strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitFailure {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", code, cli.ExitFailure, stderr.String())
	}
	if len(*calls) != 0 {
		t.Errorf("nothing may be built: %#v", *calls)
	}
	out := stderr.String()
	for _, want := range []string{"container", "image inspect"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr must name %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "is not built") {
		t.Errorf("a probe fault must not be reported as a missing base image:\n%s", out)
	}
}

// The size guard warns and never refuses: the layer directory is writable from
// inside the container, so a refusal would let a contained agent disable its
// own project's toolchain.
func TestOversizedContextWarnsAndProceeds(t *testing.T) {
	fx := newLayerFixture(t, true)
	fx.setImageID(t, plan.Image, layerTestBaseID)

	origCount := largeLayerFileCount
	largeLayerFileCount = 0
	t.Cleanup(func() { largeLayerFileCount = origCount })

	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin,
		layerArgv(fx.project, "--build-layer", "--log-level=warn"),
		strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want 0: an oversized context is a warning, not a refusal\nstderr:\n%s",
			code, stderr.String())
	}
	if len(*calls) != 2 {
		t.Fatalf("recorded %d runner calls, want a build then a run: %#v", len(*calls), *calls)
	}
	if !strings.Contains(stderr.String(), "tooling layer build context is large") {
		t.Errorf("stderr must carry the warning record:\n%s", stderr.String())
	}
}

func TestNamedLayerDirectoryWithoutADockerfileIsAUsageError(t *testing.T) {
	fx := newLayerFixture(t, false)
	tools := filepath.Join(fx.project, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin, layerArgv(fx.project, "--layer", tools),
		strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitUsage {
		t.Fatalf("exit = %d, want %d\nstderr:\n%s", code, cli.ExitUsage, stderr.String())
	}
	if len(*calls) != 0 {
		t.Errorf("nothing may run: %#v", *calls)
	}
	want := "error: " + cli.LayerFlag + " has no Dockerfile: " + tools + "\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestEnvironmentLayerDirectoryWithoutADockerfileNamesTheVariable(t *testing.T) {
	fx := newLayerFixture(t, false)
	tools := filepath.Join(fx.project, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(host.LayerEnvVar, tools)
	_, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin, layerArgv(fx.project), strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitUsage {
		t.Fatalf("exit = %d, want %d", code, cli.ExitUsage)
	}
	want := "error: " + host.LayerEnvVar + " has no Dockerfile: " + tools + "\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

// The layer flags are inert with --attach and --rebuild rather than refused:
// both dispatches return above the layer step, so the layer code never runs.
// Unlike --name with --attach, an unused flag here is not misleading.
func TestLayerFlagsAreInertWithAttach(t *testing.T) {
	fx := newLayerFixture(t, true)
	t.Setenv("STUB_LIST", "aic-live")

	orig := replaceProcess
	called := false
	replaceProcess = func([]string) error {
		called = true
		return nil
	}
	t.Cleanup(func() { replaceProcess = orig })

	var stdout, stderr bytes.Buffer
	code := runWith(failRunner(t), runtime.Darwin,
		[]string{"claude-contained", "-a", "live", "--layer", "/definitely/does/not/exist", "-C", fx.project},
		strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if !called {
		t.Fatal("the attach path must still run")
	}
	if probes := fx.imageProbes(t); len(probes) != 0 {
		t.Errorf("attach must not reach the layer step, saw: %v", probes)
	}
}

func TestLayerFlagsAreInertWithRebuild(t *testing.T) {
	fx := newLayerFixture(t, true)
	buildCtx := filepath.Join(fx.project, "buildctx")
	if err := os.MkdirAll(buildCtx, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildCtx, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer

	code := runWith(run, runtime.Darwin,
		[]string{"claude-contained", "--rebuild=full", "--build-context", buildCtx, "--layer", "/definitely/does/not/exist"},
		strings.NewReader(""), &stdout, &stderr)

	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("recorded %d runner calls, want the base rebuild alone: %#v", len(*calls), *calls)
	}
	if !contains(t, (*calls)[0], plan.Image) {
		t.Errorf("--rebuild builds the base image, never a layer: %v", (*calls)[0])
	}
	if probes := fx.imageProbes(t); len(probes) != 0 {
		t.Errorf("--rebuild must not reach the layer step, saw: %v", probes)
	}
}

// Checklist item 6, end to end: the base image's identity is a hash input, so
// rebuilding the base renames every derived image with no flag involved.
func TestChangingTheBaseImageIDForcesARebuild(t *testing.T) {
	fx := newLayerFixture(t, true)
	fx.setImageID(t, plan.Image, "sha256:aaaa")
	firstTag := fx.derivedTag(t, "sha256:aaaa")
	fx.setImageID(t, firstTag, layerTestDerivedID)

	calls, run := recordingRunner(0)
	var stdout, stderr bytes.Buffer
	if code := runWith(run, runtime.Darwin, layerArgv(fx.project),
		strings.NewReader(""), &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("first run exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if len(*calls) != 1 {
		t.Fatalf("the first run must not build: %#v", *calls)
	}

	// The base image is rebuilt: same tag, new identity.
	fx.setImageID(t, plan.Image, "sha256:bbbb")
	secondTag := fx.derivedTag(t, "sha256:bbbb")
	if secondTag == firstTag {
		t.Fatal("the derived tag must change when the base image's ID does")
	}

	calls2, run2 := recordingRunner(0)
	stdout.Reset()
	stderr.Reset()
	if code := runWith(run2, runtime.Darwin, layerArgv(fx.project, "--build-layer"),
		strings.NewReader(""), &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("second run exit = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if len(*calls2) != 2 {
		t.Fatalf("the second run must rebuild: %#v", *calls2)
	}
	if !contains(t, (*calls2)[0], secondTag) {
		t.Errorf("the rebuild must target the new tag %q: %v", secondTag, (*calls2)[0])
	}
}
