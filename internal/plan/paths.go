package plan

import (
	"path/filepath"

	"claude-contained/internal/host"
)

// hostPaths are the host locations the launcher mounts or mutates, derived from
// HOST_HOME.
//
// Every one of these is built from the raw $HOME, never a resolved form. The
// bash launcher captures HOST_HOME="$HOME" and uses it verbatim, so on a host
// where the home directory sits under a symlink the emitted mount sources keep
// the unresolved spelling while the project directory carries the resolved one.
// Helpfully resolving these would diverge on every home-derived mount at once.
type hostPaths struct {
	Home             string
	ClaudeContained  string
	ClaudeDir        string
	ClaudeProfileDir string
	// ContainerClaudeDir is where the profile appears inside the container:
	// always ~/.claude, regardless of which host directory backs it.
	ContainerClaudeDir string
	CodexDir           string
	GeminiDir          string
	CopilotDir         string
	VibeDir            string
	// AgentsDir is ~/.agents. Unlike the other tool directories it is created
	// only as a side effect of --share-skills (claude-contained:1748) -- there
	// is no other reason to mount or persist it.
	AgentsDir        string
	GitConfig        string
	ClaudeJSON       string
	SharedClaudeJSON string
}

func newHostPaths(h host.State, shareHostClaude bool) hostPaths {
	home := h.Home
	claudeContained := filepath.Join(home, ".claude-contained")
	claudeDir := filepath.Join(home, ".claude")

	// --share-host-claude points the container at the user's normal profile
	// instead of the contained one.
	profileDir := filepath.Join(claudeContained, "claude")
	if shareHostClaude {
		profileDir = claudeDir
	}

	return hostPaths{
		Home:               home,
		ClaudeContained:    claudeContained,
		ClaudeDir:          claudeDir,
		ClaudeProfileDir:   profileDir,
		ContainerClaudeDir: claudeDir,
		CodexDir:           filepath.Join(home, ".codex"),
		GeminiDir:          filepath.Join(home, ".gemini"),
		CopilotDir:         filepath.Join(home, ".copilot"),
		VibeDir:            filepath.Join(home, ".vibe"),
		AgentsDir:          filepath.Join(home, ".agents"),
		GitConfig:          filepath.Join(home, ".gitconfig"),
		ClaudeJSON:         filepath.Join(home, ".claude.json"),
		SharedClaudeJSON:   filepath.Join(claudeContained, ".claude.json"),
	}
}
