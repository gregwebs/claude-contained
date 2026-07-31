package main

// goldencase_test.go is the mechanical transcription of the retired
// tests/differential/corpus/*.case files into a Go table.
// Each entry's Slug matches its corpus basename minus the .case extension, so
// a reviewer can check this file against the originals case by case:
//
//	git show 20e85cb:tests/differential/corpus/24-env-reserved-always-exact.case
//
// 20e85cb is the last commit before the conversion; the corpus was deleted a
// few commits later, so nothing after it resolves.

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claude-contained/internal/host"
)

// goldenExtras is everything a case's Setup may hand back to the driver
// besides the filesystem state it wrote directly: the runtime-liveness stub
// fixtures (ListOutput/InspectEnv, replacing DIFF_LIST_OUTPUT/DIFF_INSPECT_DIR),
// the mid-run snapshot paths (replacing DIFF_SNAPSHOT_PATHS), and any
// environment variable the case itself needs set (replacing case_setup's own
// `export ...` lines).
type goldenExtras struct {
	// ListOutput is what `container list --quiet` / `docker ps --format ...`
	// report as running, one name per line.
	ListOutput []string
	// InspectEnv maps a container name to the KEY=VALUE lines `inspect`
	// reports for it.
	InspectEnv map[string][]string
	// Snapshot are absolute paths that, if they exist, are copied to
	// <path>.mid-run-snapshot by the injected runner just before it returns
	// -- the only window in which the worktree lock is observable.
	Snapshot []string
	// Env is set via t.Setenv after Setup returns, for the handful of cases
	// that exist to exercise an ambient variable (CLAUDE_DNS, the rebuild
	// build-context override) rather than a flag.
	Env map[string]string
}

// goldenCase is one corpus entry, transcribed.
type goldenCase struct {
	Slug string
	Desc string
	// Args builds the argv (without argv[0]) from the case's own project and
	// home directories.
	Args func(proj, home string) []string
	// Setup seeds fixtures and returns whatever the driver needs to install
	// before invoking the launcher. nil means nothing beyond the base
	// fixture.
	Setup func(t *testing.T, proj, home string) goldenExtras
	// Stdin scripts an answer for a prompt the case deliberately exercises.
	// "" (the default) means /dev/null-equivalent: an empty reader.
	Stdin string
	// NoRuntimeArgs is CASE_EXPECT_RUNTIME_ARGS=0's Go name -- the zero value
	// must be the common case, and 48 of 59 entries expect runtime args
	// (liveness guard 2 in golden_test.go).
	NoRuntimeArgs bool
	// HostGOOS restricts the case to hosts whose compile-time GOOS matches
	// (only "darwin", for case 49 -- see its own comment). Empty means no
	// restriction. This is a *host* skip, not a tree skip: what varies is
	// the GOOS the test binary was built for, not the injected `plat`.
	HostGOOS string
}

// worktreeGoldenFixture builds a real main repository (fixed basename
// "main-repo") with two linked worktrees ("wt-active", the case's -C target,
// and "wt-hidden", the prune hazard the auto-lock offer exists to protect),
// mirroring the six corpus entries that exercise the worktree lock/unlock
// cycle (41, 52-56). Fixed basenames mean callers never need Setup's return
// value to find wt-active: filepath.Join(proj, "wt-active") always works.
func worktreeGoldenFixture(t *testing.T, proj string) (mainRepo, hiddenWT, hiddenLockFile string) {
	t.Helper()
	mainRepo = filepath.Join(proj, "main-repo")
	activeWT := filepath.Join(proj, "wt-active")
	hiddenWT = filepath.Join(proj, "wt-hidden")

	mustMkdirAll(t, mainRepo)
	runGitTest(t, mainRepo, "init", "-q", "-b", "main")
	runGitTest(t, mainRepo, "config", "user.email", "test@example.com")
	runGitTest(t, mainRepo, "config", "user.name", "Golden Fixture")
	mustWriteFile(t, filepath.Join(mainRepo, "README.md"), "root\n")
	runGitTest(t, mainRepo, "add", "README.md")
	runGitTest(t, mainRepo, "commit", "-q", "-m", "initial")

	runGitTest(t, mainRepo, "worktree", "add", "-q", "-b", "active-branch", activeWT)
	runGitTest(t, mainRepo, "worktree", "add", "-q", "-b", "hidden-branch", hiddenWT)

	gitDir := strings.TrimSpace(runGitTest(t, hiddenWT, "rev-parse", "--absolute-git-dir"))
	hiddenLockFile = filepath.Join(gitDir, "locked")
	return mainRepo, hiddenWT, hiddenLockFile
}

// activeWorktreePath is worktreeGoldenFixture's fixed -C target.
func activeWorktreePath(proj string) string { return filepath.Join(proj, "wt-active") }

// reliablyDeadPID spawns a throwaway process and waits for it to exit and be
// reaped, mirroring the corpus's `( exit 0 ) & dead=$!; wait "$dead"`: a PID
// that reliably fails a liveness check.
func reliablyDeadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawning throwaway process: %v", err)
	}
	return cmd.Process.Pid
}

func writeEnvFile(t *testing.T, proj, content string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(proj, ".claude-contained", "env"), content)
}

// mkExtraDir seeds the -m/--mount fixture directory every mount-mode case
// (10-13) shares.
func mkExtraDir(t *testing.T, proj string) {
	t.Helper()
	mustMkdirAll(t, filepath.Join(proj, "extra"))
}

var goldenCases = []goldenCase{
	{
		Slug: "01-shell-debug",
		Desc: "debug shell (-s) launches bash instead of the tool",
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-C", proj} },
	},
	{
		Slug: "02-tool-claude-default",
		Desc: "default tool selection (claude, no -t)",
		Args: func(proj, home string) []string { return []string{"-N", "-C", proj} },
	},
	{
		Slug: "03-tool-codex",
		Desc: "tool selection: codex",
		Args: func(proj, home string) []string { return []string{"-N", "-C", proj, "-t", "codex"} },
	},
	{
		Slug: "04-tool-copilot",
		Desc: "tool selection: copilot",
		Args: func(proj, home string) []string { return []string{"-N", "-C", proj, "-t", "copilot"} },
	},
	{
		Slug: "05-tool-gemini",
		Desc: "tool selection: gemini",
		Args: func(proj, home string) []string { return []string{"-N", "-C", proj, "-t", "gemini"} },
	},
	{
		Slug: "06-tool-vibe",
		Desc: "tool selection: vibe",
		Args: func(proj, home string) []string { return []string{"-N", "-C", proj, "-t", "vibe"} },
	},
	{
		Slug:          "07-tool-unknown-rejected",
		Desc:          "unknown tool is rejected before any runtime argument is built",
		Args:          func(proj, home string) []string { return []string{"-N", "-C", proj, "-t", "nonexistent-tool"} },
		NoRuntimeArgs: true,
	},
	{
		Slug: "08-ssh-flag",
		Desc: "-S/--ssh enables SSH agent forwarding",
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-S", "-C", proj} },
	},
	{
		Slug: "09-yolo-flag",
		Desc: "-y/--yolo skips permission prompts",
		Args: func(proj, home string) []string { return []string{"-N", "-y", "-C", proj} },
	},
	{
		Slug:  "10-mount-default-rw",
		Desc:  "-m DIR mounts read-write by default",
		Setup: func(t *testing.T, proj, home string) goldenExtras { mkExtraDir(t, proj); return goldenExtras{} },
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "-m", filepath.Join(proj, "extra")}
		},
	},
	{
		Slug:  "11-mount-ro-suffix",
		Desc:  "-m DIR:ro mounts read-only",
		Setup: func(t *testing.T, proj, home string) goldenExtras { mkExtraDir(t, proj); return goldenExtras{} },
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "-m", filepath.Join(proj, "extra") + ":ro"}
		},
	},
	{
		Slug:  "12-mount-rw-suffix",
		Desc:  "-m DIR:rw forces read-write (overrides --readonly-extras)",
		Setup: func(t *testing.T, proj, home string) goldenExtras { mkExtraDir(t, proj); return goldenExtras{} },
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "--readonly-extras", "-m", filepath.Join(proj, "extra") + ":rw"}
		},
	},
	{
		Slug:  "13-mount-readonly-extras-default",
		Desc:  "--readonly-extras flips the default for an unsuffixed extra mount",
		Setup: func(t *testing.T, proj, home string) goldenExtras { mkExtraDir(t, proj); return goldenExtras{} },
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "--readonly-extras", "-m", filepath.Join(proj, "extra")}
		},
	},
	{
		Slug: "14-port-publish",
		Desc: "-p HOST:CONTAINER publishes a container port to the host",
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-C", proj, "-p", "8080:8080"} },
	},
	{
		Slug: "15-host-forward",
		Desc: "-H PORT forwards a host port into the container's localhost",
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-C", proj, "-H", "3845"} },
	},
	{
		Slug: "16-dns-flag",
		Desc: "--dns overrides the default resolver (repeatable)",
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "--dns", "9.9.9.9", "--dns", "8.8.8.8"}
		},
	},
	{
		Slug: "17-dns-env-var",
		Desc: "CLAUDE_DNS supplies a per-user resolver list when --dns is absent",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			return goldenExtras{Env: map[string]string{"CLAUDE_DNS": "9.9.9.9,8.8.8.8"}}
		},
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-C", proj} },
	},
	{
		Slug: "18-no-sandbox-flag",
		Desc: "--no-sandbox disables the srt sandbox",
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-C", proj, "--no-sandbox"} },
	},
	{
		Slug: "19-allow-host-flag",
		Desc: "--allow-host permits one extra sandbox egress host",
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "--allow-host", "example.com", "--allow-host", "example.org"}
		},
	},
	{
		Slug: "20-env-flag-basic",
		Desc: "-e KEY=VALUE passes an env var to the tool process",
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "-e", "API_URL=http://host.local:8080", "-e", "FOO=bar"}
		},
	},
	{
		Slug: "21-env-file-basic",
		Desc: "project .claude-contained/env file supplies an env var",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			writeEnvFile(t, proj, "FOO=from-file\n")
			return goldenExtras{}
		},
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-C", proj} },
	},
	{
		Slug: "22-env-flag-precedence-over-file",
		Desc: "-e wins over the same key in the project env file",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			writeEnvFile(t, proj, "FOO=from-file\nBAR=only-file\n")
			return goldenExtras{}
		},
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "-e", "FOO=from-flag"}
		},
	},
	{
		Slug: "23-no-project-env-flag",
		Desc: "--no-project-env ignores the project env file",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			writeEnvFile(t, proj, "FOO=from-file\n")
			return goldenExtras{}
		},
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-C", proj, "--no-project-env"} },
	},
	{
		Slug:          "24-env-reserved-always-exact",
		Desc:          "an always-reserved exact-name key (-e) is rejected before any runtime argument is built",
		Args:          func(proj, home string) []string { return []string{"-N", "-s", "-C", proj, "-e", "STAY_ROOT=1"} },
		NoRuntimeArgs: true,
	},
	{
		Slug: "25-env-reserved-always-prefix",
		Desc: "an always-reserved namespace prefix (-e HOST_*) is rejected before any runtime argument is built",
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "-e", "HOST_ANYTHING=1"}
		},
		NoRuntimeArgs: true,
	},
	{
		Slug: "26-env-reserved-file-only",
		Desc: "a file-only-reserved key (LD_PRELOAD) from the project env file is rejected before any runtime argument is built; the same key via -e is fine",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			writeEnvFile(t, proj, "LD_PRELOAD=/tmp/evil.so\n")
			return goldenExtras{}
		},
		Args:          func(proj, home string) []string { return []string{"-N", "-s", "-C", proj} },
		NoRuntimeArgs: true,
	},
	{
		Slug: "27-env-file-only-reserved-key-fine-via-flag",
		Desc: "LD_PRELOAD is only reserved from the project env file; -e LD_PRELOAD=... is accepted",
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "-e", "LD_PRELOAD=/tmp/lib.so"}
		},
	},
	{
		Slug:          "28-env-zellij-attach-refusal",
		Desc:          "--env cannot be combined with --zellij --attach; rejected before any runtime argument is built",
		Args:          func(proj, home string) []string { return []string{"--zellij", "--attach", "-e", "FOO=bar"} },
		NoRuntimeArgs: true,
	},
	{
		Slug: "29-share-skills",
		Desc: "--share-skills mounts a shared skills directory read-only",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mustWriteFile(t, filepath.Join(proj, "skills-src", "example.md"), "skill\n")
			return goldenExtras{}
		},
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "--share-skills", filepath.Join(proj, "skills-src")}
		},
	},
	{
		Slug: "30-share-host-claude",
		Desc: "--share-host-claude mounts host ~/.claude directly instead of the contained profile",
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-C", proj, "--share-host-claude"} },
	},
	{
		Slug: "31-zellij-session-name-invalid",
		Desc: "an invalid --session name is rejected before any runtime argument is built",
		Args: func(proj, home string) []string {
			return []string{"--zellij", "--session", "bad/name", "-N", "-s", "-C", proj}
		},
		NoRuntimeArgs: true,
	},
	{
		Slug:          "32-readonly-project-dir-rejected",
		Desc:          "a :ro suffix on the project directory itself is rejected before any runtime argument is built",
		Args:          func(proj, home string) []string { return []string{"-N", "-s", "-C", proj + ":ro"} },
		NoRuntimeArgs: true,
	},
	{
		Slug: "33-zellij-fresh-start",
		Desc: "--zellij starts a fresh named Zellij session",
		Args: func(proj, home string) []string { return []string{"-N", "-s", "--zellij", "-C", proj} },
	},
	{
		Slug: "34-zellij-session-explicit-name",
		Desc: "--zellij --session NAME starts (or targets) a specifically named session",
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "--zellij", "--session", "my-review", "-C", proj}
		},
	},
	{
		Slug: "35-zellij-attach-single-session",
		Desc: "--zellij --attach reconnects directly when exactly one Zellij session is live",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			return goldenExtras{
				ListOutput: []string{"aic-live1"},
				InspectEnv: map[string][]string{
					"aic-live1": {"CLAUDE_CONTAINED_ZELLIJ=1", "CLAUDE_CONTAINED_ZELLIJ_SESSION=my-session"},
				},
			}
		},
		Args: func(proj, home string) []string { return []string{"-N", "--zellij", "--attach", "-C", proj} },
	},
	{
		Slug: "36-zellij-attach-picker",
		Desc: "--zellij --attach with multiple live sessions prompts a picker (scripted stdin choice)",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			return goldenExtras{
				ListOutput: []string{"aic-live1", "aic-live2"},
				InspectEnv: map[string][]string{
					"aic-live1": {"CLAUDE_CONTAINED_ZELLIJ=1", "CLAUDE_CONTAINED_ZELLIJ_SESSION=alpha"},
					"aic-live2": {"CLAUDE_CONTAINED_ZELLIJ=1", "CLAUDE_CONTAINED_ZELLIJ_SESSION=beta"},
				},
			}
		},
		Args:  func(proj, home string) []string { return []string{"-N", "--zellij", "--attach", "-C", proj} },
		Stdin: "2\n",
	},
	{
		Slug: "37-zellij-new-session-force",
		Desc: "--zellij --new-session starts another session even while a different one is already live",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			return goldenExtras{
				ListOutput: []string{"aic-other"},
				InspectEnv: map[string][]string{
					"aic-other": {"CLAUDE_CONTAINED_ZELLIJ=1", "CLAUDE_CONTAINED_ZELLIJ_SESSION=existing-session"},
				},
			}
		},
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "--zellij", "--new-session", "--session", "my-new-session", "-C", proj}
		},
	},
	{
		Slug: "38-attach-by-name-hit",
		Desc: "-a NAME attaches directly when a matching container is running",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			return goldenExtras{ListOutput: []string{"aic-myproject"}}
		},
		Args: func(proj, home string) []string { return []string{"-a", "myproject"} },
	},
	{
		Slug:          "39-attach-by-name-miss",
		Desc:          "-a NAME with no matching running container is refused rather than silently creating one",
		Args:          func(proj, home string) []string { return []string{"-a", "nonexistent-project"} },
		NoRuntimeArgs: true,
	},
	{
		Slug: "40-attach-picker",
		Desc: "bare -a with multiple running containers prompts a picker (scripted stdin choice)",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			return goldenExtras{ListOutput: []string{"aic-alpha", "aic-beta"}}
		},
		Args:  func(proj, home string) []string { return []string{"-a"} },
		Stdin: "2\n",
	},
	{
		Slug: "41-worktree-lock-unlock-cycle",
		Desc: "a hidden linked worktree is auto-locked for the run and unlocked again on exit (-W skips the prompt)",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			_, _, lockFile := worktreeGoldenFixture(t, proj)
			return goldenExtras{Snapshot: []string{lockFile}}
		},
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-w", "-W", "-C", activeWorktreePath(proj)}
		},
	},
	{
		Slug: "42-share-skills-symlinked-dir-nested",
		Desc: "--share-skills mounts a symlinked directory target and its own nested symlink",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mustMkdirAll(t, filepath.Join(proj, "skills-src"))
			mustMkdirAll(t, filepath.Join(proj, "target-dir"))
			mustWriteFile(t, filepath.Join(proj, "target-dir", "file.md"), "content\n")
			mustMkdirAll(t, filepath.Join(proj, "nested-target"))
			mustWriteFile(t, filepath.Join(proj, "nested-target", "file.md"), "nested\n")
			mustSymlink(t, filepath.Join(proj, "target-dir"), filepath.Join(proj, "skills-src", "dir-link"))
			mustSymlink(t, filepath.Join(proj, "nested-target"), filepath.Join(proj, "target-dir", "nested-link"))
			return goldenExtras{}
		},
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "--share-skills", filepath.Join(proj, "skills-src")}
		},
	},
	{
		Slug: "43-share-skills-writable-mount-conflict",
		Desc: "--share-skills conflicts with an overlapping writable extra mount",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mustWriteFile(t, filepath.Join(proj, "skills-src", "example.md"), "skill\n")
			return goldenExtras{}
		},
		Args: func(proj, home string) []string {
			src := filepath.Join(proj, "skills-src")
			return []string{"-N", "-s", "-C", proj, "-m", src + ":rw", "--share-skills", src}
		},
		NoRuntimeArgs: true,
	},
	{
		Slug: "44-share-skills-readonly-mount-covers",
		Desc: "a read-only extra mount covering an ancestor directory satisfies the shared skills self-mount",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mustWriteFile(t, filepath.Join(proj, "skills-parent", "skills-src", "example.md"), "skill\n")
			return goldenExtras{}
		},
		Args: func(proj, home string) []string {
			parent := filepath.Join(proj, "skills-parent")
			return []string{"-N", "-s", "-C", proj, "-m", parent + ":ro", "--share-skills", filepath.Join(parent, "skills-src")}
		},
	},
	{
		Slug: "45-share-skills-broken-symlink",
		Desc: "a dangling symlink inside the shared skills directory is rejected",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mustMkdirAll(t, filepath.Join(proj, "skills-src"))
			mustSymlink(t, filepath.Join(proj, "skills-src", "does-not-exist"), filepath.Join(proj, "skills-src", "broken-link"))
			return goldenExtras{}
		},
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "--share-skills", filepath.Join(proj, "skills-src")}
		},
		NoRuntimeArgs: true,
	},
	{
		Slug: "46-account-state-first-run",
		Desc: "first run migrates a regular ~/.claude.json into the shared dir behind a symlink, and copies ~/.gitconfig",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mustWriteFile(t, filepath.Join(home, ".claude.json"), `{"seeded":"account-state"}`+"\n")
			mustWriteFile(t, filepath.Join(home, ".gitconfig"), "[user]\n\tname = Golden Fixture\n")
			return goldenExtras{}
		},
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-C", proj} },
	},
	{
		Slug: "47-account-state-already-migrated",
		Desc: "a second run leaves an already-migrated ~/.claude.json symlink and its shared target completely alone",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			shared := filepath.Join(home, ".claude-contained", ".claude.json")
			mustWriteFile(t, shared, `{"seeded":"account-state"}`+"\n")
			mustSymlink(t, shared, filepath.Join(home, ".claude.json"))
			return goldenExtras{}
		},
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-C", proj} },
	},
	{
		Slug: "48-account-state-dangling-symlink",
		Desc: "a ~/.claude.json symlink with nothing behind it is removed and not replaced",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mustSymlink(t, filepath.Join(home, ".claude-contained", ".claude.json"), filepath.Join(home, ".claude.json"))
			return goldenExtras{}
		},
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-C", proj} },
	},
	{
		Slug: "49-node-modules-overlay",
		Desc: "-N overlays container-specific node_modules for the project and read-write extra mounts, skips read-only ones, and announces only the overlays it had to create",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			platform := "linux-" + host.Probe().Arch
			mustWriteFile(t, filepath.Join(proj, "package.json"), `{"name":"proj"}`+"\n")

			overlay := filepath.Join(proj, "extra-rw", ".claude-contained", "node_modules-"+platform, "prebuilt")
			mustMkdirAll(t, overlay)
			mustWriteFile(t, filepath.Join(proj, "extra-rw", "package.json"), `{"name":"extra-rw"}`+"\n")
			mustWriteFile(t, filepath.Join(overlay, "index.js"), "x\n")

			mustMkdirAll(t, filepath.Join(proj, "extra-ro"))
			mustWriteFile(t, filepath.Join(proj, "extra-ro", "package.json"), `{"name":"extra-ro"}`+"\n")
			return goldenExtras{}
		},
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "-m", filepath.Join(proj, "extra-rw"), "-m", filepath.Join(proj, "extra-ro") + ":ro"}
		},
		HostGOOS: "darwin",
	},
	{
		Slug: "50-placeholder-cleanup-mounted-roots",
		Desc: "zero-byte srt placeholder files are swept from the project directory and every extra mount, while tracked and non-empty ones survive",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mustWriteFile(t, filepath.Join(proj, ".bashrc"), "")
			mustWriteFile(t, filepath.Join(proj, ".zshrc"), "keep me\n")
			mustMkdirAll(t, filepath.Join(proj, "extra-rw"))
			mustMkdirAll(t, filepath.Join(proj, "extra-ro"))
			mustWriteFile(t, filepath.Join(proj, "extra-rw", ".gitconfig"), "")
			mustWriteFile(t, filepath.Join(proj, "extra-ro", ".profile"), "")

			tracked := filepath.Join(proj, "tracked-repo")
			mustMkdirAll(t, tracked)
			runGitTest(t, tracked, "init", "-q", "-b", "main")
			mustWriteFile(t, filepath.Join(tracked, ".mcp.json"), "")
			runGitTest(t, tracked, "add", "-f", ".mcp.json")
			return goldenExtras{}
		},
		Args: func(proj, home string) []string {
			return []string{
				"-N", "-s", "-C", proj,
				"-m", filepath.Join(proj, "extra-rw"),
				"-m", filepath.Join(proj, "extra-ro") + ":ro",
				"-m", filepath.Join(proj, "tracked-repo"),
			}
		},
	},
	{
		Slug: "51-stat-semantics-regular-file-guards",
		Desc: "a directory named ~/.gitconfig or package.json is not a regular file, so neither the git-config copy nor the node_modules overlay fires",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mustMkdirAll(t, filepath.Join(home, ".gitconfig"))
			mustMkdirAll(t, filepath.Join(proj, "package.json"))
			return goldenExtras{}
		},
		Args: func(proj, home string) []string { return []string{"-N", "-s", "-C", proj} },
	},
	{
		Slug: "52-worktree-lock-offer-accepted",
		Desc: "the interactive worktree auto-lock offer, accepted: the prune-risk line and the Auto-locked count appear on stdout, and the hidden worktree is locked while the container runs",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			_, _, lockFile := worktreeGoldenFixture(t, proj)
			return goldenExtras{Snapshot: []string{lockFile}}
		},
		Args:  func(proj, home string) []string { return []string{"-N", "-s", "-w", "-C", activeWorktreePath(proj)} },
		Stdin: "Y\n",
	},
	{
		Slug: "53-worktree-lock-offer-declined",
		Desc: "the interactive worktree auto-lock offer, declined: the prune-risk line still appears on stdout, but no lock file is ever written and no Auto-locked line appears",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			_, _, lockFile := worktreeGoldenFixture(t, proj)
			return goldenExtras{Snapshot: []string{lockFile}}
		},
		Args:  func(proj, home string) []string { return []string{"-N", "-s", "-w", "-C", activeWorktreePath(proj)} },
		Stdin: "n\n",
	},
	{
		Slug: "54-worktree-user-lock-untouched",
		Desc: "a worktree the user locked by hand is left completely untouched by the auto-lock offer, which only picks up the other, truly-hidden worktree in the same repository",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mainRepo, _, lockFile := worktreeGoldenFixture(t, proj)
			userWT := filepath.Join(proj, "wt-user-locked")
			runGitTest(t, mainRepo, "worktree", "add", "-q", "-b", "user-branch", userWT)
			runGitTest(t, mainRepo, "worktree", "lock", "--reason", "mine", userWT)
			return goldenExtras{Snapshot: []string{lockFile}}
		},
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-w", "-W", "-C", activeWorktreePath(proj)}
		},
	},
	{
		Slug: "55-worktree-existing-owner-survives",
		Desc: "a worktree lock this run shares with another container's owner token keeps that owner after our own release -- only the last owner leaving actually unlocks",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mainRepo, hiddenWT, lockFile := worktreeGoldenFixture(t, proj)
			runGitTest(t, mainRepo, "worktree", "lock", "--reason", "cc-autolocked-by: aic-other-1111", hiddenWT)
			return goldenExtras{Snapshot: []string{lockFile}}
		},
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-w", "-W", "-C", activeWorktreePath(proj)}
		},
	},
	{
		Slug: "56-worktree-stale-mutex-reclaimed",
		Desc: "a worktree auto-lock mutex left behind by a dead launcher is reclaimed rather than timed out on: the reclaim note appears on stderr, the hidden worktree is still locked, and the mutex directory is gone afterward",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mainRepo, _, lockFile := worktreeGoldenFixture(t, proj)
			dead := reliablyDeadPID(t)
			mutexDir := filepath.Join(mainRepo, ".git", "claude-contained-worktree-locks.lock")
			mustMkdirAll(t, mutexDir)
			mustWriteFile(t, filepath.Join(mutexDir, "owner"), fmt.Sprintf("%d %d\n", dead, time.Now().Unix()))
			return goldenExtras{Snapshot: []string{lockFile}}
		},
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-w", "-W", "-C", activeWorktreePath(proj)}
		},
	},
	{
		Slug: "57-rebuild-tools",
		Desc: "--rebuild refreshes the AI tool layers and exits without a session",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			bc := filepath.Join(proj, "buildctx")
			mustWriteFile(t, filepath.Join(bc, "Dockerfile"), "FROM scratch\n")
			return goldenExtras{Env: map[string]string{"CLAUDE_CONTAINED_BUILD_CONTEXT": bc}}
		},
		Args: func(proj, home string) []string { return []string{"-R"} },
	},
	{
		Slug: "58-rebuild-full",
		Desc: "--rebuild=full pulls the base image and rebuilds without cache, then exits without a session",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			bc := filepath.Join(proj, "buildctx")
			mustWriteFile(t, filepath.Join(bc, "Dockerfile"), "FROM scratch\n")
			return goldenExtras{Env: map[string]string{"CLAUDE_CONTAINED_BUILD_CONTEXT": bc}}
		},
		Args: func(proj, home string) []string { return []string{"--rebuild=full"} },
	},
	{
		Slug:          "59-rebuild-unknown-mode",
		Desc:          "an unknown rebuild mode is rejected before any build runs",
		Args:          func(proj, home string) []string { return []string{"-R", "nonsense"} },
		NoRuntimeArgs: true,
	},
}
