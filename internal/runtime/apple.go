package runtime

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
)

//go:embed help_contained.txt
var appleHelp string

// ErrAborted reports that the user declined to start the container runtime.
var ErrAborted = errors.New("aborted")

// Apple drives Apple Containers via the `container` CLI.
type Apple struct{}

func NewApple() *Apple { return &Apple{} }

func (a *Apple) Profile() Profile {
	return Profile{
		Name: "claude-contained",
		// Apple Containers points the container at the vmnet gateway
		// (192.168.64.1) for DNS, which is frequently unreachable, so a public
		// resolver is the default rather than an extra flag the user must know.
		DefaultDNS:       []string{"1.1.1.1"},
		NotRunningPrompt: "Container system is not running. Start it? [Y/n] ",
		Help:             appleHelp,
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
	argv = append(argv, spec.Container)
	return append(argv, spec.Command...)
}

func (a *Apple) RenderBuild(spec BuildSpec) []string {
	argv := []string{a.Bin(), "build"}
	if spec.Platform != "" {
		argv = append(argv, "--platform", spec.Platform)
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

// appleInspect mirrors the shapes the bash jq filter tolerates: the response
// may be an array or a single object, and the environment may sit under
// configuration.initProcess.environment.
type appleInspect struct {
	Configuration struct {
		InitProcess struct {
			Environment []string `json:"environment"`
		} `json:"initProcess"`
	} `json:"configuration"`
}

func (a *Apple) InspectEnv(ctx context.Context, name string) ([]string, error) {
	out, err := exec.CommandContext(ctx, a.Bin(), "inspect", name).Output()
	if err != nil {
		return nil, nil
	}

	var many []appleInspect
	if err := json.Unmarshal(out, &many); err == nil {
		var env []string
		for _, item := range many {
			env = append(env, item.Configuration.InitProcess.Environment...)
		}
		return env, nil
	}

	var one appleInspect
	if err := json.Unmarshal(out, &one); err == nil {
		return one.Configuration.InitProcess.Environment, nil
	}
	return nil, nil
}

func (a *Apple) EnsureUp(ctx context.Context, out io.Writer, confirm func(string) bool) error {
	if exec.CommandContext(ctx, a.Bin(), "system", "status").Run() == nil {
		return nil
	}
	if !confirm(a.Profile().NotRunningPrompt) {
		return ErrAborted
	}
	// Bash lets the start command inherit both streams, so its progress and any
	// failure reach the user rather than vanishing into a bare exit status.
	start := exec.CommandContext(ctx, a.Bin(), "system", "start")
	start.Stdout = os.Stdout
	start.Stderr = os.Stderr
	return start.Run()
}

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
