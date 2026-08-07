package plan

import "claude-contained/internal/zellij"

// shellPath is what -s runs inside the container: a wrapper that gives bash a
// controlling terminal after the sandbox/runtime handoff, not bash itself.
const shellPath = "/usr/local/bin/shell-run"

// ToolError reports an unknown -t/--tool value. Bash discovers this late, after
// mounts and mutations have already been applied, so it is returned alongside a
// populated Program rather than short-circuiting the plan.
type ToolError struct{ Tool string }

func (e *ToolError) Error() string { return "unknown tool: " + e.Tool }

// containerCommand is the single owner of container-command assembly: the
// validated base argv and its yolo flag, the --add-dir pair each extra mount
// contributes, the user's trailing arguments, and the final -s / Zellij
// substitution. Build feeds it at three points rather than one because two of
// those inputs are observable and must stay interleaved with the surrounding
// steps: tool validation happens after the mounts and host mutations (corpus
// entry 07), and each --add-dir sits inside the extra-mount loop next to the
// MountArg it accompanies. Tickets 02 and 04 change what feeds the front of
// this type; this ticket changes nothing it emits.
type containerCommand struct {
	tool string // retained only to gate --add-dir to claude/codex; ticket 04 removes the gate
	argv []string
}

// newContainerCommand maps the tool name to its base argv and yolo flag and
// validates it. An unknown tool is a *ToolError, which Build returns after the
// mounts above it have already been emitted. The vibe+yolo warning is
// returned rather than printed so Build can keep it ordered with the other
// steps.
func newContainerCommand(tool string, yolo bool) (containerCommand, string, error) {
	argv, warning, err := toolCommand(tool, yolo)
	if err != nil {
		// Contract: toolCommand never returns both a warning and an error, so
		// dropping warning here loses nothing today. If a future tool variant
		// could produce both, this must change to return warning unconditionally
		// so Build's ordered stderr Print step still sees it.
		return containerCommand{}, "", err
	}
	return containerCommand{tool: tool, argv: argv}, warning, nil
}

// toolCommand maps the tool name to its command and yolo flag. The warning is
// returned rather than printed so it stays ordered with the other steps.
func toolCommand(tool string, yolo bool) (argv []string, warning string, err error) {
	switch tool {
	case "claude":
		argv = []string{"claude"}
		if yolo {
			argv = append(argv, "--dangerously-skip-permissions")
		}
	case "codex", "copilot", "gemini":
		argv = []string{tool}
		if yolo {
			argv = append(argv, "--yolo")
		}
	case "vibe":
		argv = []string{"vibe"}
		if yolo {
			warning = "Warning: vibe does not support yolo mode (no equivalent flag)"
		}
	default:
		return nil, "", &ToolError{Tool: tool}
	}
	return argv, warning, nil
}

// addExtraMount appends the --add-dir flag pair for one extra mount, for the
// tools that understand it. Called once per -m mount, in -m order, from
// inside Build's extra-mount loop so it stays interleaved with the MountArg
// and reg.addUser for the same mount. Ticket 04 replaces the hard-coded gate
// with user configuration.
func (c *containerCommand) addExtraMount(src string) {
	// Only claude and codex understand --add-dir.
	if c.tool == "claude" || c.tool == "codex" {
		c.argv = append(c.argv, "--add-dir", src)
	}
}

// finish appends the user's trailing arguments, applies the -s shell
// substitution (plain bash under Zellij, shell-run otherwise), and wraps the
// result for Zellij when a session is active. It returns the argv for
// RunSpec.Command. User args are appended before shell mode decides to
// discard them, matching the interleaving the pre-extraction code had.
func (c containerCommand) finish(userArgs []string, shellMode bool, zellijSession, profName string) []string {
	// Copy rather than append-in-place: c.argv may have spare capacity left by
	// addExtraMount, and an in-place append would write userArgs into that
	// shared backing array. Copying first means a later read of cmd.argv, or a
	// second finish call, can't observe corruption from this one.
	command := make([]string, len(c.argv), len(c.argv)+len(userArgs))
	copy(command, c.argv)
	command = append(command, userArgs...)
	if shellMode {
		// Under Zellij the debug shell is plain bash: the pane already supplies
		// a controlling terminal, so shellPath's rationale doesn't apply here
		// (claude-contained:1957-1961).
		if zellijSession != "" {
			command = []string{zellij.ShellCommand}
		} else {
			command = []string{shellPath}
		}
	}
	if zellijSession != "" {
		command = zellij.RunCommand(zellijSession, profName, command)
	}
	return command
}
