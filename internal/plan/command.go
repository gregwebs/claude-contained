package plan

import "claude-contained/internal/zellij"

// shellPath is what -s runs inside the container: a wrapper that gives bash a
// controlling terminal after the sandbox/runtime handoff, not bash itself.
const shellPath = "/usr/local/bin/shell-run"

// containerCommand assembles the argv executed inside the container: the user's
// command (empty when none was given, so the image CMD runs), with the -s shell
// substitution and the Zellij wrap applied. Ticket 04 (#39) reintroduces
// mount-driven injection as user configuration; this ticket injects nothing.
func containerCommand(userCmd []string, shellMode bool, zellijSession, profName string) []string {
	command := append([]string{}, userCmd...)
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
