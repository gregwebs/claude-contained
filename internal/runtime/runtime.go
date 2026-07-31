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
	"errors"
	"fmt"
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

// BuildSpec is the shape of an image build. There is no Platform field: neither
// launcher has ever passed --platform on a rebuild (README.md documents it for
// manual builds, which is a different command), and a field no caller can set is
// an invitation to render an argument bash never emitted.
type BuildSpec struct {
	Tag     string
	Context string
	// Pull refreshes the base image; NoCache discards every layer. The full
	// rebuild sets both (claude-contained:546).
	Pull      bool
	NoCache   bool
	BuildArgs []string
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
	// embed it. Both runtimes carry the same value, ProgName -- ticket 11
	// dropped the second launcher name -- but the field stays on Profile
	// rather than collapsing to the package constant directly, because
	// plan.Build takes a Profile and a hardcoded name inside it would be one
	// more thing a test double has to remember to set correctly. The product
	// name (the image tag, "reserved by claude-contained", the update notice)
	// stays literal in both runtimes.
	Name string
	// DefaultDNS applies when neither --dns nor CLAUDE_DNS supplied one. Apple
	// Containers points at an often-unreachable vmnet gateway, so it forces a
	// public resolver; Docker keeps its own resolver and leaves this empty.
	DefaultDNS []string
	// NotRunningPrompt is the exact prompt shown when the runtime is down.
	NotRunningPrompt string
	// HostForwardNotice is printed to stderr, one line per element, when -H is
	// used and this runtime cannot reach host services bound to 127.0.0.1. Empty
	// means the runtime forwards them properly, so nothing is said.
	//
	// A notice and not a refusal: -H is also used for host services listening on
	// 0.0.0.0, which both runtimes reach through the same in-container socat
	// forward (image/host-forward.sh), so refusing would remove working
	// behavior. Data on Profile rather than a Runtime method because plan.Build
	// must stay a pure function and already receives a Profile; NotRunningPrompt
	// is the existing precedent for a runtime-specific literal message.
	HostForwardNotice []string
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
	// Apple Containers issues one command, and on a host where the runtime is a
	// system service there is nothing to offer at all -- so the report may be a
	// refusal on stderr rather than a prompt.
	EnsureUp(ctx context.Context, stdout, stderr io.Writer, confirm func(prompt string) bool) error
}

// Runtime names accepted by --container-runtime and CLAUDE_CONTAINED_RUNTIME.
const (
	NameApple  = "apple"
	NameDocker = "docker"
)

// ProgName is the single name the launcher is installed under. It is not
// runtime-specific: ticket 11 dropped the second launcher name, so a Docker
// user selects the runtime with --container-runtime or
// CLAUDE_CONTAINED_RUNTIME instead of a different binary name.
const ProgName = "claude-contained"

// Selection is every input that decides which container runtime is used, in
// precedence order. Grouping them makes the precedence one readable expression
// instead of positional parameters nobody can order from the call site.
type Selection struct {
	Flag     string   // --container-runtime; "" when absent
	Env      string   // CLAUDE_CONTAINED_RUNTIME; "" when unset
	Argv0    string   // a basename containing "dock" selects Docker
	Platform Platform // decides the default, and is handed to the runtime built
}

// Select chooses the container runtime: --container-runtime, else
// CLAUDE_CONTAINED_RUNTIME, else an argv[0] basename containing "dock", else the
// host platform (Apple Containers on macOS, Docker elsewhere).
//
// argv[0] is no longer the primary selection mechanism -- both runtimes now
// install under the same name, ProgName -- but it survives as a compat
// affordance: a user who symlinks `claude-docked` to the installed binary (or
// still has one from before ticket 11) keeps selecting Docker that way with no
// flag, and it is how the shell test suites drive the Docker runtime as a
// target path. It is not the final fallback, though: a basename *without*
// "dock" is not a selection, because "not docked" cannot mean Apple Containers
// on a host that has none.
//
// Select is total -- an unrecognized Flag or Env value falls through to the next
// source rather than failing here. It has to run *before* the command line is
// parsed at all, because cli.Parse's error messages name the program and --help
// prints the selected runtime's own literal text, so a runtime must exist on
// every path including the one about to exit 2. Diagnosing a bad value is
// ValidateSelection's job, called after parsing so that -h/--help still wins.
func Select(s Selection) Runtime {
	if rt, ok := byName(s.Flag, s.Platform); ok {
		return rt
	}
	if rt, ok := byName(s.Env, s.Platform); ok {
		return rt
	}
	if strings.Contains(strings.ToLower(baseName(s.Argv0)), "dock") {
		return NewDocker(s.Platform)
	}
	if s.Platform == Darwin {
		return NewApple(s.Platform)
	}
	return NewDocker(s.Platform)
}

// byName matches a *typed* runtime name exactly, case-insensitively. It is
// deliberately not the substring test Select applies to argv[0]: "dock" matching
// is for basenames, and applying it to a value someone typed would turn
// `dockerr` into a silent selection instead of the usage error it deserves.
//
// An apple selection on a non-macOS host is returned here and refused by
// ValidateSelection, so that --container-runtime=apple --help still prints the
// help for the runtime the user asked about.
func byName(name string, p Platform) (Runtime, bool) {
	switch strings.ToLower(name) {
	case NameApple:
		return NewApple(p), true
	case NameDocker:
		return NewDocker(p), true
	}
	return nil, false
}

// ValidateSelection reports the usage error in a selection, if any, to stderr
// and returns a non-nil error when the caller should exit with a usage status.
//
// Only the source actually *used* is checked: a valid --container-runtime
// rescues a broken environment variable, which is what "the flag wins" has to
// mean to be useful.
func ValidateSelection(s Selection, stderr io.Writer) error {
	if s.Flag != "" {
		return validateRuntimeName("--container-runtime", s.Flag, s.Platform, stderr)
	}
	if s.Env != "" {
		return validateRuntimeName("CLAUDE_CONTAINED_RUNTIME", s.Env, s.Platform, stderr)
	}
	return nil
}

// ErrBadSelection reports that the runtime selection is unusable. EnsureUp's
// counterpart for selection: the message is already on stderr.
var ErrBadSelection = errors.New("invalid container runtime selection")

func validateRuntimeName(source, value string, p Platform, stderr io.Writer) error {
	switch strings.ToLower(value) {
	case NameDocker:
		return nil
	case NameApple:
		if p != Darwin {
			_, _ = fmt.Fprintln(stderr, "error: the apple container runtime is available only on macOS")
			_, _ = fmt.Fprintln(stderr, "       use --container-runtime=docker or CLAUDE_CONTAINED_RUNTIME=docker")
			return ErrBadSelection
		}
		return nil
	}
	_, _ = fmt.Fprintf(stderr, "error: %s must be %s or %s: %s\n", source, NameApple, NameDocker, value)
	return ErrBadSelection
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

// splitLines splits and trims, which is right for container *names*: bash reads
// `container list --quiet` / `docker ps` output and skips blank lines.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// splitEnvLines splits *without* trimming. bash reads inspect output with
// `while IFS= read -r line`, which preserves surrounding whitespace, and the
// Apple path preserves it too (the value comes out of a JSON string). Trimming
// here would make the two runtimes disagree for an environment value with a
// leading or trailing space -- the one thing InspectEnv must never do, since
// both runtimes feed the same Zellij discovery.
func splitEnvLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
