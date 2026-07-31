package cli

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestScanRuntime(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"absent", []string{"-s", "-N"}, ""},
		{"separate value", []string{"--container-runtime", "docker"}, "docker"},
		{"inline value", []string{"--container-runtime=docker"}, "docker"},
		{"repeated: last wins", []string{"--container-runtime=apple", "--container-runtime=docker"}, "docker"},
		{"repeated across forms", []string{"--container-runtime", "docker", "--container-runtime=apple"}, "apple"},
		{"final argument with no value", []string{"-s", "--container-runtime"}, ""},
		{"dash-leading value", []string{"--container-runtime", "--shell"}, ""},
		{"after --", []string{"--", "--container-runtime=docker"}, ""},
		{"after -- among tool args", []string{"-s", "--", "-p", "--container-runtime=docker"}, ""},

		// The flag name appearing *inside* another flag's value is not a
		// selection. Parse rejects the dash-leading value, and if the pre-scan
		// disagreed, that rejection would name the wrong program.
		{"as an -e value", []string{"-e", "--container-runtime=docker"}, ""},
		{"as a --dns value", []string{"--dns", "--container-runtime=docker"}, ""},
		{"inside an inline --dns value", []string{"--dns=--container-runtime=docker"}, ""},
		{"as a -C value", []string{"-C", "--container-runtime=docker"}, ""},

		// A value-taking flag consuming a *legitimate* value must not hide a later
		// selection.
		{"after a consumed value", []string{"-C", "/tmp", "--container-runtime=docker"}, "docker"},
		{"between other flags", []string{"-s", "--container-runtime", "apple", "-N"}, "apple"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScanRuntime(tc.args); got != tc.want {
				t.Errorf("ScanRuntime(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// The pre-scan exists only because selection has to happen before parsing. For
// every command line Parse *accepts*, the two must agree -- otherwise the runtime
// driving the run is not the one the flag named.
//
// Restricting the property to accepted inputs is deliberate: when Parse rejects,
// no runtime command ever runs, so a disagreement is unobservable.
func TestScanRuntimeAgreesWithParse(t *testing.T) {
	argvs := [][]string{
		{},
		{"-s"},
		{"--container-runtime", "docker"},
		{"--container-runtime=apple"},
		{"--container-runtime=apple", "--container-runtime", "docker"},
		{"-C", "/tmp", "--container-runtime=docker", "-N"},
		{"-e", "K=V", "--container-runtime=docker"},
		{"--dns", "1.1.1.1", "--container-runtime=apple"},
		{"-t", "codex", "--container-runtime=docker"},
		{"-s", "--", "--container-runtime=apple"},
		{"--container-runtime=docker", "--", "-p", "80:80"},
		{"-p", "80:80", "--container-runtime=docker"},
		{"-H", "3845", "--container-runtime=docker"},
		{"-m", "/tmp", "--container-runtime=docker"},
		{"--share-skills", "/tmp", "--container-runtime=docker"},
		{"--allow-host", "example.com", "--container-runtime=docker"},
		{"-R", "--container-runtime=docker"},
		{"-a", "--container-runtime=docker"},
	}

	for _, argv := range argvs {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			cfg, err := Parse(argv, "claude-contained", false, io.Discard)
			if err != nil {
				t.Skipf("Parse rejects %q; the property covers accepted input only", argv)
			}
			if got, want := ScanRuntime(argv), cfg.ContainerRuntime; got != want {
				t.Errorf("ScanRuntime(%q) = %q but Parse recorded %q", argv, got, want)
			}
		})
	}
}

func TestContainerRuntimeRequiresValue(t *testing.T) {
	for _, argv := range [][]string{
		{"--container-runtime"},
		{"--container-runtime="},
		{"--container-runtime", "--shell"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			var stderr bytes.Buffer
			_, err := Parse(argv, "claude-contained", false, &stderr)

			var exit *ExitError
			if !asExitError(err, &exit) || exit.Code != ExitUsage {
				t.Fatalf("Parse(%q) error = %v, want exit %d", argv, err, ExitUsage)
			}
			if want := "error: --container-runtime requires apple or docker\n"; stderr.String() != want {
				t.Errorf("stderr = %q, want %q", stderr.String(), want)
			}
		})
	}
}

// Parse records the value without judging it. The accepted names live in
// internal/runtime, which reports a bad one only after --help has had its chance
// -- so nobody should "helpfully" add a second check here.
func TestContainerRuntimeValueIsNotValidatedByParse(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := Parse([]string{"--container-runtime=bogus"}, "claude-contained", false, &stderr)
	if err != nil {
		t.Fatalf("Parse should accept an unknown value: %v", err)
	}
	if cfg.ContainerRuntime != "bogus" {
		t.Errorf("ContainerRuntime = %q, want %q", cfg.ContainerRuntime, "bogus")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// -h wins wherever it appears, exactly as in bash, so an invalid runtime value
// must not pre-empt it. The ordering is enforced in cmd/claude-contained; this pins the
// parser half.
func TestHelpIsRecognizedAlongsideARuntimeValue(t *testing.T) {
	cfg, err := Parse([]string{"--container-runtime=bogus", "--help"}, "claude-contained", false, io.Discard)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !cfg.HelpRequested {
		t.Error("HelpRequested = false, want true")
	}
}

// The flag's error message lists the accepted values, which are defined in
// internal/runtime. A test rather than an import, so no production coupling is
// created just to keep one string honest.
func TestFlagErrorNamesTheAcceptedRuntimes(t *testing.T) {
	var stderr bytes.Buffer
	_, _ = Parse([]string{"--container-runtime"}, "claude-contained", false, &stderr)

	// Kept in sync with internal/runtime.NameApple / NameDocker.
	for _, name := range []string{"apple", "docker"} {
		if !strings.Contains(stderr.String(), name) {
			t.Errorf("the --container-runtime error should name %q: %q", name, stderr.String())
		}
	}
}

func asExitError(err error, target **ExitError) bool {
	e, ok := err.(*ExitError)
	if ok {
		*target = e
	}
	return ok
}

// Guards the table above against a silent rename of the flag.
func TestRuntimeFlagName(t *testing.T) {
	if RuntimeFlag != "--container-runtime" {
		t.Errorf("RuntimeFlag = %q; the shell suites and USAGE.md name --container-runtime", RuntimeFlag)
	}
	if !reflect.DeepEqual(valueTakingFlags[RuntimeFlag], true) {
		t.Error("RuntimeFlag must be listed in valueTakingFlags, or ScanRuntime will misread its value")
	}
}

// --build-context, both forms, and last occurrence wins -- the same shape
// --container-runtime already has.
func TestBuildContextFlagForms(t *testing.T) {
	cfg, err := Parse([]string{"--build-context", "/a"}, "claude-contained", false, io.Discard)
	if err != nil {
		t.Fatalf("Parse (space form): %v", err)
	}
	if cfg.BuildContext != "/a" {
		t.Errorf("BuildContext = %q, want %q", cfg.BuildContext, "/a")
	}

	cfg, err = Parse([]string{"--build-context=/b"}, "claude-contained", false, io.Discard)
	if err != nil {
		t.Fatalf("Parse (= form): %v", err)
	}
	if cfg.BuildContext != "/b" {
		t.Errorf("BuildContext = %q, want %q", cfg.BuildContext, "/b")
	}

	cfg, err = Parse([]string{"--build-context", "/a", "--build-context=/b"}, "claude-contained", false, io.Discard)
	if err != nil {
		t.Fatalf("Parse (repeated): %v", err)
	}
	if cfg.BuildContext != "/b" {
		t.Errorf("BuildContext = %q, want the last occurrence %q", cfg.BuildContext, "/b")
	}
}

func TestBuildContextRequiresValue(t *testing.T) {
	for _, argv := range [][]string{
		{"--build-context"},
		{"--build-context="},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			var stderr bytes.Buffer
			_, err := Parse(argv, "claude-contained", false, &stderr)

			var exit *ExitError
			if !asExitError(err, &exit) || exit.Code != ExitUsage {
				t.Fatalf("Parse(%q) error = %v, want exit %d", argv, err, ExitUsage)
			}
			if !strings.Contains(stderr.String(), "requires") {
				t.Errorf("stderr = %q, want it to mention \"requires\"", stderr.String())
			}
		})
	}
}

// Validation belongs to the rebuild step (internal/host.FindBuildContext), not
// to Parse: a nonexistent directory must parse fine.
func TestBuildContextValueIsNotValidatedByParse(t *testing.T) {
	cfg, err := Parse([]string{"--build-context", "/definitely/does/not/exist"}, "claude-contained", false, io.Discard)
	if err != nil {
		t.Fatalf("Parse should accept an unvalidated directory: %v", err)
	}
	if cfg.BuildContext != "/definitely/does/not/exist" {
		t.Errorf("BuildContext = %q, want the raw value", cfg.BuildContext)
	}
}

// The pre-scan and Parse must agree on where --build-context's value ends, or
// a later flag inside it could be read as a runtime selection by one and not
// the other.
func TestScanRuntimeSkipsBuildContextValue(t *testing.T) {
	args := []string{"--build-context", "--container-runtime=docker"}
	if got := ScanRuntime(args); got != "" {
		t.Errorf("ScanRuntime(%q) = %q, want \"\": the token is --build-context's value", args, got)
	}

	var stderr bytes.Buffer
	_, err := Parse(args, "claude-contained", false, &stderr)
	var exit *ExitError
	if !asExitError(err, &exit) || exit.Code != ExitUsage {
		t.Fatalf("Parse(%q) error = %v, want exit %d (a dash-leading --build-context value)", args, err, ExitUsage)
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
			cfg, err := Parse(tc.argv, "claude-contained", false, io.Discard)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.argv, err)
			}
			if cfg.RebuildMode != tc.want {
				t.Errorf("RebuildMode = %q, want %q", cfg.RebuildMode, tc.want)
			}
		})
	}
}
