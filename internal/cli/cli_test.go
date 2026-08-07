package cli

import (
	"bytes"
	"strings"
	"testing"
)

const testProgName = "claude-contained"

func validateArgs(args ...string) (Config, string, error) {
	cfg := Parse(args, testProgName, false)
	var stderr bytes.Buffer
	err := Validate(&cfg, &stderr)
	return cfg, stderr.String(), err
}

func requireUsageError(t *testing.T, err error) {
	t.Helper()
	var exit *ExitError
	if !asExitError(err, &exit) || exit.Code != ExitUsage {
		t.Fatalf("error = %v, want exit %d", err, ExitUsage)
	}
}

func asExitError(err error, target **ExitError) bool {
	e, ok := err.(*ExitError)
	if ok {
		*target = e
	}
	return ok
}

func TestParseDefersRequiredValueDiagnosticsToValidate(t *testing.T) {
	cases := []struct {
		name string
		flag string
		want string
	}{
		{"short dir", "-C", "error: -C/--dir requires a value\n"},
		{"long dir", "--dir", "error: -C/--dir requires a value\n"},
		{"short mount", "-m", "error: -m/--mount requires a value\n"},
		{"long mount", "--mount", "error: -m/--mount requires a value\n"},
		{"name", "--name", "error: --name requires a value\n"},
		{"session", "--session", "error: --session requires a value\n"},
		{"container runtime", "--container-runtime", "error: --container-runtime requires apple or docker\n"},
		{"build context", "--build-context", "error: --build-context requires a directory\n"},
		{"share skills", "--share-skills", "error: --share-skills requires a value\n"},
		{"short env", "-e", "error: -e/--env requires KEY=VALUE\n"},
		{"long env", "--env", "error: -e/--env requires KEY=VALUE\n"},
		{"publish", "-p", "error: -p requires a value\n"},
		{"host forward", "-H", "error: -H requires a value\n"},
		{"dns", "--dns", "error: --dns requires a value\n"},
		{"allow host", "--allow-host", "error: --allow-host requires a value\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, stderr, err := validateArgs(tc.flag)
			requireUsageError(t, err)
			if stderr != tc.want {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
			if cfg.RebuildMode != "none" {
				t.Errorf("Parse did not return its configuration: %+v", cfg)
			}
		})
	}
}

// A value flag must not silently swallow a following option-looking token as its
// value; the migrated arg-parsing shell case proved `-t --yolo` is a missing
// value, not "tool=--yolo". This exercises the strings.HasPrefix(value, "-")
// branch of recordRequiredValue, distinct from the value=="" (final-arg) branch
// tabled in TestParseDefersRequiredValueDiagnosticsToValidate.
func TestValueFlagDoesNotSwallowAFollowingOption(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"env", []string{"-e", "--yolo"}, "error: -e/--env requires KEY=VALUE\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := validateArgs(tc.args...)
			requireUsageError(t, err)
			if stderr != tc.want {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
		})
	}
}

func TestParseDefersInlineValueDiagnosticsToValidate(t *testing.T) {
	cases := []struct {
		arg  string
		want string
	}{
		{"--dir=", "error: --dir requires a non-empty directory\n"},
		{"--mount=", "error: --mount requires a non-empty directory\n"},
		{"--name=", "error: --name requires a non-empty name\n"},
		{"--session=", "error: --session requires a non-empty name\n"},
		{"--container-runtime=", "error: --container-runtime requires apple or docker\n"},
		{"--build-context=", "error: --build-context requires a non-empty directory\n"},
		{"--share-skills=", "error: --share-skills requires a non-empty directory\n"},
	}

	for _, tc := range cases {
		t.Run(tc.arg, func(t *testing.T) {
			_, stderr, err := validateArgs(tc.arg)
			requireUsageError(t, err)
			if stderr != tc.want {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
		})
	}
}

func TestParseDefersOtherSyntaxDiagnosticsToValidate(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			"new session with name",
			[]string{"--new-session=NAME"},
			"error: --new-session no longer takes a name; use --session=NAME\n",
		},
		{
			"new session with empty name",
			[]string{"--new-session="},
			"error: --new-session no longer takes a name; use --session=NAME\n",
		},
		{
			"unknown flag",
			[]string{"--wat"},
			"error: unknown flag: --wat\n" +
				"       run 'claude-contained --help' for the supported flags\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := validateArgs(tc.args...)
			requireUsageError(t, err)
			if stderr != tc.want {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
		})
	}
}

func TestSyntacticallyAcceptedEmptyInlineFormsRemainAccepted(t *testing.T) {
	cases := []struct {
		arg   string
		check func(Config) bool
	}{
		{"--env=", func(cfg Config) bool { return len(cfg.EnvFlagArgs) == 1 && cfg.EnvFlagArgs[0] == "" }},
		{"--dns=", func(cfg Config) bool { return len(cfg.DNSServers) == 1 && cfg.DNSServers[0] == "" }},
		{"--allow-host=", func(cfg Config) bool { return len(cfg.SrtAllowHosts) == 1 && cfg.SrtAllowHosts[0] == "" }},
		{"--rebuild=", func(cfg Config) bool { return cfg.RebuildMode == "" }},
	}

	for _, tc := range cases {
		t.Run(tc.arg, func(t *testing.T) {
			cfg, stderr, err := validateArgs(tc.arg)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
			if !tc.check(cfg) {
				t.Errorf("Parse did not record %q: %+v", tc.arg, cfg)
			}
		})
	}
}

func TestInlineToolRemainsUnknown(t *testing.T) {
	_, stderr, err := validateArgs("--tool=")
	requireUsageError(t, err)
	want := "error: unknown flag: --tool=\n" +
		"       run 'claude-contained --help' for the supported flags\n"
	if stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestValidatePreservesSemanticDiagnosticOrderAndText(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"new session requires zellij", []string{"--new-session"},
			"error: --new-session is valid only with --zellij\n"},
		{"session requires zellij", []string{"--session", "name"},
			"error: --session is valid only with --zellij\n"},
		{"zellij attach rejects shell", []string{"--zellij", "--attach", "--shell"},
			"error: --zellij --attach cannot be combined with --shell\n"},
		{"zellij attach rejects attach name", []string{"--zellij", "--attach", "name"},
			"error: -a/--attach takes no name with --zellij; use --session=NAME\n"},
		{"name rejects attach", []string{"--attach", "--name", "name"},
			"error: --name cannot be combined with -a/--attach\n" +
				"       --name names a new container; --attach reconnects to an existing one.\n"},
		{"empty zellij session", []string{"--zellij", "--session="},
			"error: --session requires a non-empty name\n"},
		{"invalid zellij session", []string{"--zellij", "--session", "bad/name"},
			"error: invalid Zellij session name: bad/name\n" +
				"       Use only letters, numbers, '_', '.', and '-'; do not start with '-'.\n"},
		{"zellij attach rejects env", []string{"--zellij", "--attach", "--env", "K=V"},
			"error: --env cannot be combined with --zellij --attach\n" +
				"       Attaching starts a Zellij client; the pane keeps the environment it was\n" +
				"       created with, so the variable would silently never reach the tool.\n" +
				"       Set it when the session is created instead.\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := validateArgs(tc.args...)
			requireUsageError(t, err)
			if stderr != tc.want {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
		})
	}
}

func TestValidateRejectsAnEmptyZellijSessionName(t *testing.T) {
	cfg := Config{
		ZellijMode:           true,
		ZellijSessionNameSet: true,
		parse:                parseState{progName: testProgName},
	}
	var stderr bytes.Buffer
	err := Validate(&cfg, &stderr)
	requireUsageError(t, err)
	if want := "error: Zellij session name cannot be empty\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

// The retired zellij-flags suite's "accepts valid names" case used Good_1.2-3,
// which exercises every allowed class at once: mixed-case letters, digits, '_',
// '.', and '-'. The negative twins (empty, slash) are pinned by
// TestValidatePreservesSemanticDiagnosticOrderAndText and golden 31; this pins
// acceptance of the full class, so a narrowed pattern would be caught.
func TestValidateZellijSessionNameAcceptsTheFullAllowedClass(t *testing.T) {
	for _, name := range []string{"Good_1.2-3", "a", "A.B_c-9", "with.dots", "under_score", "1"} {
		var stderr bytes.Buffer
		if err := ValidateZellijSessionName(name, &stderr); err != nil {
			t.Errorf("ValidateZellijSessionName(%q) = %v, want accepted; stderr=%q", name, err, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("ValidateZellijSessionName(%q) wrote stderr %q, want none", name, stderr.String())
		}
	}
}

// The leading-dash branch is distinct from the pattern-mismatch branch the
// slash case covers: '-lead' matches the character class but is refused by the
// explicit name[0]=='-' guard.
func TestValidateZellijSessionNameRejectsLeadingDash(t *testing.T) {
	var stderr bytes.Buffer
	err := ValidateZellijSessionName("-lead", &stderr)
	requireUsageError(t, err)
	want := "error: invalid Zellij session name: -lead\n" +
		"       Use only letters, numbers, '_', '.', and '-'; do not start with '-'.\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestValidateNormalizesCustomContainerName(t *testing.T) {
	cfg, stderr, err := validateArgs("--name", "aic-My Project")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if cfg.CustomContainerName != "aic-my-project" {
		t.Errorf("CustomContainerName = %q, want %q", cfg.CustomContainerName, "aic-my-project")
	}
}

func TestValidateReportsOnlyTheFirstSyntaxFailure(t *testing.T) {
	// "positional first" is no longer a syntax-failure scenario: the first
	// unconsumed token terminates flag parsing and becomes the command, so
	// "project --wat" is a *valid* command (["project", "--wat"]), not two
	// failures racing to be first. See TestFirstPositionalTerminatesFlagParsing.
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown first", []string{"--wat", "project"},
			"error: unknown flag: --wat\n       run 'claude-contained --help' for the supported flags\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := validateArgs(tc.args...)
			requireUsageError(t, err)
			if stderr != tc.want {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
		})
	}
}

func TestSyntaxFailureOutranksSemanticFailure(t *testing.T) {
	_, stderr, err := validateArgs("--new-session", "--wat")
	requireUsageError(t, err)
	want := "error: unknown flag: --wat\n" +
		"       run 'claude-contained --help' for the supported flags\n"
	if stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

func TestParseContinuesAfterSyntaxFailureToRecordRuntimeSelection(t *testing.T) {
	cfg := Parse([]string{"--wat", "--container-runtime=docker"}, testProgName, false)
	if cfg.ContainerRuntime != "docker" {
		t.Errorf("ContainerRuntime = %q, want docker", cfg.ContainerRuntime)
	}
	var stderr bytes.Buffer
	err := Validate(&cfg, &stderr)
	requireUsageError(t, err)
	want := "error: unknown flag: --wat\n" +
		"       run 'claude-contained --help' for the supported flags\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestEffectiveHelpPrecedence(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantHelp bool
		wantErr  string
	}{
		{"help before syntax failure", []string{"--help", "--wat"}, true, ""},
		{"syntax failure before help", []string{"--wat", "--help"}, false,
			"error: unknown flag: --wat\n       run 'claude-contained --help' for the supported flags\n"},
		{"help before semantic failure", []string{"--help", "--new-session"}, true, ""},
		{"consumed help is not help", []string{"-e", "--help"}, false,
			"error: -e/--env requires KEY=VALUE\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, stderr, err := validateArgs(tc.args...)
			if cfg.HelpRequested != tc.wantHelp {
				t.Errorf("HelpRequested = %v, want %v", cfg.HelpRequested, tc.wantHelp)
			}
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
			} else {
				requireUsageError(t, err)
			}
			if stderr != tc.wantErr {
				t.Errorf("stderr = %q, want %q", stderr, tc.wantErr)
			}
		})
	}
}

func TestParsePreservesMergedRuntimeSelectionGrammar(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"required value masks runtime", []string{"--help", "-e", "--container-runtime=docker"}, ""},
		{"malformed runtime leaves following runtime flag", []string{"--help", "--container-runtime", "--container-runtime=docker"}, "docker"},
		{"malformed runtime leaves tool boundary", []string{"--help", "--container-runtime", "--", "--container-runtime=docker"}, ""},
		{"consumed boundary does not stop parsing", []string{"--help", "-e", "--", "--container-runtime=docker"}, "docker"},
		{"empty inline runtime overwrites valid runtime", []string{"--help", "--container-runtime=docker", "--container-runtime="}, ""},
		{"malformed runtime preserves prior runtime", []string{"--help", "--container-runtime=docker", "--container-runtime", "--shell"}, "docker"},
		{"real boundary hides runtime", []string{"--help", "--", "--container-runtime=docker"}, ""},
		{"repeated valid runtime last wins", []string{"--container-runtime=apple", "--help", "--container-runtime=docker"}, "docker"},
		{"runtime after help", []string{"--help", "--container-runtime=docker"}, "docker"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Parse(tc.args, testProgName, false)
			if cfg.ContainerRuntime != tc.want {
				t.Errorf("ContainerRuntime = %q, want %q", cfg.ContainerRuntime, tc.want)
			}
			if !cfg.HelpRequested {
				t.Error("HelpRequested = false, want true")
			}
			var stderr bytes.Buffer
			if err := Validate(&cfg, &stderr); err != nil {
				t.Errorf("effective help should suppress deferred validation: %v", err)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestParseForwardsCommandArgumentsAfterTheFirstRealBoundary(t *testing.T) {
	cfg := Parse([]string{"-s", "--", "--", "--container-runtime=docker", "arg"}, testProgName, false)
	want := []string{"--", "--container-runtime=docker", "arg"}
	if strings.Join(cfg.Command, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("Command = %q, want %q", cfg.Command, want)
	}
	if cfg.ContainerRuntime != "" {
		t.Errorf("ContainerRuntime = %q, want empty", cfg.ContainerRuntime)
	}
}

// TestFirstPositionalTerminatesFlagParsing pins the grammar docs/adr/0009
// describes: the first token not consumed by a flag terminates flag parsing,
// and everything from it onward -- including further dash-leading tokens --
// is the container command, verbatim.
func TestFirstPositionalTerminatesFlagParsing(t *testing.T) {
	joined := func(cmd []string) string { return strings.Join(cmd, "\x00") }

	t.Run("bare positional becomes a command", func(t *testing.T) {
		cfg, stderr, err := validateArgs("npm", "test")
		if err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		want := []string{"npm", "test"}
		if joined(cfg.Command) != joined(want) {
			t.Errorf("Command = %q, want %q", cfg.Command, want)
		}
	})

	t.Run("flags before the command still parse", func(t *testing.T) {
		cfg := Parse([]string{"-C", "/foo", "npm", "test"}, testProgName, false)
		if cfg.ProjectDir != "/foo" {
			t.Errorf("ProjectDir = %q, want /foo", cfg.ProjectDir)
		}
		want := []string{"npm", "test"}
		if joined(cfg.Command) != joined(want) {
			t.Errorf("Command = %q, want %q", cfg.Command, want)
		}
	})

	t.Run("the command owns its own flags", func(t *testing.T) {
		cfg := Parse([]string{"npm", "test", "-C", "/foo"}, testProgName, false)
		if cfg.ProjectDir != "" {
			t.Errorf("ProjectDir = %q, want empty: -C after the command belongs to npm", cfg.ProjectDir)
		}
		want := []string{"npm", "test", "-C", "/foo"}
		if joined(cfg.Command) != joined(want) {
			t.Errorf("Command = %q, want %q", cfg.Command, want)
		}
	})

	t.Run("-- and a bare command are identical", func(t *testing.T) {
		withMarker := Parse([]string{"--", "npm", "test"}, testProgName, false)
		bare := Parse([]string{"npm", "test"}, testProgName, false)
		if joined(withMarker.Command) != joined(bare.Command) {
			t.Errorf("Command = %q, want %q", withMarker.Command, bare.Command)
		}
	})

	t.Run("empty command falls through to the image default", func(t *testing.T) {
		for _, args := range [][]string{{}, {"--"}} {
			cfg, stderr, err := validateArgs(args...)
			if err != nil {
				t.Fatalf("Validate(%v): %v", args, err)
			}
			if stderr != "" {
				t.Errorf("stderr(%v) = %q, want empty", args, stderr)
			}
			if len(cfg.Command) != 0 {
				t.Errorf("Command(%v) = %q, want empty", args, cfg.Command)
			}
		}
	})
}

// TestDeletedSpellingsBecomeMigrationErrors pins -t/-y as removed flags, each
// naming its positional replacement rather than being silently accepted or
// falling into the generic unknown-flag path.
func TestDeletedSpellingsBecomeMigrationErrors(t *testing.T) {
	t.Run("-t names its removal and consumes the value", func(t *testing.T) {
		cfg, stderr, err := validateArgs("-t", "codex")
		requireUsageError(t, err)
		want := "error: -t/--tool is no longer accepted\n" +
			"       name the program positionally:  claude-contained <program>\n"
		if stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
		// The value is consumed, not left to also start a positional command.
		if len(cfg.Command) != 0 {
			t.Errorf("Command = %q, want empty: -t's value must not also be read as a command", cfg.Command)
		}
	})

	t.Run("-y names its removal", func(t *testing.T) {
		_, stderr, err := validateArgs("-y")
		requireUsageError(t, err)
		want := "error: -y/--yolo is no longer accepted\n" +
			"       pass the flag to the program:  claude-contained claude --dangerously-skip-permissions\n"
		if stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	})

	t.Run("-- --model sonnet cannot start a command with a flag", func(t *testing.T) {
		_, stderr, err := validateArgs("--", "--model", "sonnet")
		requireUsageError(t, err)
		want := "error: command cannot start with a flag: --model\n" +
			"       name the program first:  claude-contained claude --model sonnet\n"
		if stderr != want {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	})
}

// TestCommandConflictsWithNoWhereToRun pins each flag that supplies its own
// command (or reconnects instead of running one) as a usage error when a
// command is also given, rather than a silent discard.
func TestCommandConflictsWithNoWhereToRun(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"shell with command", []string{"-s", "npm", "test"},
			"error: -s/--shell cannot be combined with a command\n" +
				"       -s runs a debug shell in place of the container command.\n"},
		{"rebuild with command", []string{"-R", "npm", "test"},
			"error: -R/--rebuild cannot be combined with a command\n" +
				"       --rebuild builds an image and exits; it runs no command.\n" +
				"       select the mode with --rebuild=MODE:  claude-contained --rebuild=full\n"},
		// -a's own optional-value consumption (section 4.6) would otherwise read
		// "npm" as the attach name and fail the pre-existing "zellij-attach-name"
		// check first; "--" keeps the command's first token dash-leading so -a
		// leaves it alone and the command actually reaches this conflict check.
		{"zellij attach with command", []string{"--zellij", "--attach", "--", "npm", "test"},
			"error: --zellij --attach cannot be combined with a command\n" +
				"       Attaching reconnects to an existing session; the command would never run.\n"},
		{"attach with command", []string{"-a", "npm", "test"},
			"error: -a/--attach cannot be combined with a command\n" +
				"       Attaching reconnects to a running container.\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := validateArgs(tc.args...)
			requireUsageError(t, err)
			if stderr != tc.want {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
		})
	}
}

func TestParsePreservesOptionalValueConsumption(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantRebuild string
		wantAttach  string
		wantRuntime string
	}{
		{"rebuild consumes value", []string{"-R", "full", "--container-runtime=docker"}, "full", "", "docker"},
		{"rebuild leaves flag", []string{"-R", "--container-runtime=docker"}, "tools", "", "docker"},
		{"attach consumes value", []string{"-a", "named", "--container-runtime=docker"}, "none", "named", "docker"},
		{"attach leaves flag", []string{"-a", "--container-runtime=docker"}, "none", "", "docker"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Parse(tc.args, testProgName, false)
			if cfg.RebuildMode != tc.wantRebuild || cfg.AttachName != tc.wantAttach || cfg.ContainerRuntime != tc.wantRuntime {
				t.Errorf("Parse = rebuild %q, attach %q, runtime %q; want %q, %q, %q",
					cfg.RebuildMode, cfg.AttachName, cfg.ContainerRuntime,
					tc.wantRebuild, tc.wantAttach, tc.wantRuntime)
			}
		})
	}
}

func TestContainerRuntimeValueIsNotValidatedByCLI(t *testing.T) {
	cfg, stderr, err := validateArgs("--container-runtime=bogus")
	if err != nil {
		t.Fatalf("Validate should accept a runtime name owned by internal/runtime: %v", err)
	}
	if cfg.ContainerRuntime != "bogus" {
		t.Errorf("ContainerRuntime = %q, want %q", cfg.ContainerRuntime, "bogus")
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestRuntimeFlagName(t *testing.T) {
	if RuntimeFlag != "--container-runtime" {
		t.Errorf("RuntimeFlag = %q; the shell suites and USAGE.md name --container-runtime", RuntimeFlag)
	}
}

func TestFlagErrorNamesTheAcceptedRuntimes(t *testing.T) {
	_, stderr, _ := validateArgs("--container-runtime")
	for _, name := range []string{"apple", "docker"} {
		if !strings.Contains(stderr, name) {
			t.Errorf("the --container-runtime error should name %q: %q", name, stderr)
		}
	}
}

func TestBuildContextFlagFormsAndLastOccurrence(t *testing.T) {
	cfg, stderr, err := validateArgs("--build-context", "/a", "--build-context=/b")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if cfg.BuildContext != "/b" {
		t.Errorf("BuildContext = %q, want the last occurrence %q", cfg.BuildContext, "/b")
	}
}

func TestBuildContextValueIsNotValidatedByCLI(t *testing.T) {
	cfg, stderr, err := validateArgs("--build-context", "/definitely/does/not/exist")
	if err != nil {
		t.Fatalf("Validate should accept an unvalidated directory: %v", err)
	}
	if cfg.BuildContext != "/definitely/does/not/exist" {
		t.Errorf("BuildContext = %q, want the raw value", cfg.BuildContext)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestLayerFlagFormsAndLastOccurrence(t *testing.T) {
	cfg, stderr, err := validateArgs("--layer", "/a", "--layer=/b")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if cfg.LayerDir != "/b" {
		t.Errorf("LayerDir = %q, want the last occurrence %q", cfg.LayerDir, "/b")
	}
}

func TestLayerValueIsNotValidatedByCLI(t *testing.T) {
	cfg, stderr, err := validateArgs("--layer", "/definitely/does/not/exist")
	if err != nil {
		t.Fatalf("Validate should accept an unvalidated directory: %v", err)
	}
	if cfg.LayerDir != "/definitely/does/not/exist" {
		t.Errorf("LayerDir = %q, want the raw value", cfg.LayerDir)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

// The asymmetric `what` strings, mirroring --build-context: the separate form
// wants "a directory", the inline form "a non-empty directory".
func TestLayerFlagRequiredValueGrammar(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"separate form with no value", []string{"--layer"}, "error: --layer requires a directory\n"},
		{"inline form with an empty value", []string{"--layer="}, "error: --layer requires a non-empty directory\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := validateArgs(tc.args...)
			requireUsageError(t, err)
			if stderr != tc.want {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
		})
	}
}

func TestLayerBooleanFlags(t *testing.T) {
	cfg, stderr, err := validateArgs("--build-layer")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if stderr != "" || !cfg.BuildLayer || cfg.NoLayer {
		t.Errorf("--build-layer: BuildLayer=%v NoLayer=%v stderr=%q", cfg.BuildLayer, cfg.NoLayer, stderr)
	}

	cfg, stderr, err = validateArgs("--no-layer")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if stderr != "" || !cfg.NoLayer || cfg.BuildLayer {
		t.Errorf("--no-layer: BuildLayer=%v NoLayer=%v stderr=%q", cfg.BuildLayer, cfg.NoLayer, stderr)
	}
}

func TestNoLayerConflictsWithSelectingOrBuildingOne(t *testing.T) {
	want := "error: --no-layer cannot be combined with --layer or --build-layer\n" +
		"       --no-layer runs the base image; the others select or build a tooling layer.\n"

	for _, args := range [][]string{
		{"--no-layer", "--build-layer"},
		{"--no-layer", "--layer", "/some/dir"},
		{"--no-layer", "--layer=/some/dir", "--build-layer"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, stderr, err := validateArgs(args...)
			requireUsageError(t, err)
			if stderr != want {
				t.Errorf("stderr = %q, want %q", stderr, want)
			}
		})
	}
}

// The conflict check is last in ValidateContext, so it is strictly additive:
// an input that already failed an earlier check still reports that earlier
// message, unchanged.
func TestAnEarlierValidationStillWinsOverTheLayerConflict(t *testing.T) {
	_, stderr, err := validateArgs("--session", "review", "--no-layer", "--build-layer")
	requireUsageError(t, err)
	if want := "error: --session is valid only with --zellij\n"; stderr != want {
		t.Errorf("stderr = %q, want the earlier check's message %q", stderr, want)
	}
}

func TestDiagnosticFlagFormsAndLastOccurrence(t *testing.T) {
	cfg, stderr, err := validateArgs(
		"--log-level", "debug",
		"--log-file", "/first",
		"--log-level=warn",
		"--log-file=/second",
		"--log-only",
	)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if cfg.LogLevel != "warn" || !cfg.LogLevelSet {
		t.Errorf("log level = %q, set %v; want warn, true", cfg.LogLevel, cfg.LogLevelSet)
	}
	if cfg.LogFile != "/second" || !cfg.LogOnly {
		t.Errorf("log file/only = %q/%v, want /second/true", cfg.LogFile, cfg.LogOnly)
	}
}

func TestMalformedDiagnosticValueDoesNotReplacePriorSelection(t *testing.T) {
	cfg, stderr, err := validateArgs("--log-level=debug", "--log-level=")
	requireUsageError(t, err)
	if stderr != "error: --log-level requires debug, info, warn, error, or off\n" {
		t.Errorf("stderr = %q", stderr)
	}
	if cfg.LogLevel != "debug" || !cfg.LogLevelSet {
		t.Errorf("selected log level = %q, %v; want prior debug", cfg.LogLevel, cfg.LogLevelSet)
	}
}

func TestDiagnosticFlagsUseDeferredRequiredValueGrammar(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--log-level"}, "error: --log-level requires debug, info, warn, error, or off\n"},
		{[]string{"--log-level="}, "error: --log-level requires debug, info, warn, error, or off\n"},
		{[]string{"--log-file"}, "error: --log-file requires a path\n"},
		{[]string{"--log-file="}, "error: --log-file requires a non-empty path\n"},
	}
	for _, tt := range tests {
		_, stderr, err := validateArgs(tt.args...)
		requireUsageError(t, err)
		if stderr != tt.want {
			t.Errorf("%q stderr = %q, want %q", tt.args, stderr, tt.want)
		}
	}
}

func TestRebuildModeDefaults(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"no flag", []string{"-s"}, "none"},
		{"-R alone", []string{"-R"}, "tools"},
		{"-R before another flag", []string{"-R", "-s"}, "tools"},
		{"-R full", []string{"-R", "full"}, "full"},
		{"--rebuild= empty mode", []string{"--rebuild="}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Parse(tc.argv, testProgName, false)
			if cfg.RebuildMode != tc.want {
				t.Errorf("RebuildMode = %q, want %q", cfg.RebuildMode, tc.want)
			}
		})
	}
}
