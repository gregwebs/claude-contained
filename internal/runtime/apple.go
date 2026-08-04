package runtime

import (
	"context"
	_ "embed"
	"errors"
	"io"
	"os/exec"

	"claude-contained/internal/diagnostic"
)

// appleHelp is the source of truth for Apple Containers' --help text; changes
// go through help_contained.txt and the assertions in runtime_test.go, not a
// diff against a retired bash script -- see docs/adr/0004-go-launcher-rewrite.md.
// A comment cannot live inside the .txt, which is printed verbatim.
//
//go:embed help_contained.txt
var appleHelp string

// ErrAborted reports that the user declined to start the container runtime.
var ErrAborted = errors.New("aborted")

// Apple drives Apple Containers via the `container` CLI.
type Apple struct{ platform Platform }

// appleDigestBuildRefSupported is deliberately false until a version-specific
// live probe proves that a local name@digest is resolved without registry I/O.
// It is injectable for the synthetic capability test; unverified versions use
// the mutable local tag guarded by the launcher.
var appleDigestBuildRefSupported = func(context.Context, string, string) bool { return false }

// NewApple takes the platform explicitly; there is no zero-argument form,
// because it would silently construct the unnamed-platform behavior in a caller
// that meant to say Darwin.
func NewApple(p Platform) *Apple { return &Apple{platform: p} }

func (a *Apple) Profile() Profile {
	return Profile{
		Name: ProgName,
		// Apple Containers points the container at the vmnet gateway
		// (192.168.64.1) for DNS, which is frequently unreachable, so a public
		// resolver is the default rather than an extra flag the user must know.
		DefaultDNS:       []string{"1.1.1.1"},
		NotRunningPrompt: "Container system is not running. Start it? [Y/n] ",
		Help:             appleHelp,
		// Apple Containers routes the container through the vmnet gateway, which
		// cannot reach a host service bound to 127.0.0.1 (apple/container#346).
		// Kept byte-identical to claude-contained's own lines, because the golden
		// tests compare stderr.
		HostForwardNotice: []string{
			"Warning: Apple Containers cannot reach host services bound only to 127.0.0.1.",
			"         -H reaches host services listening on 0.0.0.0; use Docker for the rest.",
		},
		// Apple Containers applies binds sequentially into the guest and cannot
		// create a nested mount's destination under a read-only parent, so the
		// --share-skills Codex system-skills remount needs a preexisting
		// mountpoint. See the Profile field's own comment.
		ReadonlyRemountNeedsExistingMountpoint: true,
	}
}

func (a *Apple) Bin() string { return "container" }

func (a *Apple) RenderRun(spec RunSpec) []string {
	argv := []string{a.Bin(), "run", "--rm", "-it"}

	for _, arg := range spec.Args {
		if rendered, ok := renderCommonArg(arg); ok {
			argv = append(argv, rendered...)
			continue
		}
		switch arg.(type) {
		case SSHArg:
			argv = append(argv, "--ssh")
		case HostGatewayArg:
			// macOS provides host reachability built-in.
		case LabelArg:
			// Apple Containers has no label concept; discovery uses the
			// container environment instead, which both runtimes carry.
		}
	}

	argv = append(argv, spec.Image)
	return append(argv, spec.Command...)
}

func (a *Apple) RenderExec(spec ExecSpec) []string {
	argv := []string{a.Bin(), "exec"}
	if spec.TTY {
		argv = append(argv, "-it")
	}
	if spec.User != "" {
		argv = append(argv, "-u", spec.User)
	}
	for _, e := range spec.Env {
		argv = append(argv, "-e", e.Key+"="+e.Value)
	}
	argv = append(argv, spec.Container)
	return append(argv, spec.Command...)
}

// RenderBuild shares its shape with Docker's: only Bin() and the labels differ.
// See renderCommonArg for the analogous split on the run path.
//
// nil, not spec.Labels, mirroring the `case LabelArg:` arm above -- but for a
// harder reason than "no equivalent". Apple Containers does document
// `container build --label`; it has simply never been run here, and a rejected
// build flag *fails the build* on the primary platform, where a build failure
// is a hard error with no fallback by design. Nothing reads a label back, so
// dropping them costs provenance only. See BuildSpec.Labels.
func (a *Apple) RenderBuild(spec BuildSpec) []string {
	return renderBuild(a.Bin(), spec, nil)
}

// ImageID asks Apple Containers for the image's manifest digest. Neither the
// subcommand spelling nor the JSON shape has been run against a real install;
// probeImageID's capability probe is what turns a wrong noun into a named
// fault instead of a false "the base image is not built".
func (a *Apple) DescribeImage(ctx context.Context, ref string) (ImageDescriptor, bool, error) {
	id, ok, err := probeImageID(ctx, a.Bin(), ref, nil, parseAppleImageID)
	if err != nil || !ok {
		return ImageDescriptor{}, ok, err
	}
	if appleDigestBuildRefSupported(ctx, ref, id) {
		return ImageDescriptor{Identity: id, BuildRef: ref + "@" + id, BuildRefImmutable: true}, true, nil
	}
	return ImageDescriptor{Identity: id, BuildRef: ref, BuildRefImmutable: false}, true, nil
}

func (a *Apple) RenderTag(source, target string) []string {
	return []string{a.Bin(), "image", "tag", source, target}
}
func (a *Apple) RenderRemove(ref string) []string {
	// Apple calls the operation "delete" (not rm); --force makes an already
	// absent stage a successful cleanup.
	return []string{a.Bin(), "image", "delete", "--force", ref}
}

func (a *Apple) List(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, a.Bin(), "list", "--quiet").Output()
	if err != nil {
		// A failing probe means "nothing running" here, matching the bash
		// `2>/dev/null || true`.
		return nil, nil
	}
	return splitLines(string(out)), nil
}

func (a *Apple) InspectEnv(ctx context.Context, name string) ([]string, error) {
	out, err := exec.CommandContext(ctx, a.Bin(), "inspect", name).Output()
	if err != nil {
		// A failing probe means "no environment" here, matching the bash
		// `2>/dev/null || true`.
		return nil, nil
	}
	return parseAppleInspect(out), nil
}

// EnsureUp assumes a macOS host: an apple selection off darwin is refused by
// ValidateSelection, which cmd/claude-contained calls before anything touches
// the host, so this method is unreachable on any other platform. There is
// therefore no defensive platform arm here.
func (a *Apple) EnsureUp(ctx context.Context, stdout, stderr io.Writer, confirm func(string) bool) error {
	statusErr := exec.CommandContext(ctx, a.Bin(), "system", "status").Run()
	if statusErr == nil {
		return nil
	}
	diagnostic.For(ctx, diagnostic.ComponentRuntime).Debug("container runtime liveness probe failed",
		diagnostic.ErrorAttr(statusErr))
	if !confirm(a.Profile().NotRunningPrompt) {
		return ErrAborted
	}
	// Bash lets the start command inherit both streams, so its progress and any
	// failure reach the user rather than vanishing into a bare exit status.
	start := exec.CommandContext(ctx, a.Bin(), "system", "start")
	start.Stdout = stdout
	start.Stderr = stderr
	return start.Run()
}
