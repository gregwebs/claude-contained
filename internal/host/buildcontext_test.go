package host

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// contextDir returns a fresh directory, optionally holding a Dockerfile, for
// use as one of BuildContextSources' inputs.
func contextDir(t *testing.T, withDockerfile bool) string {
	t.Helper()
	dir := realTempDir(t)
	if withDockerfile {
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// gitRepoWithDockerfile builds a real git repository whose root optionally
// holds a Dockerfile, and returns the repo root plus a "bin" subdirectory
// inside it -- the shape rank 4 (the enclosing repository) resolves through.
func gitRepoWithDockerfile(t *testing.T, withDockerfile bool) (repoRoot, binDir string) {
	t.Helper()
	repoRoot = realTempDir(t)
	if out, err := exec.Command("git", "-C", repoRoot, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if withDockerfile {
		if err := os.WriteFile(filepath.Join(repoRoot, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	binDir = filepath.Join(repoRoot, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return repoRoot, binDir
}

func TestBuildContextFlagWins(t *testing.T) {
	flagDir := contextDir(t, true)
	envDir := contextDir(t, true)
	_, selfBin := gitRepoWithDockerfile(t, true)

	got, err := FindBuildContext(BuildContextSources{
		Flag: flagDir,
		Env:  envDir,
		Self: filepath.Join(selfBin, "claude-go"),
	})
	if err != nil {
		t.Fatalf("FindBuildContext: %v", err)
	}
	if got != flagDir {
		t.Errorf("got %q, want the flag's directory %q", got, flagDir)
	}
}

func TestBuildContextEnvBeatsSelfLocation(t *testing.T) {
	envDir := contextDir(t, true)
	_, selfBin := gitRepoWithDockerfile(t, true)

	got, err := FindBuildContext(BuildContextSources{
		Env:  envDir,
		Self: filepath.Join(selfBin, "claude-go"),
	})
	if err != nil {
		t.Fatalf("FindBuildContext: %v", err)
	}
	if got != envDir {
		t.Errorf("got %q, want the env directory %q", got, envDir)
	}
}

func TestBuildContextFromExecutableDirectory(t *testing.T) {
	dir := contextDir(t, true)
	exe := filepath.Join(dir, "claude-go")

	got, err := FindBuildContext(BuildContextSources{Self: exe})
	if err != nil {
		t.Fatalf("FindBuildContext: %v", err)
	}
	if got != dir {
		t.Errorf("got %q, want the executable's own directory %q", got, dir)
	}
}

func TestBuildContextFromEnclosingRepository(t *testing.T) {
	repoRoot, binDir := gitRepoWithDockerfile(t, true)
	exe := filepath.Join(binDir, "claude-go")

	got, err := FindBuildContext(BuildContextSources{Self: exe})
	if err != nil {
		t.Fatalf("FindBuildContext: %v", err)
	}
	if got != repoRoot {
		t.Errorf("got %q, want the enclosing repository root %q", got, repoRoot)
	}
}

func TestBuildContextRepositoryWithoutDockerfileIsNotAContext(t *testing.T) {
	_, binDir := gitRepoWithDockerfile(t, false)
	exe := filepath.Join(binDir, "claude-go")

	_, err := FindBuildContext(BuildContextSources{Self: exe})
	if !errors.Is(err, ErrNoBuildContext) {
		t.Errorf("err = %v, want ErrNoBuildContext", err)
	}
}

func TestBuildContextResolvesSymlinkedInstall(t *testing.T) {
	repoRoot, binDir := gitRepoWithDockerfile(t, true)
	realExe := filepath.Join(binDir, "claude-go")
	if err := os.WriteFile(realExe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	linkDir := realTempDir(t)
	link := filepath.Join(linkDir, "claude-go")
	if err := os.Symlink(realExe, link); err != nil {
		t.Fatal(err)
	}

	got, err := FindBuildContext(BuildContextSources{Self: link})
	if err != nil {
		t.Fatalf("FindBuildContext: %v", err)
	}
	if got != repoRoot {
		t.Errorf("got %q, want the checkout the symlink resolves into %q", got, repoRoot)
	}
}

func TestBuildContextExplicitSourceDoesNotFallThrough(t *testing.T) {
	badFlagDir := contextDir(t, false)
	goodEnvDir := contextDir(t, true)
	_, selfBin := gitRepoWithDockerfile(t, true)

	_, err := FindBuildContext(BuildContextSources{
		Flag: badFlagDir,
		Env:  goodEnvDir,
		Self: filepath.Join(selfBin, "claude-go"),
	})

	var bad *BadBuildContextError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want *BadBuildContextError", err)
	}
	if !bad.FromFlag {
		t.Error("FromFlag = false, want true: the flag is the source that failed")
	}
	if bad.Dir != badFlagDir {
		t.Errorf("Dir = %q, want %q", bad.Dir, badFlagDir)
	}
}

func TestBuildContextEnvSourceIsReported(t *testing.T) {
	badEnvDir := contextDir(t, false)

	_, err := FindBuildContext(BuildContextSources{Env: badEnvDir})

	var bad *BadBuildContextError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want *BadBuildContextError", err)
	}
	if bad.FromFlag {
		t.Error("FromFlag = true, want false: the environment variable is the source that failed")
	}
	if bad.Dir != badEnvDir {
		t.Errorf("Dir = %q, want %q", bad.Dir, badEnvDir)
	}
}

func TestBuildContextDockerfileMustBeARegularFile(t *testing.T) {
	dir := realTempDir(t)
	if err := os.Mkdir(filepath.Join(dir, "Dockerfile"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := FindBuildContext(BuildContextSources{Flag: dir})

	var bad *BadBuildContextError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want *BadBuildContextError: a directory named Dockerfile is not a build recipe", err)
	}
}

func TestBuildContextEmptySelfIsNotResolved(t *testing.T) {
	_, err := FindBuildContext(BuildContextSources{})
	if !errors.Is(err, ErrNoBuildContext) {
		t.Errorf("err = %v, want ErrNoBuildContext", err)
	}
}

func TestBuildContextErrorReportsTheLiteralInput(t *testing.T) {
	badDir := contextDir(t, false)
	link := filepath.Join(realTempDir(t), "link-to-bad")
	if err := os.Symlink(badDir, link); err != nil {
		t.Fatal(err)
	}

	_, err := FindBuildContext(BuildContextSources{Flag: link})

	var bad *BadBuildContextError
	if !errors.As(err, &bad) {
		t.Fatalf("err = %v, want *BadBuildContextError", err)
	}
	if bad.Dir != link {
		t.Errorf("Dir = %q, want the literal input %q, not its resolved form", bad.Dir, link)
	}
}
