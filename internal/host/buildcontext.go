package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// BuildContextEnvVar names the checkout to build from. CLAUDE_CONTAINED_ is the
// launcher's prefix, and is also a reserved key prefix for the tool process
// (internal/env/env.go), so a project env file cannot set this -- correct, since
// a contained agent must not choose what the host builds.
const BuildContextEnvVar = "CLAUDE_CONTAINED_BUILD_CONTEXT"

// BuildContextSources are the places the image build context can come from, in
// precedence order. Grouped like runtime.Selection so the precedence reads as
// one expression instead of three positional parameters.
type BuildContextSources struct {
	// Flag is --build-context; "" when absent.
	Flag string
	// Env is CLAUDE_CONTAINED_BUILD_CONTEXT; "" when unset.
	Env string
	// Self is this executable's own path, from os.Executable -- deliberately not
	// argv[0]. bash resolves `$0`, which the kernel sets to the full script path
	// even for a PATH lookup, so find_build_context (claude-contained:507-521)
	// could rely on it. A compiled binary's argv[0] is a bare basename in that
	// case, and resolving it would anchor the build context to the current
	// working directory -- i.e. to whichever project the user happens to be in.
	Self string
}

// ErrNoBuildContext reports that no source named a directory holding a
// Dockerfile. The caller owns the message: the advice names the program.
var ErrNoBuildContext = errors.New("no image build context found")

// BadBuildContextError reports that an explicitly named directory holds no
// Dockerfile. An explicit source does not fall through to the next one: it is an
// assertion about the filesystem, and building something other than what the
// user named would be worse than refusing. FromFlag distinguishes the only two
// explicit sources so the caller can name the one actually used.
type BadBuildContextError struct {
	Dir      string
	FromFlag bool
}

func (e *BadBuildContextError) Error() string {
	return fmt.Sprintf("no Dockerfile in %s", e.Dir)
}

// FindBuildContext resolves the directory to hand the container runtime's build
// command, mirroring find_build_context (claude-contained:507-521) for the
// self-location half and adding the two explicit sources ahead of it.
func FindBuildContext(src BuildContextSources) (string, error) {
	for _, explicit := range []struct {
		dir      string
		fromFlag bool
	}{{src.Flag, true}, {src.Env, false}} {
		if explicit.dir == "" {
			continue
		}
		resolved := ResolvePath(explicit.dir)
		if hasDockerfile(resolved) {
			return resolved, nil
		}
		// The literal input, not the resolved path: the user recognizes what
		// they typed.
		return "", &BadBuildContextError{Dir: explicit.dir, FromFlag: explicit.fromFlag}
	}

	if src.Self == "" {
		return "", ErrNoBuildContext
	}
	selfDir := filepath.Dir(ResolvePath(src.Self))
	if hasDockerfile(selfDir) {
		return selfDir, nil
	}
	// The enclosing repository counts only when its root holds a Dockerfile, so a
	// binary installed inside some *other* checkout fails instead of building it.
	if root, err := gitIn(selfDir, "rev-parse", "--show-toplevel"); err == nil && root != "" && hasDockerfile(root) {
		return root, nil
	}
	return "", ErrNoBuildContext
}

// hasDockerfile mirrors bash's `[[ -f "${dir}/Dockerfile" ]]`: Stat follows
// symlinks, as -f does, and a directory named Dockerfile is not a build recipe.
func hasDockerfile(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "Dockerfile"))
	return err == nil && info.Mode().IsRegular()
}
