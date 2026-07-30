package runtime

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// dockerHelp is the bash `claude-docked --help` text plus the
// runtime-selection and build-context additions, which bash cannot have. See
// appleHelp.
//
//go:embed help_docked.txt
var dockerHelp string

// dockerMacSSHSocket is the bridged socket Docker Desktop exposes on macOS.
const dockerMacSSHSocket = "/run/host-services/ssh-auth.sock"

// dockerPollInterval is how long EnsureUp waits between daemon probes while
// Docker Desktop starts. A package variable only so a test can shrink it.
var dockerPollInterval = time.Second

// ErrNotRunning reports that the container runtime is down and this host cannot
// be asked to start it. EnsureUp has already explained itself on stderr.
var ErrNotRunning = errors.New("container runtime is not running")

// Docker drives Docker via the `docker` CLI.
type Docker struct{ platform Platform }

// NewDocker takes the platform explicitly; see NewApple for why there is no
// zero-argument form.
func NewDocker(p Platform) *Docker { return &Docker{platform: p} }

func (d *Docker) Profile() Profile {
	return Profile{
		Name: "claude-docked",
		// Docker keeps its own runtime resolver, so there is no forced default
		// here -- unlike Apple Containers.
		DefaultDNS:       nil,
		NotRunningPrompt: "Docker is not running. Start Docker Desktop? [Y/n] ",
		Help:             dockerHelp,
		// No notice: Docker routes to host services bound to 127.0.0.1, so -H
		// works for everything.
		HostForwardNotice: nil,
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
			// explicit mapping. Exactly Linux, with no else -- an unrecognized
			// platform gets no mapping, matching claude-docked:1828.
			if d.platform == Linux {
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
// bridged socket at a fixed path, while everywhere else the real agent socket has
// to be mounted through -- the one place a bind is not expressed as --mount.
//
// The platform test is `== Darwin` *with* a catch-all else, where the
// host-gateway test above is `== Linux` *without* one. The asymmetry is
// claude-docked:1813-1825 versus :1828, and it decides what an unrecognized
// platform does; tidying the two into one shape would change that silently.
//
// SSH_AUTH_SOCK is read here rather than captured into host.State because two of
// the three configurations ignore it: hoisting it would push a Docker-on-Linux
// detail above internal/runtime. bash reads it at the same point in the same
// process, so there is no behavioral difference.
func (d *Docker) sshArgs() []string {
	if d.platform == Darwin {
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
	for _, e := range spec.Env {
		argv = append(argv, "-e", e.Key+"="+e.Value)
	}
	argv = append(argv, spec.Container)
	return append(argv, spec.Command...)
}

// RenderBuild shares its shape with Apple's: only Bin() differs.
func (d *Docker) RenderBuild(spec BuildSpec) []string {
	argv := []string{d.Bin(), "build"}
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
	// splitEnvLines, not splitLines: an environment value's surrounding
	// whitespace is significant and the Apple path preserves it.
	return splitEnvLines(string(out)), nil
}

func (d *Docker) EnsureUp(ctx context.Context, stdout, stderr io.Writer, confirm func(string) bool) error {
	if d.daemonUp(ctx) {
		return nil
	}
	if d.platform != Darwin {
		// Off macOS the daemon is a service, not an application. Starting it is
		// distro-specific and may or may not need root (`sudo systemctl start
		// docker`, or `systemctl --user start docker-desktop` for Docker Desktop
		// on Linux and rootless installs), so there is nothing this process can
		// usefully offer to do -- and a [Y/n] whose "yes" branch cannot work is
		// worse than a plain diagnosis.
		//
		// This deliberately diverges from claude-docked:869-872, which runs
		// `open -a Docker` on a host with no `open` and then either dies with no
		// message under `set -e` or polls forever. That is the bug this ticket
		// exists to fix, and there is no oracle for the correct behavior.
		_, _ = fmt.Fprintln(stderr, "error: Docker is not running.")
		_, _ = fmt.Fprintln(stderr, "       Start the daemon and retry (for example: sudo systemctl start docker,")
		_, _ = fmt.Fprintln(stderr, "       or systemctl --user start docker-desktop for Docker Desktop).")
		return ErrNotRunning
	}
	if !confirm(d.Profile().NotRunningPrompt) {
		return ErrAborted
	}
	// Unlike Apple Containers' single start command, Docker Desktop is an
	// application: open it and wait for the daemon to answer. The wait is
	// deliberately unbounded, matching claude-docked:872.
	_ = exec.CommandContext(ctx, "open", "-a", "Docker").Run()
	_, _ = fmt.Fprintln(stdout, "Waiting for Docker to start...")
	for {
		if d.daemonUp(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dockerPollInterval):
		}
	}
}

func (d *Docker) daemonUp(ctx context.Context) bool {
	return exec.CommandContext(ctx, d.Bin(), "info").Run() == nil
}
