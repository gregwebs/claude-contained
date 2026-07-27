package plan

import (
	"reflect"
	"testing"
	"time"

	"claude-contained/internal/cli"
	"claude-contained/internal/host"
	"claude-contained/internal/runtime"
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
	return runtime.Profile{Name: "claude-contained", DefaultDNS: []string{"1.1.1.1"}}
}

// The headline property from the ticket: a full Answers map up front yields the
// entire Program -- every mutation in order plus the final run -- in one call,
// with no process started and no filesystem touched.
func TestBuildProducesCompleteProgramInOneCall(t *testing.T) {
	cfg := cli.Config{Tool: "claude", ShellMode: true, ContainedNodeModules: true}
	facts := Facts{ProjectDir: "/home/dev/work/app"}

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
		MkdirAll{"/home/dev/.m2"},
		MkdirAll{"/home/dev/.vaadin"},
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
		runtime.MountArg{Src: "/home/dev/.m2", Dst: "/home/dev/.m2"},
		runtime.MountArg{Src: "/home/dev/.vaadin", Dst: "/home/dev/.vaadin"},
		runtime.MountArg{Src: "/home/dev/work/app", Dst: "/home/dev/work/app"},
		runtime.EnvArg{Key: "TZ", Value: "Europe/Helsinki"},
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
	docker := runtime.Profile{Name: "claude-docked"}

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
