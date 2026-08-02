package host

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// layerProject returns a project directory, optionally holding a default
// tooling layer at .claude-contained/layer/Dockerfile.
func layerProject(t *testing.T, withLayer bool) string {
	t.Helper()
	proj := realTempDir(t)
	if withLayer {
		dir := filepath.Join(proj, LayerDirName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return proj
}

// layerDir returns a standalone directory, optionally holding a Dockerfile --
// the shape --layer and CLAUDE_CONTAINED_LAYER name directly.
func layerDir(t *testing.T, withDockerfile bool) string {
	t.Helper()
	dir := realTempDir(t)
	if withDockerfile {
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestFindLayerFlagWins(t *testing.T) {
	flagDir := layerDir(t, true)
	envDir := layerDir(t, true)
	proj := layerProject(t, true)

	got, err := FindLayer(LayerSources{Flag: flagDir, Env: envDir, ProjectDir: proj})
	if err != nil {
		t.Fatalf("FindLayer: %v", err)
	}
	if got != flagDir {
		t.Errorf("got %q, want the flag's directory %q", got, flagDir)
	}
}

func TestFindLayerEnvBeatsTheDefault(t *testing.T) {
	envDir := layerDir(t, true)
	proj := layerProject(t, true)

	got, err := FindLayer(LayerSources{Env: envDir, ProjectDir: proj})
	if err != nil {
		t.Fatalf("FindLayer: %v", err)
	}
	if got != envDir {
		t.Errorf("got %q, want the environment's directory %q", got, envDir)
	}
}

func TestFindLayerNamedDirectoryWithoutADockerfileIsAHardError(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		bad := layerDir(t, false)

		_, err := FindLayer(LayerSources{Flag: bad, ProjectDir: layerProject(t, true)})

		var badErr *BadLayerError
		if !errors.As(err, &badErr) {
			t.Fatalf("err = %v, want *BadLayerError", err)
		}
		if !badErr.FromFlag {
			t.Error("FromFlag = false, want true: the flag is the source that failed")
		}
		if badErr.Dir != bad {
			t.Errorf("Dir = %q, want %q", badErr.Dir, bad)
		}
	})

	t.Run("environment", func(t *testing.T) {
		bad := layerDir(t, false)

		_, err := FindLayer(LayerSources{Env: bad, ProjectDir: layerProject(t, true)})

		var badErr *BadLayerError
		if !errors.As(err, &badErr) {
			t.Fatalf("err = %v, want *BadLayerError", err)
		}
		if badErr.FromFlag {
			t.Error("FromFlag = true, want false: the environment variable is the source that failed")
		}
	})
}

func TestFindLayerNonexistentNamedDirectoryIsAHardError(t *testing.T) {
	missing := filepath.Join(realTempDir(t), "does-not-exist")

	_, err := FindLayer(LayerSources{Flag: missing, ProjectDir: layerProject(t, true)})

	var badErr *BadLayerError
	if !errors.As(err, &badErr) {
		t.Fatalf("err = %v, want *BadLayerError: a named directory that is not there is still named", err)
	}
	if badErr.Dir != missing {
		t.Errorf("Dir = %q, want the literal input %q", badErr.Dir, missing)
	}
}

// hasDockerfile's IsRegular check, on the *named* path: a directory called
// Dockerfile is not a build recipe.
func TestFindLayerNamedDirectoryWhoseDockerfileIsADirectory(t *testing.T) {
	dir := realTempDir(t)
	if err := os.Mkdir(filepath.Join(dir, "Dockerfile"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := FindLayer(LayerSources{Flag: dir})

	var badErr *BadLayerError
	if !errors.As(err, &badErr) {
		t.Fatalf("err = %v, want *BadLayerError", err)
	}
}

// The flag's failure is reported even when the environment names a perfectly
// good layer: an explicit source does not fall through to the next one.
func TestFindLayerExplicitSourceDoesNotFallThrough(t *testing.T) {
	badFlag := layerDir(t, false)
	goodEnv := layerDir(t, true)

	_, err := FindLayer(LayerSources{Flag: badFlag, Env: goodEnv, ProjectDir: layerProject(t, true)})

	var badErr *BadLayerError
	if !errors.As(err, &badErr) {
		t.Fatalf("err = %v, want *BadLayerError", err)
	}
	if !badErr.FromFlag || badErr.Dir != badFlag {
		t.Errorf("got %+v, want the flag's own directory reported", badErr)
	}
}

func TestFindLayerDefaultDirectoryIsUsed(t *testing.T) {
	proj := layerProject(t, true)

	got, err := FindLayer(LayerSources{ProjectDir: proj})
	if err != nil {
		t.Fatalf("FindLayer: %v", err)
	}
	if want := filepath.Join(proj, LayerDirName); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFindLayerAbsentDefaultIsNotAnError(t *testing.T) {
	_, err := FindLayer(LayerSources{ProjectDir: layerProject(t, false)})
	if !errors.Is(err, ErrNoLayer) {
		t.Errorf("err = %v, want ErrNoLayer", err)
	}
}

// The documented asymmetry: a *default* directory that exists but holds no
// Dockerfile is "no layer", not an error. Nobody named it.
func TestFindLayerDefaultDirectoryWithoutADockerfileIsNotAnError(t *testing.T) {
	proj := layerProject(t, false)
	if err := os.MkdirAll(filepath.Join(proj, LayerDirName), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := FindLayer(LayerSources{ProjectDir: proj})
	if !errors.Is(err, ErrNoLayer) {
		t.Errorf("err = %v, want ErrNoLayer, not a hard error: the default was never named", err)
	}
}

func TestFindLayerDefaultDockerfileIsADirectory(t *testing.T) {
	proj := layerProject(t, false)
	if err := os.MkdirAll(filepath.Join(proj, LayerDirName, "Dockerfile"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := FindLayer(LayerSources{ProjectDir: proj})
	if !errors.Is(err, ErrNoLayer) {
		t.Errorf("err = %v, want ErrNoLayer", err)
	}
}

func TestFindLayerNoSourcesAtAllIsNoLayer(t *testing.T) {
	_, err := FindLayer(LayerSources{})
	if !errors.Is(err, ErrNoLayer) {
		t.Errorf("err = %v, want ErrNoLayer", err)
	}
}

// The returned path is resolved, matching FindBuildContext: the hash and the
// build context argument both have to name the real directory, not a link to
// it, or two paths to the same layer would produce two derived images.
func TestFindLayerResolvesASymlinkedDirectory(t *testing.T) {
	real := layerDir(t, true)
	link := filepath.Join(realTempDir(t), "layer-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	got, err := FindLayer(LayerSources{Flag: link})
	if err != nil {
		t.Fatalf("FindLayer: %v", err)
	}
	if got != real {
		t.Errorf("got %q, want the resolved directory %q", got, real)
	}
}
