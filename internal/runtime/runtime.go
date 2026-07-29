// Package runtime is the container-runtime seam: the one place that knows
// whether we are driving Apple Containers or Docker.
//
// The rule that makes the rewrite worth doing is that *nothing above this
// package may mention `container` or `docker`*. Everything shared -- argument
// parsing, host probing, plan building -- works in terms of RunSpec and
// Profile, and only the implementations here turn that intent into a command
// line.
package runtime

import (
	"context"
	"io"
	"strings"
)

// Arg is one unit of intent in a run. The bash launcher interleaves mounts,
// environment variables and flags in a specific order that is directly
// observable in the emitted argv, so a RunSpec cannot group them by kind and
// still reproduce it. An ordered list of typed args preserves that order
// without letting any runtime syntax leak upwards.
type Arg interface{ isArg() }

type (
	// MemoryArg is the memory ceiling for the container.
	MemoryArg struct{ Value string }
	// NameArg names the container.
	NameArg struct{ Value string }
	// WorkdirArg is the working directory inside the container.
	WorkdirArg struct{ Value string }
	// EnvArg is one environment variable for the container's init process.
	EnvArg struct{ Key, Value string }
	// MountArg is one bind mount. Mount syntax is not a divergence: both
	// runtimes take the same form, read-only marker included.
	MountArg struct {
		Src      string
		Dst      string
		ReadOnly bool
	}
	// PortArg publishes a port.
	PortArg struct{ Spec string }
	// DNSArg overrides a resolver.
	DNSArg struct{ Server string }
	// SSHArg forwards the host's SSH agent. Apple Containers has a dedicated
	// flag; Docker needs a socket bind that differs again between macOS and
	// Linux, which is why this is intent rather than a rendered flag.
	SSHArg struct{}
	// HostGatewayArg asks that the host be reachable from inside the
	// container. macOS and Windows provide this built-in; only Docker on Linux
	// needs an explicit mapping.
	HostGatewayArg struct{}
	// LabelArg is informational metadata. Docker records these; Apple
	// Containers has no equivalent, so discovery must never depend on them.
	LabelArg struct{ Key, Value string }
)

func (MemoryArg) isArg()      {}
func (NameArg) isArg()        {}
func (WorkdirArg) isArg()     {}
func (EnvArg) isArg()         {}
func (MountArg) isArg()       {}
func (PortArg) isArg()        {}
func (DNSArg) isArg()         {}
func (SSHArg) isArg()         {}
func (HostGatewayArg) isArg() {}
func (LabelArg) isArg()       {}

// RunSpec describes the intended run in runtime-agnostic terms.
type RunSpec struct {
	Args    []Arg
	Image   string
	Command []string
}

// ExecSpec is the shape of an exec into a running container (ticket 07).
type ExecSpec struct {
	Container string
	User      string
	TTY       bool
	// Env is the environment for the exec'd process, emitted as `-e K=V` in
	// order. `exec` bypasses the entrypoint, so nothing else sets HOME/PATH
	// for the attached process (claude-contained:175-178).
	Env     []EnvArg
	Command []string
}

// BuildSpec is the shape of an image build (ticket 10).
type BuildSpec struct {
	Tag       string
	Context   string
	Platform  string
	BuildArgs []string
	NoCache   bool
}

// Profile is the pure, data-only half of a runtime: everything plan building
// needs to know about which runtime it targets, with no I/O attached.
//
// This exists because several planning decisions genuinely differ between the
// runtimes -- the default DNS resolver, the wording of the "runtime is not
// running" prompt -- and plan.Build must stay a pure function. Handing it a
// Profile keeps it pure while letting it see the difference.
type Profile struct {
	// Name is the program name, used only in the handful of messages that
	// embed it. The product name (the image tag, "reserved by
	// claude-contained", the update notice) stays literal in both runtimes.
	Name string
	// DefaultDNS applies when neither --dns nor CLAUDE_DNS supplied one. Apple
	// Containers points at an often-unreachable vmnet gateway, so it forces a
	// public resolver; Docker keeps its own resolver and leaves this empty.
	DefaultDNS []string
	// NotRunningPrompt is the exact prompt shown when the runtime is down.
	NotRunningPrompt string
	// Help is the full --help text. The two launchers' help differs by much
	// more than the program name -- the description line, the --dns line, an
	// entire DNS paragraph, a Docker-only "Build the image" block, and even
	// example column alignment -- so it is two literal texts, not a template.
	Help string
}

// Runtime is the seam. Profile is available without touching the system; the
// methods below are the operations that actually shell out.
type Runtime interface {
	Profile() Profile
	// Bin is the runtime executable, resolved through PATH.
	Bin() string
	// RenderRun returns the complete argv, including the binary, the image and
	// the container command.
	RenderRun(RunSpec) []string
	// RenderExec returns the complete argv for an exec (ticket 07).
	RenderExec(ExecSpec) []string
	// RenderBuild returns the complete argv for an image build (ticket 10).
	RenderBuild(BuildSpec) []string
	// List reports the names of running containers.
	List(ctx context.Context) ([]string, error)
	// InspectEnv reports a container's environment as KEY=VALUE lines -- the
	// portable source of truth for Zellij discovery (ticket 08), so both
	// runtimes must yield the same shape.
	InspectEnv(ctx context.Context, name string) ([]string, error)
	// EnsureUp checks whether the runtime is running and, if not, asks confirm
	// and starts it. It owns its user-facing output, because the runtimes
	// differ in more than a probe: Docker opens an application and polls,
	// Apple Containers issues one command.
	EnsureUp(ctx context.Context, out io.Writer, confirm func(prompt string) bool) error
}

// Select chooses the runtime from argv[0]. This deliberately avoids adding a
// flag: any flag the bash launchers do not know would itself be a divergence in
// the unknown-flag path, and bash picks its runtime by *being a different file*.
// A basename containing "dock" means Docker, anything else Apple Containers --
// which is why the build produces a `claude-go-docked` symlink.
//
// Ticket 09 adds explicit selection by flag and environment variable; the
// signature already has room for both, with argv[0] as the fallback.
func Select(argv0, envOverride, flagOverride string) Runtime {
	choice := flagOverride
	if choice == "" {
		choice = envOverride
	}
	if choice == "" {
		choice = baseName(argv0)
	}
	if strings.Contains(strings.ToLower(choice), "dock") {
		return NewDocker()
	}
	return NewApple()
}

func baseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// renderMount is shared: both runtimes accept the same bind-mount syntax.
func renderMount(m MountArg) []string {
	opt := "type=bind,src=" + m.Src + ",dst=" + m.Dst
	if m.ReadOnly {
		opt += ",readonly"
	}
	return []string{"--mount", opt}
}

// renderCommonArg handles every arg whose syntax the two runtimes share. It
// returns ok=false for the ones each implementation must decide for itself.
func renderCommonArg(a Arg) ([]string, bool) {
	switch v := a.(type) {
	case MemoryArg:
		return []string{"--memory", v.Value}, true
	case NameArg:
		return []string{"--name", v.Value}, true
	case WorkdirArg:
		return []string{"-w", v.Value}, true
	case EnvArg:
		return []string{"-e", v.Key + "=" + v.Value}, true
	case MountArg:
		return renderMount(v), true
	case PortArg:
		return []string{"-p", v.Spec}, true
	case DNSArg:
		return []string{"--dns", v.Server}, true
	}
	return nil, false
}
