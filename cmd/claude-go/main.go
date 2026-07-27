// Command claude-go is the Go launcher for contained AI coding sessions.
//
// The container runtime it drives is selected from argv[0] by
// internal/runtime.Select, so no flag the bash launchers lack has to enter the
// CLI surface -- such a flag would itself be a divergence in the unknown-flag
// path. Which basename maps to which runtime is that package's business, not
// this one's.
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
