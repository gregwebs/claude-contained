package blackbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// Launcher is the pair of built launcher artifacts: the primary name and the
// "-docked" compatibility symlink whose argv[0] basename selects the Docker
// runtime. The symlink is a relative link to the primary within the same
// directory, mirroring the Makefile's build rule.
type Launcher struct {
	Primary string
	Docked  string
}

var (
	buildOnce sync.Once
	built     Launcher
	buildErr  error
	buildDir  string
)

// BuildLauncher builds the launcher from the current source once per test
// process into an isolated temporary directory and creates the -docked symlink
// there. It never depends on a pre-existing bin/claude-contained. Call Cleanup
// from TestMain after m.Run() to remove the directory.
func BuildLauncher(t testing.TB) Launcher {
	t.Helper()
	buildOnce.Do(buildLauncher)
	if buildErr != nil {
		t.Fatalf("blackbox: %v", buildErr)
	}
	return built
}

func buildLauncher() {
	if _, err := exec.LookPath("go"); err != nil {
		buildErr = fmt.Errorf("`go` is not on PATH; the black-box harness builds the launcher from source: %w", err)
		return
	}
	root, err := moduleRoot()
	if err != nil {
		buildErr = err
		return
	}
	dir, err := os.MkdirTemp("", "blackbox-launcher-")
	if err != nil {
		buildErr = fmt.Errorf("creating build dir: %w", err)
		return
	}
	buildDir = dir

	primary := filepath.Join(dir, "claude-contained")
	cmd := exec.Command("go", "build", "-o", primary, "./cmd/claude-contained")
	cmd.Dir = root // inherits the ambient env so GOCACHE and friends still apply.
	if out, err := cmd.CombinedOutput(); err != nil {
		buildErr = fmt.Errorf("building the launcher: %w\n%s", err, out)
		return
	}

	docked := filepath.Join(dir, "claude-contained-docked")
	if err := os.Symlink("claude-contained", docked); err != nil {
		buildErr = fmt.Errorf("creating -docked symlink: %w", err)
		return
	}
	built = Launcher{Primary: primary, Docked: docked}
}

// ModuleRoot returns the repository root (the directory holding go.mod), for
// tests that need to read source files such as the embedded help text.
func ModuleRoot(t testing.TB) string {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("blackbox: %v", err)
	}
	return root
}

// Cleanup removes the build directory. It is safe to call when no build ran.
func Cleanup() {
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod, so `go build ./cmd/claude-contained` resolves regardless of which
// package's tests are running.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found above the working directory")
		}
		dir = parent
	}
}
