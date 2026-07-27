package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// State is everything about the host that plan building is allowed to see. It
// is captured once, up front, so that planning itself can stay a pure function
// of its inputs -- including the clock, because the container name embeds
// `date +%H%M` (claude-contained:1537).
type State struct {
	Home     string
	UID      string
	GID      string
	Arch     string
	Timezone string
	Now      time.Time
	// GHToken is AI_GH_TOKEN, which the launcher turns into GH_TOKEN for the
	// tool process (claude-contained:1420).
	GHToken string
	// Memory is CLAUDE_MEMORY, defaulted to 8g (claude-contained:1517).
	Memory string
	// DNSEnv mirrors bash's `${CLAUDE_DNS+x}` test: DNSEnvSet distinguishes
	// "unset" (take the runtime default) from "set but empty" (no DNS flags).
	DNSEnv    string
	DNSEnvSet bool
	// ShareHostClaude reflects CLAUDE_CONTAINED_SHARE_HOST_CLAUDE=1
	// (claude-contained:592).
	ShareHostClaude bool
}

// Probe captures host state. HOME comes from the environment rather than the
// passwd database on purpose: the test harnesses override HOME to redirect
// every mount and mutation into a fixture, and user.Current() would ignore that
// and reach into the developer's real home directory.
func Probe() State {
	memory := os.Getenv("CLAUDE_MEMORY")
	if memory == "" {
		memory = "8g"
	}

	dnsEnv, dnsSet := os.LookupEnv("CLAUDE_DNS")

	return State{
		Home:            os.Getenv("HOME"),
		UID:             strconv.Itoa(os.Getuid()),
		GID:             strconv.Itoa(os.Getgid()),
		Arch:            containerArch(),
		Timezone:        Timezone(),
		Now:             time.Now(),
		GHToken:         os.Getenv("AI_GH_TOKEN"),
		Memory:          memory,
		DNSEnv:          dnsEnv,
		DNSEnvSet:       dnsSet,
		ShareHostClaude: os.Getenv("CLAUDE_CONTAINED_SHARE_HOST_CLAUDE") == "1",
	}
}

// containerArch mirrors container_arch (claude-contained:349-355): `uname -m`
// with macOS's arm64 normalized to the Linux spelling.
func containerArch() string {
	out, err := exec.Command("uname", "-m").Output()
	if err != nil {
		return ""
	}
	arch := strings.TrimSpace(string(out))
	if arch == "arm64" {
		return "aarch64"
	}
	return arch
}

// Timezone mirrors host_timezone (claude-contained:491-502): $TZ wins, else a
// single (non-recursive) readlink of /etc/localtime with everything up to and
// including the first "/zoneinfo/" stripped. Anything else yields "".
func Timezone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	link, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	const marker = "/zoneinfo/"
	if i := strings.Index(link, marker); i >= 0 {
		return link[i+len(marker):]
	}
	return ""
}

// WorktreeMainRepo mirrors get_worktree_main_repo (claude-contained:1046-1061).
// It reports the main repository root only when dir is a linked worktree, which
// bash detects by .git being a *file* rather than a directory.
func WorktreeMainRepo(dir string) string {
	// Stat, not Lstat: bash's test is `[[ -f "$git_file" ]]`, which follows
	// symlinks. A .git that is a symlink to a worktree pointer file still marks
	// a linked worktree, and missing that would silently drop both the .git
	// mount and the repository's GIT_PROTECT_DIRS entry.
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}

	commonDir, err := gitIn(dir, "rev-parse", "--git-common-dir")
	if err != nil || commonDir == "" || commonDir == ".git" {
		return ""
	}
	return filepath.Dir(ResolvePath(commonDir))
}

// MainWorktreeRepoRoot mirrors get_main_worktree_repo_root
// (claude-contained:1063-1074): the repository root, but only when dir sits in
// the *main* worktree rather than a linked one.
func MainWorktreeRepoRoot(dir string) string {
	repoRoot, err := gitIn(dir, "rev-parse", "--show-toplevel")
	if err != nil || repoRoot == "" {
		return ""
	}
	if info, err := os.Stat(filepath.Join(repoRoot, ".git")); err != nil || !info.IsDir() {
		return ""
	}

	commonDir, err := gitIn(repoRoot, "rev-parse", "--git-common-dir")
	if err != nil || commonDir == "" {
		return ""
	}

	commonAbs := commonDir
	if !filepath.IsAbs(commonAbs) {
		commonAbs = filepath.Join(repoRoot, commonAbs)
	}
	if ResolvePath(commonAbs) == ResolvePath(filepath.Join(repoRoot, ".git")) {
		return repoRoot
	}
	return ""
}

func gitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
