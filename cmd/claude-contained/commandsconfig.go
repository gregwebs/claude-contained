package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"claude-contained/internal/plan"
)

// commandInjectionConfigPath is the project-local mount-flag-injection config,
// a sibling of the project env file under <projectDir>/.claude-contained. It is
// read on the host before container start and, per Decision A (#39), mounted
// read-only into the container, so a running container cannot rewrite it.
func commandInjectionConfigPath(projectDir string) string {
	return filepath.Join(projectDir, ".claude-contained", "commands.json")
}

// readCommandInjection reads and parses
// <projectDir>/.claude-contained/commands.json for the current run. present is
// false when the file does not exist -- an ordinary outcome (the launcher
// never seeds this file; see Decision B, #39), not an error. A
// present-but-malformed file is a hard error naming the path: the file is
// user-created and, per Decision A, read-only inside the container, so a
// parse failure is a user mistake to fail fast on.
func readCommandInjection(projectDir string) (injection map[string]string, present bool, err error) {
	path := commandInjectionConfigPath(projectDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	injection, err = plan.ParseCommandInjection(data)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	return injection, true, nil
}
