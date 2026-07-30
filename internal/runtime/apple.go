package runtime

import (
	"context"
	_ "embed"
	"errors"
	"io"
	"os/exec"
)

// appleHelp is the bash `claude-contained --help` text plus the
// runtime-selection and build-context additions, which bash cannot have
// because it selects its runtime by being a different file and always finds
// its build context by self-location. That is the only difference, and it is
// deliberate -- see docs/adr/0004-go-launcher-rewrite.md. A comment cannot
// live inside the .txt, which is printed verbatim.
//
//go:embed help_contained.txt
var appleHelp string

// ErrAborted reports that the user declined to start the container runtime.
var ErrAborted = errors.New("aborted")

// Apple drives Apple Containers via the `container` CLI.
type Apple struct{ platform Platform }

// NewApple takes the platform explicitly; there is no zero-argument form,
// because it would silently construct the unnamed-platform behavior in a caller
// that meant to say Darwin.
func NewApple(p Platform) *Apple { return &Apple{platform: p} }

func (a *Apple) Profile() Profile {
	return Profile{
		Name: "claude-contained",
		// Apple Containers points the container at the vmnet gateway
		// (192.168.64.1) for DNS, which is frequently unreachable, so a public
		// resolver is the default rather than an extra flag the user must know.
		DefaultDNS:       []string{"1.1.1.1"},
		NotRunningPrompt: "Container system is not running. Start it? [Y/n] ",
		Help:             appleHelp,
		// Apple Containers routes the container through the vmnet gateway, which
		// cannot reach a host service bound to 127.0.0.1 (apple/container#346).
		// Kept byte-identical to claude-contained's own lines, because the
		// differential harness compares stderr.
		HostForwardNotice: []string{
			"Warning: Apple Containers cannot reach host services bound only to 127.0.0.1.",
			"         -H reaches host services listening on 0.0.0.0; use Docker for the rest.",
		},
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

// RenderBuild shares its shape with Docker's: only Bin() differs. See
// renderCommonArg for the analogous split on the run path.
func (a *Apple) RenderBuild(spec BuildSpec) []string {
	argv := []string{a.Bin(), "build"}
	// --pull before --no-cache, matching claude-contained:546 argument for
	// argument: the corpus compares the emitted argv.
	if spec.Pull {
		argv = append(argv, "--pull")
	}
	if spec.NoCache {
		argv = append(argv, "--no-cache")
	}
	for _, ba := range spec.BuildArgs {
		argv = append(argv, "--build-arg", ba)
	}
	return append(argv, "-t", spec.Tag, spec.Context)
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
// ValidateSelection, which cmd/claude-go calls before anything touches the host,
// so this method is unreachable on any other platform. There is therefore no
// defensive platform arm here.
func (a *Apple) EnsureUp(ctx context.Context, stdout, stderr io.Writer, confirm func(string) bool) error {
	if exec.CommandContext(ctx, a.Bin(), "system", "status").Run() == nil {
		return nil
	}
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
