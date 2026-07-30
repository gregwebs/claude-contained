// Command claude-contained is the Go launcher for contained AI coding sessions.
//
// The container runtime it drives is selected by internal/runtime.Select:
// --container-runtime, else CLAUDE_CONTAINED_RUNTIME, else an argv[0] basename
// containing "dock", else the host platform (Apple Containers on macOS, Docker
// elsewhere). How each of those maps to a runtime is that package's business,
// not this one's.
//
// The flag is knowingly absent from the bash launchers, which select their
// runtime by being different files. That makes it a real divergence in the
// unknown-flag path, accepted and pinned by a test rather than avoided -- see
// docs/adr/0004-go-launcher-rewrite.md and ticket 11, which drops the second
// launcher name entirely.
package main

import (
	"os"
)

// main does nothing but exit. Every other function returns its status instead
// of calling os.Exit, because deferred cleanup does not run on a direct exit --
// and the launcher's cleanup releases worktree locks that, left behind, would
// block the user's next run.
func main() {
	os.Exit(run(os.Args, os.Stdin, os.Stdout, os.Stderr))
}
