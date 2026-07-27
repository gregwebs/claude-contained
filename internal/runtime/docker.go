package runtime

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
)

//go:embed help_docked.txt
var dockerHelp string

// dockerMacSSHSocket is the bridged socket Docker Desktop exposes on macOS.
const dockerMacSSHSocket = "/run/host-services/ssh-auth.sock"

// Docker drives Docker via the `docker` CLI.
type Docker struct{}

func NewDocker() *Docker { return &Docker{} }

func (d *Docker) Profile() Profile {
	return Profile{
		Name: "claude-docked",
		// Docker keeps its own runtime resolver, so there is no forced default
		// here -- unlike Apple Containers.
		DefaultDNS:       nil,
		NotRunningPrompt: "Docker is not running. Start Docker Desktop? [Y/n] ",
		Help:             dockerHelp,
	}
}

func (d *Docker) Bin() string { return "docker" }

func (d *Docker) RenderRun(spec RunSpec) []string {
	argv := []string{d.Bin(), "run", "--rm", "-it"}

	for _, arg := range spec.Args {
		if rendered, ok := renderCommonArg(arg); ok {
			argv = append(argv, rendered...)
			continue
		}
		switch v := arg.(type) {
		case SSHArg:
			argv = append(argv, d.sshArgs()...)
		case HostGatewayArg:
			// macOS and Windows provide this built-in; only Linux needs the
			// explicit mapping.
			if runtime.GOOS == "linux" {
				argv = append(argv, "--add-host", "host.docker.internal:host-gateway")
			}
		case LabelArg:
			argv = append(argv, "--label", v.Key+"="+v.Value)
		}
	}

	argv = append(argv, spec.Image)
	return append(argv, spec.Command...)
}

// sshArgs covers all three configurations: Docker Desktop on macOS exposes a
// bridged socket at a fixed path, while on Linux the real agent socket has to be
// mounted through -- the one place a bind is not expressed as --mount.
func (d *Docker) sshArgs() []string {
	if runtime.GOOS == "darwin" {
		return []string{
			"--mount", "type=bind,src=" + dockerMacSSHSocket + ",dst=/ssh-agent",
			"-e", "SSH_AUTH_SOCK=/ssh-agent",
		}
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		return []string{"-v", sock + ":/ssh-agent", "-e", "SSH_AUTH_SOCK=/ssh-agent"}
	}
	return nil
}

func (d *Docker) RenderExec(spec ExecSpec) []string {
	argv := []string{d.Bin(), "exec"}
	if spec.TTY {
		argv = append(argv, "-it")
	}
	if spec.User != "" {
		argv = append(argv, "-u", spec.User)
	}
	argv = append(argv, spec.Container)
	return append(argv, spec.Command...)
}

func (d *Docker) RenderBuild(spec BuildSpec) []string {
	argv := []string{d.Bin(), "build"}
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

func (d *Docker) List(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, d.Bin(), "ps", "--format", "{{.Names}}").Output()
	if err != nil {
		return nil, nil
	}
	return splitLines(string(out)), nil
}

func (d *Docker) InspectEnv(ctx context.Context, name string) ([]string, error) {
	out, err := exec.CommandContext(ctx, d.Bin(), "inspect",
		"-f", "{{range .Config.Env}}{{println .}}{{end}}", name).Output()
	if err != nil {
		return nil, nil
	}
	return splitLines(string(out)), nil
}

func (d *Docker) EnsureUp(ctx context.Context, out io.Writer, confirm func(string) bool) error {
	if exec.CommandContext(ctx, d.Bin(), "info").Run() == nil {
		return nil
	}
	if !confirm(d.Profile().NotRunningPrompt) {
		return ErrAborted
	}
	// Unlike Apple Containers' single start command, Docker Desktop is an
	// application: open it and wait for the daemon to answer.
	_ = exec.CommandContext(ctx, "open", "-a", "Docker").Run()
	fmt.Fprintln(out, "Waiting for Docker to start...")
	for {
		if exec.CommandContext(ctx, d.Bin(), "info").Run() == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
