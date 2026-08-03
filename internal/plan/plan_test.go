package plan

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"claude-contained/internal/cli"
	"claude-contained/internal/env"
	"claude-contained/internal/host"
	"claude-contained/internal/runtime"
	"claude-contained/internal/zellij"
)

// testHost is a fully synthetic host. Nothing here is probed, which is the
// property under test: Build performs no I/O of its own.
func testHost() host.State {
	return host.State{
		Home:     "/home/dev",
		UID:      "501",
		GID:      "20",
		Arch:     "aarch64",
		Timezone: "Europe/Helsinki",
		Now:      time.Date(2026, 7, 27, 14, 23, 0, 0, time.UTC),
		Memory:   "8g",
	}
}

func appleProfile() runtime.Profile {
	return runtime.Profile{Name: runtime.ProgName, DefaultDNS: []string{"1.1.1.1"}}
}

// The headline property from the ticket: a full Answers map up front yields the
// entire Program -- every mutation in order plus the final run -- in one call,
// with no process started and no filesystem touched.
func TestBuildProducesCompleteProgramInOneCall(t *testing.T) {
	cfg := cli.Config{Tool: "claude", ShellMode: true, ContainedNodeModules: true}
	// The environment arrives already resolved: Build emits it verbatim and
	// derives nothing itself, which is what keeps it pure.
	facts := Facts{
		ProjectDir: "/home/dev/work/app",
		Env:        []env.Pair{{Key: "TZ", Value: "Europe/Helsinki"}},
	}

	program, err := Build(cfg, testHost(), facts, appleProfile(), Answers{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if program.Pending != nil {
		t.Fatalf("unexpected prompt: %+v", program.Pending)
	}
	if program.Run == nil {
		t.Fatal("Run is nil")
	}

	wantSteps := []Step{
		MkdirAll{"/home/dev/.claude"},
		MkdirAll{"/home/dev/.claude-contained"},
		MkdirAll{"/home/dev/.claude-contained/claude"},
		MkdirAll{"/home/dev/.codex"},
		MkdirAll{"/home/dev/.copilot"},
		MkdirAll{"/home/dev/.gemini"},
		MkdirAll{"/home/dev/.vibe"},
		MkdirAll{"/home/dev/.claude/skills"},
		MkdirAll{"/home/dev/.claude-contained/claude/skills"},
		MkdirAll{"/home/dev/.claude/agents"},
		MkdirAll{"/home/dev/.claude-contained/claude/agents"},
		MkdirAll{"/home/dev/.claude/commands"},
		MkdirAll{"/home/dev/.claude-contained/claude/commands"},
		MkdirAll{"/home/dev/.claude/plugins"},
		MkdirAll{"/home/dev/.claude-contained/claude/plugins"},
	}
	if !reflect.DeepEqual(program.Steps, wantSteps) {
		t.Errorf("steps mismatch\n got: %#v\nwant: %#v", program.Steps, wantSteps)
	}

	wantArgs := []runtime.Arg{
		runtime.MemoryArg{Value: "8g"},
		runtime.NameArg{Value: "aic-app-1423"},
		runtime.EnvArg{Key: "HOST_HOME", Value: "/home/dev"},
		runtime.EnvArg{Key: "HOST_UID", Value: "501"},
		runtime.EnvArg{Key: "HOST_GID", Value: "20"},
		runtime.WorkdirArg{Value: "/home/dev/work/app"},
		runtime.MountArg{Src: "/home/dev/.claude-contained/claude", Dst: "/home/dev/.claude"},
		runtime.MountArg{Src: "/home/dev/.codex", Dst: "/home/dev/.codex"},
		runtime.MountArg{Src: "/home/dev/.copilot", Dst: "/home/dev/.copilot"},
		runtime.MountArg{Src: "/home/dev/.gemini", Dst: "/home/dev/.gemini"},
		runtime.MountArg{Src: "/home/dev/.vibe", Dst: "/home/dev/.vibe"},
		runtime.MountArg{Src: "/home/dev/work/app", Dst: "/home/dev/work/app"},
		runtime.EnvArg{Key: "TZ", Value: "Europe/Helsinki"},
		runtime.EnvArg{Key: env.ExplicitKeysMarker, Value: "TZ"},
		runtime.MountArg{Src: "/home/dev/.claude/skills", Dst: "/home/dev/.claude/skills"},
		runtime.MountArg{Src: "/home/dev/.claude/agents", Dst: "/home/dev/.claude/agents"},
		runtime.MountArg{Src: "/home/dev/.claude/commands", Dst: "/home/dev/.claude/commands"},
		runtime.MountArg{Src: "/home/dev/.claude/plugins", Dst: "/home/dev/.claude/plugins"},
		runtime.EnvArg{Key: "GIT_PROTECT_DIRS", Value: "/home/dev/work/app"},
		runtime.DNSArg{Server: "1.1.1.1"},
		runtime.HostGatewayArg{},
		runtime.MountArg{Src: "/home/dev/.claude-contained", Dst: "/home/dev/.claude-contained"},
	}
	if !reflect.DeepEqual(program.Run.Args, wantArgs) {
		t.Errorf("args mismatch\n got: %#v\nwant: %#v", program.Run.Args, wantArgs)
	}

	if program.Run.Image != Image {
		t.Errorf("image = %q, want %q", program.Run.Image, Image)
	}
	// -s runs the wrapper that gives bash a controlling terminal, not bash.
	if want := []string{shellPath}; !reflect.DeepEqual(program.Run.Command, want) {
		t.Errorf("command = %#v, want %#v", program.Run.Command, want)
	}
}

// Declining the worktree prompt has to remove the mount *and* drop the
// repository from GIT_PROTECT_DIRS, which is the sole input to the sandbox's
// writable-path policy. That coupling is why answers are inputs to planning.
func TestBuildWorktreeAnswerDrivesMountAndProtectDirs(t *testing.T) {
	cfg := cli.Config{Tool: "claude", ShellMode: true, ContainedNodeModules: true}
	facts := Facts{ProjectDir: "/w/app", WorktreeMainRepo: "/w/main"}
	h := testHost()

	// With no answer recorded, Build stops and asks.
	first, err := Build(cfg, h, facts, appleProfile(), Answers{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if first.Pending == nil || first.Pending.ID != PromptWorktreeGit {
		t.Fatalf("expected the worktree prompt, got %+v", first.Pending)
	}
	if first.Run != nil {
		t.Error("Run must be nil while a prompt is pending")
	}
	if !first.Pending.Default {
		t.Error("the worktree prompt defaults to yes")
	}

	accepted, err := Build(cfg, h, facts, appleProfile(), Answers{PromptWorktreeGit: true})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !hasMount(accepted.Run.Args, "/w/main/.git") {
		t.Error("accepting should mount the main repository .git")
	}
	if got := envValue(accepted.Run.Args, "GIT_PROTECT_DIRS"); got != "/w/app:/w/main" {
		t.Errorf("GIT_PROTECT_DIRS = %q, want %q", got, "/w/app:/w/main")
	}

	declined, err := Build(cfg, h, facts, appleProfile(), Answers{PromptWorktreeGit: false})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if hasMount(declined.Run.Args, "/w/main/.git") {
		t.Error("declining should not mount the main repository .git")
	}
	if got := envValue(declined.Run.Args, "GIT_PROTECT_DIRS"); got != "/w/app" {
		t.Errorf("GIT_PROTECT_DIRS = %q, want %q", got, "/w/app")
	}
}

// Build is called repeatedly as answers arrive, so the prefix it re-emits must
// be identical or the driver's skip-by-index would apply a step twice.
func TestBuildReplaysAnIdenticalPrefix(t *testing.T) {
	cfg := cli.Config{Tool: "claude", ShellMode: true, ContainedNodeModules: true}
	facts := Facts{ProjectDir: "/w/app", WorktreeMainRepo: "/w/main"}
	h := testHost()

	first, _ := Build(cfg, h, facts, appleProfile(), Answers{})
	second, _ := Build(cfg, h, facts, appleProfile(), Answers{PromptWorktreeGit: true})

	if len(second.Steps) < len(first.Steps) {
		t.Fatalf("second call produced fewer steps (%d) than the first (%d)",
			len(second.Steps), len(first.Steps))
	}
	if !reflect.DeepEqual(first.Steps, second.Steps[:len(first.Steps)]) {
		t.Errorf("prefix diverged\nfirst:  %#v\nsecond: %#v", first.Steps, second.Steps[:len(first.Steps)])
	}
}

// --- Ticket 06: the worktree auto-lock offer ----------------------------

func findWorktreeAutoLock(steps []Step) *WorktreeAutoLock {
	for _, s := range steps {
		if w, ok := s.(WorktreeAutoLock); ok {
			return &w
		}
	}
	return nil
}

func printTexts(steps []Step) []string {
	var out []string
	for _, s := range steps {
		if p, ok := s.(Print); ok {
			out = append(out, p.Text)
		}
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// The offer is a Print step (the prune-risk count Build can compute) plus a
// prompt, and only accepting produces the WorktreeAutoLock step -- the
// "Auto-locked N" count is an I/O result the applier owns, not Build.
func TestBuildOffersWorktreeAutoLock(t *testing.T) {
	cfg := cli.Config{Tool: "claude", ShellMode: true, ContainedNodeModules: true}
	facts := Facts{
		ProjectDir: "/w/app",
		WorktreeLocks: WorktreeLockCandidates{
			Repo:   "/w/app",
			Hidden: []string{"/other/hidden-wt"},
		},
	}
	h := testHost()

	first, err := Build(cfg, h, facts, appleProfile(), Answers{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if first.Pending == nil || first.Pending.ID != PromptWorktreeLocks {
		t.Fatalf("expected the worktree-lock prompt, got %+v", first.Pending)
	}
	if !first.Pending.Default {
		t.Error("the worktree-lock prompt defaults to yes")
	}
	if !containsString(printTexts(first.Steps), "1 linked worktree(s) under /w/app hidden from container (prune risk).") {
		t.Errorf("steps = %#v, want the prune-risk Print", first.Steps)
	}
	if findWorktreeAutoLock(first.Steps) != nil {
		t.Error("WorktreeAutoLock must not appear before the prompt is answered")
	}

	accepted, err := Build(cfg, h, facts, appleProfile(), Answers{PromptWorktreeLocks: true})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	lock := findWorktreeAutoLock(accepted.Steps)
	if lock == nil {
		t.Fatal("expected a WorktreeAutoLock step after accepting")
	}
	if lock.Repo != "/w/app" || !reflect.DeepEqual(lock.Worktrees, []string{"/other/hidden-wt"}) {
		t.Errorf("WorktreeAutoLock = %+v, want Repo=/w/app Worktrees=[/other/hidden-wt]", *lock)
	}
	if lock.Owner == "" {
		t.Error("WorktreeAutoLock.Owner must carry the deduplicated container name")
	}
}

// A declined offer must emit the prune-risk Print and nothing else: no
// WorktreeAutoLock step at all.
func TestBuildDeclinedOfferEmitsPrintOnly(t *testing.T) {
	cfg := cli.Config{Tool: "claude", ShellMode: true, ContainedNodeModules: true}
	facts := Facts{
		ProjectDir: "/w/app",
		WorktreeLocks: WorktreeLockCandidates{
			Repo:   "/w/app",
			Hidden: []string{"/other/hidden-wt"},
		},
	}
	h := testHost()

	declined, err := Build(cfg, h, facts, appleProfile(), Answers{PromptWorktreeLocks: false})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !containsString(printTexts(declined.Steps), "1 linked worktree(s) under /w/app hidden from container (prune risk).") {
		t.Error("declining should still emit the prune-risk Print")
	}
	if findWorktreeAutoLock(declined.Steps) != nil {
		t.Error("declining must not produce a WorktreeAutoLock step")
	}
	if declined.Pending != nil {
		t.Fatalf("unexpected pending prompt: %+v", declined.Pending)
	}
	if declined.Run == nil {
		t.Fatal("Run must be set once the prompt is answered")
	}
}

// -W/--lock-worktrees (cfg.LockWorktrees) skips the prompt entirely and locks
// unconditionally.
func TestBuildLockWorktreesFlagSkipsPrompt(t *testing.T) {
	cfg := cli.Config{Tool: "claude", ShellMode: true, ContainedNodeModules: true, LockWorktrees: true}
	facts := Facts{
		ProjectDir: "/w/app",
		WorktreeLocks: WorktreeLockCandidates{
			Repo:   "/w/app",
			Hidden: []string{"/other/hidden-wt"},
		},
	}

	program, err := Build(cfg, testHost(), facts, appleProfile(), Answers{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if program.Pending != nil {
		t.Fatalf("-W must skip the prompt, got pending %+v", program.Pending)
	}
	if findWorktreeAutoLock(program.Steps) == nil {
		t.Error("-W must produce a WorktreeAutoLock step without asking")
	}
}

// No hidden worktrees at all: neither the Print nor a prompt nor a step.
func TestBuildNoHiddenWorktreesIsSilent(t *testing.T) {
	cfg := cli.Config{Tool: "claude", ShellMode: true, ContainedNodeModules: true}
	facts := Facts{ProjectDir: "/w/app"}

	program, err := Build(cfg, testHost(), facts, appleProfile(), Answers{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if program.Pending != nil {
		t.Fatalf("unexpected pending prompt: %+v", program.Pending)
	}
	for _, txt := range printTexts(program.Steps) {
		if strings.Contains(txt, "hidden from container") {
			t.Errorf("unexpected prune-risk Print: %q", txt)
		}
	}
}

// Accepting the PromptWorktreeGit question changes both which repository is
// scanned and which worktrees count as hidden (mounting the main repo's .git
// makes it visible, which can hide worktrees that were hidden without it) --
// so the two candidate sets are genuinely independent inputs, not derivable
// from one another.
func TestBuildWorktreeGitAnswerSelectsLockCandidateSet(t *testing.T) {
	cfg := cli.Config{Tool: "claude", ShellMode: true, ContainedNodeModules: true}
	facts := Facts{
		ProjectDir:       "/w/app",
		WorktreeMainRepo: "/w/main",
		WorktreeLocks: WorktreeLockCandidates{
			Repo:   "/w/main",
			Hidden: []string{"/w/declined-hidden"},
		},
		WorktreeLocksWithGitMount: WorktreeLockCandidates{
			Repo:   "/w/main",
			Hidden: []string{"/w/accepted-hidden"},
		},
	}
	h := testHost()

	// Decline the .git mount, then accept the lock offer: must see the
	// "declined" candidate set.
	declinedGit, err := Build(cfg, h, facts, appleProfile(),
		Answers{PromptWorktreeGit: false, PromptWorktreeLocks: true})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	lock := findWorktreeAutoLock(declinedGit.Steps)
	if lock == nil || !reflect.DeepEqual(lock.Worktrees, []string{"/w/declined-hidden"}) {
		t.Fatalf("declined-.git run: WorktreeAutoLock = %+v, want Worktrees=[/w/declined-hidden]", lock)
	}

	// Accept the .git mount, then accept the lock offer: must see the
	// "accepted" candidate set.
	acceptedGit, err := Build(cfg, h, facts, appleProfile(),
		Answers{PromptWorktreeGit: true, PromptWorktreeLocks: true})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	lock = findWorktreeAutoLock(acceptedGit.Steps)
	if lock == nil || !reflect.DeepEqual(lock.Worktrees, []string{"/w/accepted-hidden"}) {
		t.Fatalf("accepted-.git run: WorktreeAutoLock = %+v, want Worktrees=[/w/accepted-hidden]", lock)
	}
}

// The replay-prefix guarantee has to survive a second prompt: a driver
// answering the worktree-git question first, then the worktree-lock question,
// must see the first call's steps as an exact prefix of the second's.
func TestBuildReplaysPrefixAcrossTwoPrompts(t *testing.T) {
	cfg := cli.Config{Tool: "claude", ShellMode: true, ContainedNodeModules: true}
	facts := Facts{
		ProjectDir:       "/w/app",
		WorktreeMainRepo: "/w/main",
		WorktreeLocksWithGitMount: WorktreeLockCandidates{
			Repo:   "/w/main",
			Hidden: []string{"/w/hidden"},
		},
	}
	h := testHost()

	first, _ := Build(cfg, h, facts, appleProfile(), Answers{})
	second, _ := Build(cfg, h, facts, appleProfile(), Answers{PromptWorktreeGit: true})
	third, _ := Build(cfg, h, facts, appleProfile(), Answers{PromptWorktreeGit: true, PromptWorktreeLocks: true})

	if len(second.Steps) < len(first.Steps) {
		t.Fatalf("second call produced fewer steps (%d) than the first (%d)", len(second.Steps), len(first.Steps))
	}
	if !reflect.DeepEqual(first.Steps, second.Steps[:len(first.Steps)]) {
		t.Errorf("prefix diverged between round 1 and round 2\nfirst:  %#v\nsecond: %#v", first.Steps, second.Steps[:len(first.Steps)])
	}
	if len(third.Steps) < len(second.Steps) {
		t.Fatalf("third call produced fewer steps (%d) than the second (%d)", len(third.Steps), len(second.Steps))
	}
	if !reflect.DeepEqual(second.Steps, third.Steps[:len(second.Steps)]) {
		t.Errorf("prefix diverged between round 2 and round 3\nsecond: %#v\nthird:  %#v", second.Steps, third.Steps[:len(second.Steps)])
	}
	if third.Pending != nil {
		t.Fatalf("third round should be fully answered, got pending %+v", third.Pending)
	}
}

// Bash reports an unknown tool only after the mounts and mutations above it
// have already been applied, and corpus entry 07 asserts exactly that. So the
// error arrives with a populated Program rather than short-circuiting it.
func TestBuildUnknownToolStillReportsItsSteps(t *testing.T) {
	cfg := cli.Config{Tool: "nope", ContainedNodeModules: true}
	facts := Facts{ProjectDir: "/w/app"}

	program, err := Build(cfg, testHost(), facts, appleProfile(), Answers{})
	var toolErr *ToolError
	if !asToolError(err, &toolErr) {
		t.Fatalf("expected a ToolError, got %v", err)
	}
	if toolErr.Tool != "nope" {
		t.Errorf("ToolError.Tool = %q, want %q", toolErr.Tool, "nope")
	}
	if len(program.Steps) == 0 {
		t.Error("the mutations determined before the tool check must still be reported")
	}
	if program.Run != nil {
		t.Error("Run must be nil when the tool is unknown")
	}
}

// CLAUDE_DNS distinguishes unset (take the runtime default) from set-but-empty
// and the two opt-out spellings, which is a `${CLAUDE_DNS+x}` test in bash.
func TestResolveDNS(t *testing.T) {
	apple := appleProfile()
	docker := runtime.Profile{Name: runtime.ProgName}

	cases := []struct {
		name  string
		cfg   cli.Config
		state host.State
		prof  runtime.Profile
		want  []string
	}{
		{"apple default when unset", cli.Config{}, host.State{}, apple, []string{"1.1.1.1"}},
		{"docker has no default", cli.Config{}, host.State{}, docker, nil},
		{"explicit flag wins", cli.Config{DNSServers: []string{"4.4.4.4"}},
			host.State{DNSEnvSet: true, DNSEnv: "9.9.9.9"}, apple, []string{"4.4.4.4"}},
		{"env list expands", cli.Config{},
			host.State{DNSEnvSet: true, DNSEnv: "9.9.9.9,8.8.8.8"}, apple, []string{"9.9.9.9", "8.8.8.8"}},
		{"system opts out", cli.Config{},
			host.State{DNSEnvSet: true, DNSEnv: "system"}, apple, nil},
		{"empty opts out", cli.Config{},
			host.State{DNSEnvSet: true, DNSEnv: ""}, apple, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveDNS(tc.cfg, tc.state, tc.prof); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("resolveDNS = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// The account-state migration is the mutation that can destroy credentials, so
// all four starting states are pinned. The case that matters most is the
// already-migrated symlink: treating it as a regular file would rename it over
// its own target.
func TestAccountStateSteps(t *testing.T) {
	paths := newHostPaths(host.State{Home: "/home/dev"}, false)

	move := MoveFile{Src: "/home/dev/.claude.json", Dst: "/home/dev/.claude-contained/.claude.json"}
	link := Symlink{Target: "/home/dev/.claude-contained/.claude.json", Link: "/home/dev/.claude.json"}

	cases := []struct {
		name  string
		facts AccountStateFacts
		want  []Step
	}{
		{
			name:  "nothing present anywhere",
			facts: AccountStateFacts{},
			want:  nil,
		},
		{
			name:  "a regular file is relocated and replaced with a link",
			facts: AccountStateFacts{Exists: true, IsRegularFile: true},
			want:  []Step{move, link},
		},
		{
			// The dangerous one. Without the `&& !IsSymlink` guard this would
			// take the migrate branch and rename the link over its own target.
			name: "an already-migrated symlink is left completely alone",
			facts: AccountStateFacts{
				Exists: true, IsRegularFile: true, IsSymlink: true,
				SharedExists: true, SharedIsRegularFile: true,
			},
			want: nil,
		},
		{
			name:  "a broken symlink is removed and not replaced",
			facts: AccountStateFacts{IsSymlink: true},
			want:  []Step{RemoveFile{Path: "/home/dev/.claude.json"}},
		},
		{
			name:  "absent, with a shared file to point at",
			facts: AccountStateFacts{SharedExists: true, SharedIsRegularFile: true},
			want:  []Step{link},
		},
		{
			// bash tests `-e` here, not `-f`: a directory at the shared path
			// still means the link is not broken, so it must survive.
			name:  "a symlink to a directory at the shared path survives",
			facts: AccountStateFacts{Exists: true, IsSymlink: true, SharedExists: true},
			want:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := accountStateSteps(paths, Facts{AccountState: tc.facts}); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("accountStateSteps = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDeduplicateName(t *testing.T) {
	running := []string{"aic-app-1423", "aic-app-1423-2"}
	if got := deduplicateName("aic-app-1423", running); got != "aic-app-1423-3" {
		t.Errorf("deduplicateName = %q, want %q", got, "aic-app-1423-3")
	}
	if got := deduplicateName("aic-other-1423", running); got != "aic-other-1423" {
		t.Errorf("deduplicateName = %q, want it unchanged", got)
	}
}

// stepIndex returns the index of the first step deep-equal to want, or -1.
func stepIndex(steps []Step, want Step) int {
	for i, s := range steps {
		if reflect.DeepEqual(s, want) {
			return i
		}
	}
	return -1
}

// argIndex returns the index of the first arg deep-equal to want, or -1.
func argIndex(args []runtime.Arg, want runtime.Arg) int {
	for i, a := range args {
		if reflect.DeepEqual(a, want) {
			return i
		}
	}
	return -1
}

// TestZellijProgram covers ticket 08's plan.Build insertions (§5.4): the
// session-store mkdirs, the env markers plus Docker labels, and the
// container command wrapper -- all only when a session is set.
func TestZellijProgram(t *testing.T) {
	cfg := cli.Config{Tool: "claude"}
	facts := Facts{ProjectDir: "/home/dev/work/app", ZellijSession: "review"}
	prof := appleProfile()

	program, err := Build(cfg, testHost(), facts, prof, Answers{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if program.Pending != nil {
		t.Fatalf("unexpected prompt: %+v", program.Pending)
	}
	if program.Run == nil {
		t.Fatal("Run is nil")
	}

	// (a) the mkdirs, positioned after the generic shared-state directory and
	// before the extension-resource mkdirs.
	stateIdx := stepIndex(program.Steps, MkdirAll{"/home/dev/.claude-contained"})
	dataIdx := stepIndex(program.Steps, MkdirAll{"/home/dev/.claude-contained/zellij/data"})
	cacheIdx := stepIndex(program.Steps, MkdirAll{"/home/dev/.claude-contained/zellij/cache"})
	extIdx := stepIndex(program.Steps, MkdirAll{"/home/dev/.claude/skills"})
	if stateIdx < 0 || dataIdx < 0 || cacheIdx < 0 || extIdx < 0 {
		t.Fatalf("missing steps: state=%d data=%d cache=%d ext=%d", stateIdx, dataIdx, cacheIdx, extIdx)
	}
	if stateIdx >= dataIdx || dataIdx >= cacheIdx || cacheIdx >= extIdx {
		t.Errorf("mkdir order wrong: state=%d data=%d cache=%d ext=%d", stateIdx, dataIdx, cacheIdx, extIdx)
	}

	// (b) the env markers, in order, after SRT_ALLOW_HOSTS (there is none in
	// this fixture, so just before SSHArg -- which is absent too, so before
	// HostGatewayArg) and (c) the label pair.
	markerIdx := argIndex(program.Run.Args, runtime.EnvArg{Key: zellij.MarkerEnv, Value: "1"})
	sessionIdx := argIndex(program.Run.Args, runtime.EnvArg{Key: zellij.SessionEnv, Value: "review"})
	gatewayIdx := argIndex(program.Run.Args, runtime.HostGatewayArg{})
	if markerIdx < 0 || sessionIdx < 0 || gatewayIdx < 0 {
		t.Fatalf("missing args: marker=%d session=%d gateway=%d", markerIdx, sessionIdx, gatewayIdx)
	}
	if markerIdx >= sessionIdx || sessionIdx >= gatewayIdx {
		t.Errorf("env marker order wrong: marker=%d session=%d gateway=%d", markerIdx, sessionIdx, gatewayIdx)
	}

	labelMarkerIdx := argIndex(program.Run.Args, runtime.LabelArg{Key: zellij.LabelMarker, Value: "1"})
	labelSessionIdx := argIndex(program.Run.Args, runtime.LabelArg{Key: zellij.LabelSession, Value: "review"})
	if labelMarkerIdx < 0 || labelSessionIdx < 0 {
		t.Fatalf("missing label args: marker=%d session=%d", labelMarkerIdx, labelSessionIdx)
	}
	if labelMarkerIdx != sessionIdx+1 || labelSessionIdx != labelMarkerIdx+1 {
		t.Errorf("labels not immediately after env markers: marker=%d session=%d labelMarker=%d labelSession=%d",
			markerIdx, sessionIdx, labelMarkerIdx, labelSessionIdx)
	}

	// (d) the container command wrapper.
	want := zellij.RunCommand("review", prof.Name, []string{"claude"})
	if !reflect.DeepEqual(program.Run.Command, want) {
		t.Errorf("Command = %#v, want %#v", program.Run.Command, want)
	}
}

// TestZellijShellIsBashNotShellRun pins risk 13: under Zellij, --shell is
// plain bash, never /usr/local/bin/shell-run.
func TestZellijShellIsBashNotShellRun(t *testing.T) {
	cfg := cli.Config{Tool: "claude", ShellMode: true}
	facts := Facts{ProjectDir: "/home/dev/work/app", ZellijSession: "review"}

	program, err := Build(cfg, testHost(), facts, appleProfile(), Answers{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := zellij.RunCommand("review", "claude-contained", []string{"bash"})
	if !reflect.DeepEqual(program.Run.Command, want) {
		t.Errorf("Command = %#v, want %#v", program.Run.Command, want)
	}
	for _, tok := range program.Run.Command {
		if strings.Contains(tok, "/usr/local/bin/shell-run") {
			t.Errorf("Command contains shell-run: %#v", program.Run.Command)
		}
	}
}

// TestNoZellijArgsWithoutSession guards against a nil-vs-empty mix-up: an
// unset ZellijSession must produce no marker env, no labels, no zellij
// mkdirs, and an unwrapped Run.Command.
func TestNoZellijArgsWithoutSession(t *testing.T) {
	cfg := cli.Config{Tool: "claude"}
	facts := Facts{ProjectDir: "/home/dev/work/app"}

	program, err := Build(cfg, testHost(), facts, appleProfile(), Answers{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if stepIndex(program.Steps, MkdirAll{"/home/dev/.claude-contained/zellij/data"}) >= 0 {
		t.Error("zellij data mkdir present without a session")
	}
	if stepIndex(program.Steps, MkdirAll{"/home/dev/.claude-contained/zellij/cache"}) >= 0 {
		t.Error("zellij cache mkdir present without a session")
	}
	if envValue(program.Run.Args, zellij.MarkerEnv) != "" {
		t.Error("Zellij marker env present without a session")
	}
	if envValue(program.Run.Args, zellij.SessionEnv) != "" {
		t.Error("Zellij session env present without a session")
	}
	for _, a := range program.Run.Args {
		if _, ok := a.(runtime.LabelArg); ok {
			t.Errorf("unexpected LabelArg without a session: %#v", a)
		}
	}
	want := []string{"claude"}
	if !reflect.DeepEqual(program.Run.Command, want) {
		t.Errorf("Command = %#v, want %#v (unwrapped)", program.Run.Command, want)
	}
}

func hasMount(args []runtime.Arg, src string) bool {
	for _, a := range args {
		if m, ok := a.(runtime.MountArg); ok && m.Src == src {
			return true
		}
	}
	return false
}

func envValue(args []runtime.Arg, key string) string {
	for _, a := range args {
		if e, ok := a.(runtime.EnvArg); ok && e.Key == key {
			return e.Value
		}
	}
	return ""
}

func asToolError(err error, target **ToolError) bool {
	te, ok := err.(*ToolError)
	if ok {
		*target = te
	}
	return ok
}

// The -H capability notice. It is a Print step rather than direct output because
// Build is pure and resumable: its prefix is re-emitted on every prompt round, so
// printing directly would repeat the notice once per round.
func TestHostForwardNoticeIsEmittedForApple(t *testing.T) {
	prof := appleProfile()
	prof.HostForwardNotice = []string{"first", "second"}

	cfg := cli.Config{Tool: "claude", ShellMode: true, HostForwards: []string{"3845"}}
	program, err := Build(cfg, testHost(), Facts{ProjectDir: "/home/dev/work/app"}, prof, Answers{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []Step{Print{Text: "first", Stderr: true}, Print{Text: "second", Stderr: true}}
	if got := printSteps(program.Steps); !reflect.DeepEqual(got, want) {
		t.Errorf("notice steps = %#v, want %#v", got, want)
	}
	// The env var is still emitted: this is a notice, not a refusal.
	if !hasEnvArg(program.Run.Args, "HOST_FORWARD_PORTS", "3845") {
		t.Error("HOST_FORWARD_PORTS was not emitted")
	}
}

// Position inside stderr is load-bearing: bash prints the notice at :1799, before
// the tool warning (:1882), the shared-skills lines (:1906) and the node_modules
// notice (:1939). Any other position is a corpus diff for a case that combines
// them.
func TestHostForwardNoticePrecedesTheOtherNotices(t *testing.T) {
	prof := appleProfile()
	prof.HostForwardNotice = []string{"host-forward notice"}

	// -t vibe -y produces the tool warning; ContainedNodeModules produces the
	// node_modules notice once the project looks like a Node project.
	cfg := cli.Config{Tool: "vibe", YoloMode: true, ShellMode: true, HostForwards: []string{"3845"}}
	program, err := Build(cfg, testHost(), Facts{ProjectDir: "/home/dev/work/app"}, prof, Answers{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	prints := printSteps(program.Steps)
	if len(prints) < 2 {
		t.Fatalf("expected the notice and the tool warning, got %#v", prints)
	}
	if prints[0].(Print).Text != "host-forward notice" {
		t.Errorf("the -H notice must come first, got %#v", prints)
	}
}

func TestHostForwardNoticeAbsentForDocker(t *testing.T) {
	// The Docker profile carries no notice, which is the whole capability
	// difference: it reaches host services bound to 127.0.0.1.
	prof := runtime.Profile{Name: runtime.ProgName}

	cfg := cli.Config{Tool: "claude", ShellMode: true, HostForwards: []string{"3845"}}
	program, err := Build(cfg, testHost(), Facts{ProjectDir: "/home/dev/work/app"}, prof, Answers{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if got := printSteps(program.Steps); len(got) != 0 {
		t.Errorf("Docker should print nothing for -H, got %#v", got)
	}
	if !hasEnvArg(program.Run.Args, "HOST_FORWARD_PORTS", "3845") {
		t.Error("HOST_FORWARD_PORTS was not emitted")
	}
}

func TestNoHostForwardNoticeWithoutFlag(t *testing.T) {
	prof := appleProfile()
	prof.HostForwardNotice = []string{"should not appear"}

	cfg := cli.Config{Tool: "claude", ShellMode: true}
	program, err := Build(cfg, testHost(), Facts{ProjectDir: "/home/dev/work/app"}, prof, Answers{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := printSteps(program.Steps); len(got) != 0 {
		t.Errorf("no -H means no notice, got %#v", got)
	}
}

// A derived image replaces the base image in the run spec and changes nothing
// else. The DeepEqual on Args is the load-bearing half: checklist item 12 says
// a project with no layer produces byte-identical arguments, and this pins the
// converse -- a project *with* one moves only the image operand.
func TestDerivedImageReplacesOnlyTheImage(t *testing.T) {
	cfg := cli.Config{Tool: "claude", ShellMode: true, ContainedNodeModules: true}
	base := Facts{ProjectDir: "/home/dev/work/app"}
	derived := base
	derived.DerivedImage = "claude-contained-layer:app-0123456789abcdef0123456789abcdef"

	baseProgram, err := Build(cfg, testHost(), base, appleProfile(), Answers{})
	if err != nil {
		t.Fatalf("Build (base): %v", err)
	}
	derivedProgram, err := Build(cfg, testHost(), derived, appleProfile(), Answers{})
	if err != nil {
		t.Fatalf("Build (derived): %v", err)
	}

	if baseProgram.Run.Image != Image {
		t.Errorf("an empty DerivedImage must yield %q, got %q", Image, baseProgram.Run.Image)
	}
	if derivedProgram.Run.Image != derived.DerivedImage {
		t.Errorf("Image = %q, want %q", derivedProgram.Run.Image, derived.DerivedImage)
	}
	if !reflect.DeepEqual(baseProgram.Run.Args, derivedProgram.Run.Args) {
		t.Errorf("nothing but the image may move:\nbase:    %#v\nderived: %#v",
			baseProgram.Run.Args, derivedProgram.Run.Args)
	}
	if !reflect.DeepEqual(baseProgram.Run.Command, derivedProgram.Run.Command) {
		t.Errorf("the container command must not move:\nbase:    %#v\nderived: %#v",
			baseProgram.Run.Command, derivedProgram.Run.Command)
	}
}

func printSteps(steps []Step) []Step {
	var out []Step
	for _, s := range steps {
		if _, ok := s.(Print); ok {
			out = append(out, s)
		}
	}
	return out
}

func hasEnvArg(args []runtime.Arg, key, value string) bool {
	for _, a := range args {
		if e, ok := a.(runtime.EnvArg); ok && e.Key == key && e.Value == value {
			return true
		}
	}
	return false
}
