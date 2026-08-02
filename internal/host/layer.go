package host

import (
	"errors"
	"fmt"
	"path/filepath"
)

// LayerEnvVar names the tooling layer directory. CLAUDE_CONTAINED_ is the
// launcher's prefix, and is also a reserved key prefix for the tool process
// (internal/env/env.go), so a project env file cannot set this -- correct,
// since a contained agent must not choose what the host builds.
const LayerEnvVar = "CLAUDE_CONTAINED_LAYER"

// LayerDirName is the per-project default, relative to the project directory.
// A dedicated subdirectory rather than .claude-contained/ itself: that
// directory already holds launcher-owned state, and folding a node_modules
// overlay into the build context would rebuild the toolchain on every
// dependency install.
var LayerDirName = filepath.Join(".claude-contained", "layer")

// LayerSources are the places the tooling layer directory can come from, in
// precedence order -- the same rule, and deliberately the same shape, as
// BuildContextSources. The two resolvers live side by side so a reader who
// finds one finds the other.
type LayerSources struct {
	// Flag is --layer; "" when absent.
	Flag string
	// Env is CLAUDE_CONTAINED_LAYER; "" when unset.
	Env string
	// ProjectDir is the resolved project directory; the default lives inside
	// it. Unlike BuildContextSources.Self this is not a fallback *source* --
	// there is no self-location step for a layer, because a layer belongs to
	// the project rather than to the checkout the launcher was built from.
	ProjectDir string
}

// ErrNoLayer reports that this project has no tooling layer. Unlike
// ErrNoBuildContext this is an ordinary outcome, not a failure: most projects
// have no layer and must behave exactly as they did before layers existed.
var ErrNoLayer = errors.New("no tooling layer")

// BadLayerError reports that an explicitly named directory holds no Dockerfile.
// Like BadBuildContextError, an explicit source does not fall through: it is an
// assertion about the filesystem, and running the base image instead would
// produce a container that looks healthy while missing the toolchain the user
// just named a directory for. FromFlag distinguishes the only two explicit
// sources so the caller can name the one actually used.
type BadLayerError struct {
	Dir      string
	FromFlag bool
}

func (e *BadLayerError) Error() string {
	return fmt.Sprintf("no Dockerfile in %s", e.Dir)
}

// FindLayer resolves the project's tooling layer directory, parallel to
// FindBuildContext.
//
// The asymmetry between the explicit sources and the default is the ticket's,
// and it is the whole design: a *named* directory holding no Dockerfile is a
// hard error, while the *default* directory holding none is simply "no layer".
// Nobody named the default, so there is no assertion to violate -- and making
// it an error would break every project that has never heard of layers. The
// caller emits a debug record for the silent case so it is diagnosable.
func FindLayer(src LayerSources) (string, error) {
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
		// they typed (same reason as buildcontext.go's).
		return "", &BadLayerError{Dir: explicit.dir, FromFlag: explicit.fromFlag}
	}

	if src.ProjectDir == "" {
		return "", ErrNoLayer
	}
	dir := ResolvePath(filepath.Join(src.ProjectDir, LayerDirName))
	if hasDockerfile(dir) {
		return dir, nil
	}
	return "", ErrNoLayer
}
