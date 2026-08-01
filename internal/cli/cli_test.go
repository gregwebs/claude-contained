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
		{"short tool", "-t", "error: -t/--tool requires a value\n"},
		{"long tool", "--tool", "error: -t/--tool requires a value\n"},
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
			if cfg.RebuildMode != "none" || cfg.Tool != "claude" {
				t.Errorf("Parse did not return its configuration: %+v", cfg)
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
		{
			"positional argument",
			[]string{"project"},
			"error: positional arguments are no longer accepted: project\n" +
				"       use -C/--dir for the project directory:  claude-contained -C project\n" +
				"       use -m/--mount for extra directories:    claude-contained -m project\n" +
				"       (bare 'claude-contained' uses the current directory)\n",
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
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown first", []string{"--wat", "project"},
			"error: unknown flag: --wat\n       run 'claude-contained --help' for the supported flags\n"},
		{"positional first", []string{"project", "--wat"},
			"error: positional arguments are no longer accepted: project\n" +
				"       use -C/--dir for the project directory:  claude-contained -C project\n" +
				"       use -m/--mount for extra directories:    claude-contained -m project\n" +
				"       (bare 'claude-contained' uses the current directory)\n"},
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

func TestParseForwardsToolArgumentsAfterTheFirstRealBoundary(t *testing.T) {
	cfg := Parse([]string{"-s", "--", "--", "--container-runtime=docker", "arg"}, testProgName, false)
	want := []string{"--", "--container-runtime=docker", "arg"}
	if strings.Join(cfg.ToolArgs, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("ToolArgs = %q, want %q", cfg.ToolArgs, want)
	}
	if cfg.ContainerRuntime != "" {
		t.Errorf("ContainerRuntime = %q, want empty", cfg.ContainerRuntime)
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
