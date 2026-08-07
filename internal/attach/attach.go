// Package attach reconnects to an already-running container.
//
// `exec` bypasses the image entrypoint, so this package routes the command
// through the image's environment resolver and sandbox wrapper. Nothing here
// is runtime-specific: internal/runtime turns the ExecSpec into a command line.
package attach

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"claude-contained/internal/env"
	"claude-contained/internal/runtime"
)

// Prefix is the conventional container-name prefix (claude-contained:955).
// A fourth copy of this literal alongside internal/cli/cli.go:365 and
// internal/plan/plan.go:118,121 -- deliberately not consolidated here (out of
// scope for ticket 07; see the ticket's Comments for a follow-up note).
const Prefix = "aic-"

const (
	sandboxWrapper = "/usr/local/bin/srt-run" // claude-contained:148
	ToolEnvPath    = "/usr/local/bin/tool-env"
	shellPath      = "/usr/local/bin/shell-run" // claude-contained:140
)

var selectionPattern = regexp.MustCompile(`^[0-9]+$`)

// Request is everything Resolve needs. Running is passed in rather than
// fetched, so Resolve stays a function of its inputs and needs no runtime.
type Request struct {
	Name  string // cli.Config.AttachName, unnormalized, may be ""
	Shell bool   // cli.Config.ShellMode
	// Command is the container command from the CLI (everything after `--`).
	// Empty means run the attach default: a debug shell. `container exec` /
	// `docker exec` bypass the image ENTRYPOINT/CMD, so unlike the run path
	// there is no image default to inherit (see docs/adr/0009 and ticket 03
	// of #20).
	Command    []string
	SrtDisable bool       // cli.Config.SrtDisable
	Home       string     // host.State.Home
	Env        []env.Pair // --env flags only; the project file is read later
	Running    []string   // every running container, unfiltered

	Stdout, Stderr io.Writer
	// Prompt writes the prompt and returns one raw input line. ok=false is
	// EOF, which bash turns into an exit-1 abort with no message.
	Prompt func(prompt string) (line string, ok bool)
}

// Decision is the outcome. Spec==nil means there is nothing to exec and the
// launcher should return Code.
type Decision struct {
	Spec *runtime.ExecSpec
	Code int
}

// Resolve implements the plain attach block (claude-contained:952-1030): by
// name, or interactively when no name is given.
func Resolve(req Request) Decision {
	containers := FilterRunning(req.Running)

	if req.Name == "" {
		name, code, ok := selectFrom(req, containers)
		if !ok {
			return Decision{Code: code}
		}
		return Decision{Spec: buildSpec(req, name)}
	}

	name := normalizeName(req.Name)
	if !containsName(containers, name) {
		// --attach reconnects only. Creating on a name miss used to be silent,
		// so a typo started a second container instead of reporting the
		// mistake (claude-contained:1008-1013).
		_, _ = fmt.Fprintf(req.Stderr, "error: no running container named %s\n", name)
		_, _ = fmt.Fprintf(req.Stderr, "       use --name %s to create a new one\n", DisplayName(name))
		return Decision{Code: 1}
	}
	return Decision{Spec: buildSpec(req, name)}
}

// FilterRunning keeps only conventionally-prefixed, non-empty names
// (claude-contained:957-959).
func FilterRunning(names []string) []string {
	var out []string
	for _, n := range names {
		if n == "" {
			continue
		}
		if strings.HasPrefix(n, Prefix) {
			out = append(out, n)
		}
	}
	return out
}

// normalizeName prepends Prefix when the caller omitted it
// (claude-contained:1004-1006).
func normalizeName(name string) string {
	if strings.HasPrefix(name, Prefix) {
		return name
	}
	return Prefix + name
}

// DisplayName strips Prefix once, for the picker and error text
// (claude-contained:967, :1012).
func DisplayName(name string) string {
	return strings.TrimPrefix(name, Prefix)
}

func containsName(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// selectFrom is the interactive picker (claude-contained:962-989). ok=false
// means there is nothing to exec; code is what the launcher should return.
func selectFrom(req Request, containers []string) (name string, code int, ok bool) {
	if len(containers) == 0 {
		_, _ = fmt.Fprintln(req.Stdout, "No running aic containers")
		return "", 0, false
	}

	_, _ = fmt.Fprintln(req.Stdout, "Running containers:")
	for i, c := range containers {
		_, _ = fmt.Fprintf(req.Stdout, "  %d) %s\n", i+1, DisplayName(c))
	}
	_, _ = fmt.Fprintln(req.Stdout, "")

	if req.Prompt == nil {
		// No prompt to read from is the same as an immediate EOF.
		return "", 1, false
	}
	line, promptOK := req.Prompt(fmt.Sprintf("Select container (1-%d, q to quit): ", len(containers)))
	if !promptOK {
		return "", 1, false
	}

	idx, quit, valid := ParseSelection(line, len(containers))
	if quit {
		return "", 0, false
	}
	if !valid {
		_, _ = fmt.Fprintln(req.Stderr, "Invalid selection")
		return "", 1, false
	}
	return containers[idx-1], 0, true
}

// ParseSelection mirrors bash's `read choice` handling: only leading/trailing
// IFS whitespace (space, tab, newline) is stripped, so a trailing '\r'
// survives and makes the value invalid, matching bash's regex match against
// the raw string.
func ParseSelection(line string, count int) (idx int, quit, valid bool) {
	trimmed := strings.Trim(line, " \t\n")
	if trimmed == "" || trimmed == "q" || trimmed == "Q" {
		return 0, true, false
	}
	if !selectionPattern.MatchString(trimmed) {
		return 0, false, false
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n < 1 || n > count {
		return 0, false, false
	}
	return n, false, true
}

// buildSpec assembles the ExecSpec for a resolved container name.
func buildSpec(req Request, name string) *runtime.ExecSpec {
	return &runtime.ExecSpec{
		Container: name,
		User:      "dev",
		TTY:       true,
		Env:       ExecEnv(req.Home, req.Env),
		Command:   Command(req),
	}
}

// Command builds the container command: the env resolver, the optional
// sandbox wrapper, then the user's command. `container exec` bypasses the
// image CMD, so an empty command (and -s) default to shell-run explicitly.
func Command(req Request) []string {
	cmd := []string{ToolEnvPath}
	if !req.SrtDisable {
		cmd = append(cmd, sandboxWrapper)
	}

	// req.Shell and an empty req.Command both resolve to shellPath. -s plus a
	// command is already rejected by Validate's "shell-with-command" check, so
	// req.Shell == true always implies an empty req.Command; the || is
	// belt-and-suspenders and documents intent.
	if req.Shell || len(req.Command) == 0 {
		return append(cmd, shellPath)
	}
	return append(cmd, req.Command...)
}

// ExecEnv carries only host-known values. Tool paths and layer fragments are
// resolved inside the container by tool-env, for both run and attach paths.
func ExecEnv(home string, pairs []env.Pair) []runtime.EnvArg {
	out := []runtime.EnvArg{
		{Key: "HOME", Value: home},
	}
	for _, p := range pairs {
		out = append(out, runtime.EnvArg{Key: p.Key, Value: p.Value})
	}
	out = append(out, runtime.EnvArg{Key: env.ExplicitKeysMarker, Value: env.ExplicitKeysValue(pairs)})
	return out
}
