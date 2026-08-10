package main

// goldencase_test.go is the curated survivor table: each entry is a scenario
// admitted under the retention rule in CONTRIBUTING.md ("Golden tests") and
// docs/adr/0012-curated-golden-contract-matrix.md -- a full run that crosses
// ownership boundaries (host -> plan -> runtime), an ordered user-visible
// contract, or a safety-critical filesystem lifecycle a focused test cannot
// economically prove. Everything else was ported once from the retired
// tests/differential/corpus/*.case files and has since been pruned back to a
// focused test; see the removal ledger in the issue #54 pull request for
// where each removed scenario's coverage now lives.
//
// A surviving entry's Slug still matches its original corpus basename minus
// the .case extension, so a reviewer can still check it against the
// original:
//
//	git show 20e85cb:tests/differential/corpus/24-env-reserved-always-exact.case
//
// 20e85cb is the last commit before the conversion; the corpus was deleted a
// few commits later, so nothing after it resolves.

import (
	"path/filepath"
	"strings"
	"testing"

	"claude-contained/internal/host"
	"claude-contained/internal/layer"
	"claude-contained/internal/plan"
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
	// ImageIDs maps an image reference to the identifier the stubs' `image
	// inspect` arm reports for it. A reference absent from this map is absent
	// from the runtime's image store, which is how a case says "the base image
	// is not built" or "this derived image has not been built yet".
	ImageIDs map[string]string
}

// goldenCase is one surviving scenario.
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
	// must be the common case: most survivors reach the run path and expect
	// runtime args (liveness guard 2 in golden_test.go).
	NoRuntimeArgs bool
	// Terminal forces isTerminal to report a terminal for this case. The
	// driver hands runWith a strings.Reader, which is never a character
	// device, so without this no case could reach a prompt that is gated on
	// having one -- the tooling layer's build confirmation, which fails closed
	// rather than prompting when there is no terminal.
	Terminal bool
	// Configs is the set of tree names this scenario runs in, a subset of
	// {"apple-darwin","docker-darwin","docker-linux"}. A scenario runs only in
	// the configurations that can expose a distinct assembled contract; the
	// retention rule in CONTRIBUTING.md governs the choice.
	Configs []string
	// Admit is the one-line admission reason: the unique cross-boundary or
	// config-specific assembled risk this golden protects. Enforced non-empty
	// by TestGoldenMatrixIsWellFormed.
	Admit string
}

// worktreeGoldenFixture builds a real main repository (fixed basename
// "main-repo") with two linked worktrees ("wt-active", the case's -C target,
// and "wt-hidden", the prune hazard the auto-lock offer exists to protect),
// mirroring the corpus entries that exercise the worktree lock/unlock cycle.
// Fixed basenames mean callers never need Setup's return value to find
// wt-active: filepath.Join(proj, "wt-active") always works.
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

func writeEnvFile(t *testing.T, proj, content string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(proj, ".claude-contained", "env"), content)
}

// mkExtraDir seeds the -m/--mount fixture directory used by mount-mode
// scenarios.
func mkExtraDir(t *testing.T, proj string) {
	t.Helper()
	mustMkdirAll(t, filepath.Join(proj, "extra"))
}

// goldenBaseImageID is the identifier the stub runtime reports for the base
// image in the tooling-layer case. A fixture constant, not a probe: it is one
// of the three hash inputs, so it has to be as fixed as the layer directory's
// own contents for the derived tag to be reproducible.
const goldenBaseImageID = "sha256:base00"

// goldenLayerDockerfile is the layer the tooling-layer case checks in. Its
// bytes are a hash input, so they are a constant rather than written inline
// per case.
const goldenLayerDockerfile = "ARG BASE_IMAGE=claude-contained:latest\n" +
	"FROM ${BASE_IMAGE}\n" +
	"RUN echo layer-marker > /usr/local/share/layer-marker\n"

// writeGoldenLayer checks a tooling layer into the case's project directory at
// the default location and returns its resolved identity, so a case can name
// the derived tag before the launcher computes it -- which is the only way to
// present a derived image as *already built*.
func writeGoldenLayer(t *testing.T, proj string) layer.Identity {
	t.Helper()
	dir := filepath.Join(proj, host.LayerDirName)
	mustWriteFile(t, filepath.Join(dir, "Dockerfile"), goldenLayerDockerfile)
	id, err := layer.Resolve(dir, proj, goldenBaseImageID)
	if err != nil {
		t.Fatalf("resolving the fixture layer: %v", err)
	}
	return id
}

var goldenCases = []goldenCase{
	{
		Slug:    "02-tool-claude-default",
		Desc:    "default command: no positional, image CMD runs",
		Args:    func(proj, home string) []string { return []string{"-N", "-C", proj} },
		Configs: []string{"apple-darwin", "docker-darwin"},
		Admit: "Full ordinary run crosses host->plan->runtime; the Apple-vs-Docker assembled " +
			"argv contract. Linux adds only the host-gateway constant, owned by host-forward.",
	},
	{
		Slug:    "08-ssh-flag",
		Desc:    "-S/--ssh enables SSH agent forwarding",
		Args:    func(proj, home string) []string { return []string{"-N", "-s", "-S", "-C", proj} },
		Configs: []string{"apple-darwin", "docker-darwin", "docker-linux"},
		Admit:   "sshArgs() three-way runtime/platform split; the only scenario needing sshAgent.",
	},
	{
		Slug:    "15-host-forward",
		Desc:    "-H PORT forwards a host port into the container's localhost",
		Args:    func(proj, home string) []string { return []string{"-N", "-s", "-C", proj, "-H", "3845"} },
		Configs: []string{"apple-darwin", "docker-darwin", "docker-linux"},
		Admit: "Apple emits the -H stderr notice (Profile.HostForwardNotice); docker-linux is " +
			"where --add-host host-gateway is emitted; docker-darwin is the no-notice/no-gateway baseline.",
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
		Configs: []string{"docker-darwin"},
		Admit: "Ordered assembled contract: -e overrides the project env file in the container " +
			"command. Platform-independent; parser owned by internal/env.",
	},
	{
		Slug:    "33-zellij-fresh-start",
		Desc:    "--zellij starts a fresh named Zellij session",
		Args:    func(proj, home string) []string { return []string{"-N", "-s", "--zellij", "-C", proj} },
		Configs: []string{"docker-darwin"},
		Admit:   "Zellij session assembly wraps the container command; platform-independent.",
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
		Configs: []string{"docker-darwin"},
		Admit:   "Safety-critical filesystem lock lifecycle observed mid-run and post-run; platform-independent.",
	},
	{
		Slug: "46-account-state-first-run",
		Desc: "first run migrates a regular ~/.claude.json into the shared dir behind a symlink, and copies ~/.gitconfig",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mustWriteFile(t, filepath.Join(home, ".claude.json"), `{"seeded":"account-state"}`+"\n")
			mustWriteFile(t, filepath.Join(home, ".gitconfig"), "[user]\n\tname = Golden Fixture\n")
			return goldenExtras{}
		},
		Args:    func(proj, home string) []string { return []string{"-N", "-s", "-C", proj} },
		Configs: []string{"docker-darwin"},
		Admit: "Safety-critical account-state migration (regular file -> shared dir behind symlink " +
			"+ gitconfig copy); platform-independent.",
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
		Configs: []string{"docker-darwin"},
		Admit: "Safety-critical placeholder sweep across project + every extra mount, sparing " +
			"tracked/non-empty files; platform-independent.",
	},
	{
		Slug: "60-layer-build-confirmed",
		Desc: "a checked-in tooling layer that has not been built is confirmed, built with the base image's resolved ID as BASE_IMAGE, and run in place of the base image",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			writeGoldenLayer(t, proj)
			// Only the base image exists; the derived tag is absent, which is
			// what makes the launcher prompt.
			return goldenExtras{ImageIDs: map[string]string{plan.Image: goldenBaseImageID}}
		},
		Args:     func(proj, home string) []string { return []string{"-N", "-s", "-C", proj} },
		Stdin:    "y\n",
		Terminal: true,
		Configs:  []string{"apple-darwin", "docker-darwin"},
		Admit: "Cross-boundary contract: host layer resolve -> runtime build with resolved " +
			"BASE_IMAGE -> run derived image; build/tag argv differs Apple vs Docker.",
	},
	{
		Slug: "66-attach-by-name-with-command",
		Desc: "-a NAME -- CMD attaches and runs the given command instead of the shell default",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			return goldenExtras{ListOutput: []string{"aic-myproject"}}
		},
		Args:    func(proj, home string) []string { return []string{"-a", "myproject", "--", "npm", "test"} },
		Configs: []string{"apple-darwin", "docker-darwin"},
		Admit:   "Attach exec argv assembly differs Apple vs Docker; end-to-end attach + command operand.",
	},
	{
		// #39: a commands.json entry matching the command's literal first
		// token makes -m append <mountFlag> <path> to the *end* of the
		// container command, once per extra mount -- not inserted after
		// token 0, so the operand is "claude --model sonnet --add-dir
		// <PROJ>/extra" rather than the pre-#22 "claude --add-dir <PROJ>/extra
		// --model sonnet".
		Slug: "67-command-injection-mount-flag",
		Desc: "commands.json makes -m append <mountFlag> to a matching command",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			mkExtraDir(t, proj)
			mustWriteFile(t, filepath.Join(proj, ".claude-contained", "commands.json"),
				`{"claude":{"mountFlag":"--add-dir"},"codex":{"mountFlag":"--add-dir"}}`+"\n")
			return goldenExtras{}
		},
		Args: func(proj, home string) []string {
			return []string{"-N", "-C", proj, "-m", filepath.Join(proj, "extra"), "claude", "--model", "sonnet"}
		},
		Configs: []string{"docker-darwin"},
		Admit: "Assembled contract: commands.json appends mountFlag at the end of the container " +
			"command, once per extra mount; platform-independent.",
	},
	{
		Slug: "68-share-skills-external-target-root",
		Desc: "--share-skills mounts a safe common root for an external absolute directory symlink target",
		Setup: func(t *testing.T, proj, home string) goldenExtras {
			sharedRepo := filepath.Join(filepath.Dir(proj), "shared-repo")
			target := filepath.Join(sharedRepo, ".agents", "skills", "implement")
			mustWriteFile(t, filepath.Join(target, "SKILL.md"), "skill\n")
			mustWriteFile(t, filepath.Join(sharedRepo, "sibling-proof.txt"), "sibling visibility proof\n")
			mustMkdirAll(t, filepath.Join(sharedRepo, "skills"))
			mustSymlink(t, target, filepath.Join(sharedRepo, "skills", "implement"))
			return goldenExtras{}
		},
		Args: func(proj, home string) []string {
			return []string{"-N", "-s", "-C", proj, "--share-skills", filepath.Join(filepath.Dir(proj), "shared-repo", "skills")}
		},
		Configs: []string{"docker-darwin"},
		Admit: "Assembled contract: external absolute-symlink target yields a safe common-root " +
			"read-only mount at path parity; platform-independent.",
	},
}
