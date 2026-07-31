// Package attach reconnects to an already-running container.
//
// `exec` bypasses the image entrypoint, so this package -- not the entrypoint --
// prepends the sandbox wrapper and supplies HOME/JAVA_HOME/PATH. Nothing here
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
	sandboxWrapper = "srt-run"                  // claude-contained:148
	shellPath      = "/usr/local/bin/shell-run" // claude-contained:140
	claudePath     = "/opt/claude/claude"       // claude-contained:134
	javaHome       = "/opt/jbr"
)

// containerPathFmt is the PATH the exec'd process gets (claude-contained:177).
// %s is the *host* HOME, exactly as bash expands $HOME there.
const containerPathFmt = "/opt/claude:/home/dev/.sdkman/candidates/maven/current/bin:" +
	"/home/dev/.sdkman/candidates/jbang/current/bin:/opt/jbr/bin:%s/.local/bin:" +
	"/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

var selectionPattern = regexp.MustCompile(`^[0-9]+$`)

// Request is everything Resolve needs. Running is passed in rather than
// fetched, so Resolve stays a function of its inputs and needs no runtime.
type Request struct {
	Name       string     // cli.Config.AttachName, unnormalized, may be ""
	Shell      bool       // cli.Config.ShellMode
	Tool       string     // cli.Config.Tool (never validated -- see doc)
	Yolo       bool       // cli.Config.YoloMode
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

// Command builds the container command (build_attach_cmd /
// build_attach_shell_cmd, claude-contained:143-165). --shell ignores the tool
// and the yolo flag entirely.
func Command(req Request) []string {
	var cmd []string
	if !req.SrtDisable {
		cmd = append(cmd, sandboxWrapper)
	}

	if req.Shell {
		return append(cmd, shellPath)
	}

	cmd = append(cmd, toolPath(req.Tool))
	if req.Yolo {
		switch req.Tool {
		case "claude":
			cmd = append(cmd, "--dangerously-skip-permissions")
		case "codex", "copilot", "gemini":
			cmd = append(cmd, "--yolo")
		case "vibe":
			// This string deliberately differs from the run path's warning
			// (internal/plan/plan.go), which adds " (no equivalent flag)".
			_, _ = fmt.Fprintln(req.Stderr, "Warning: vibe does not support yolo mode")
		}
		// Any other tool: no flag, no error -- the attach path never
		// validates the tool (claude-contained:132-137, :151-155).
	}
	return cmd
}

// toolPath mirrors get_tool_path (claude-contained:132-137): claude lives
// under /opt/claude, everything else is looked up on PATH verbatim, including
// an unknown tool.
func toolPath(tool string) string {
	if tool == "claude" {
		return claudePath
	}
	return tool
}

// ExecEnv mirrors exec_env_args (claude-contained:175-178): HOME, JAVA_HOME,
// PATH, then the user's --env pairs in order.
func ExecEnv(home string, pairs []env.Pair) []runtime.EnvArg {
	out := []runtime.EnvArg{
		{Key: "HOME", Value: home},
		{Key: "JAVA_HOME", Value: javaHome},
		{Key: "PATH", Value: fmt.Sprintf(containerPathFmt, home)},
	}
	for _, p := range pairs {
		out = append(out, runtime.EnvArg{Key: p.Key, Value: p.Value})
	}
	return out
}
