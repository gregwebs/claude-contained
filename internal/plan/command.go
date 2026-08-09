package plan

import "claude-contained/internal/zellij"

// shellPath is what -s runs inside the container: a wrapper that gives bash a
// controlling terminal after the sandbox/runtime handoff, not bash itself.
const shellPath = "/usr/local/bin/shell-run"

// containerCommand assembles the argv executed inside the container: the
// user's command (empty when none was given, so the image CMD runs), with
// mount-flag injection, the -s shell substitution, and the Zellij wrap
// applied in that order. Injection runs before the shell/Zellij handling
// because -s replaces the command outright (there is nothing left to inject
// into) and the Zellij wrap must see the final argv, not the pre-injection
// one.
func containerCommand(userCmd, extraMounts []string, injection map[string]string, shellMode bool, zellijSession, profName string) []string {
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
	} else {
		command = injectMountFlags(command, extraMounts, injection)
	}
	if zellijSession != "" {
		command = zellij.RunCommand(zellijSession, profName, command)
	}
	return command
}

// injectMountFlags appends <mountFlag> <path> once per extra mount, in -m
// order, when the command's literal first token is a configured key -- no
// path sniffing, honoring ADR-0003/ADR-0009 (the launcher holds no built-in
// program-name knowledge; a user opts a program in via commands.json). An
// empty command leaves the image CMD opaque, so there is nothing to match.
func injectMountFlags(command, extraMounts []string, injection map[string]string) []string {
	if len(command) == 0 || len(injection) == 0 {
		return command
	}
	flag, ok := injection[command[0]]
	if !ok {
		return command
	}
	for _, m := range extraMounts {
		command = append(command, flag, m)
	}
	return command
}
