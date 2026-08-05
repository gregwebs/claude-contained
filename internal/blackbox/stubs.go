package blackbox

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Stubs is a directory of stub runtime executables (symlinks to this test
// binary) plus the spec that drives their behavior and the log they append to.
// Prepend Dir to a launcher's PATH and set the spec via LauncherEnv.
type Stubs struct {
	Dir      string
	specPath string
	logPath  string
	spec     stubSpec
}

// NewStubs creates a stub directory with an executable symlink for each named
// runtime (e.g. "docker", "container") pointing at this test binary. Configure
// behavior with Arm; unconfigured subcommands exit 0.
func NewStubs(t testing.TB, bins ...string) *Stubs {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("blackbox: locating test binary: %v", err)
	}
	dir := t.TempDir()
	for _, b := range bins {
		if err := os.Symlink(self, filepath.Join(dir, b)); err != nil {
			t.Fatalf("blackbox: linking stub %s: %v", b, err)
		}
	}
	s := &Stubs{
		Dir:      dir,
		specPath: filepath.Join(dir, "spec.json"),
		logPath:  filepath.Join(dir, "invocations.jsonl"),
	}
	s.spec = stubSpec{LogPath: s.logPath, Bins: map[string][]stubArm{}}
	return s
}

// ArmConfig is the side-effect-naming shape a test uses to describe one behavior
// rule, decoupling test call sites from the wire-format stubArm.
type ArmConfig struct {
	Match       string
	Exit        int
	Stdout      string
	Stderr      string
	ReadyFile   string
	BlockOnFIFO string
	DoneFile    string
}

// Arm appends a behavior rule for one runtime and rewrites the spec file.
func (s *Stubs) Arm(t testing.TB, bin string, arm ArmConfig) *Stubs {
	t.Helper()
	s.spec.Bins[bin] = append(s.spec.Bins[bin], stubArm{
		Match:       arm.Match,
		ReadyFile:   arm.ReadyFile,
		BlockOnFIFO: arm.BlockOnFIFO,
		DoneFile:    arm.DoneFile,
		Stdout:      arm.Stdout,
		Stderr:      arm.Stderr,
		Exit:        arm.Exit,
	})
	s.writeSpec(t)
	return s
}

func (s *Stubs) writeSpec(t testing.TB) {
	t.Helper()
	data, err := json.Marshal(s.spec)
	if err != nil {
		t.Fatalf("blackbox: marshaling stub spec: %v", err)
	}
	if err := os.WriteFile(s.specPath, data, 0o600); err != nil {
		t.Fatalf("blackbox: writing stub spec: %v", err)
	}
}

// LauncherEnv returns the environment entry that puts a launched process into
// stub mode. NewStubs always writes an initial (possibly empty) spec so this is
// valid even before Arm is called.
func (s *Stubs) LauncherEnv(t testing.TB) string {
	t.Helper()
	if _, err := os.Stat(s.specPath); err != nil {
		s.writeSpec(t)
	}
	return stubEnvVar + "=" + s.specPath
}

// Events returns every recorded stub invocation in call order.
func (s *Stubs) Events(t testing.TB) []Invocation {
	t.Helper()
	f, err := os.Open(s.logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("blackbox: reading stub log: %v", err)
	}
	defer func() { _ = f.Close() }()

	var events []Invocation
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var inv Invocation
		if err := json.Unmarshal([]byte(line), &inv); err != nil {
			t.Fatalf("blackbox: parsing stub log line %q: %v", line, err)
		}
		events = append(events, inv)
	}
	return events
}

// InvokedBins reports the set of runtime names that were invoked at least once.
func (s *Stubs) InvokedBins(t testing.TB) map[string]bool {
	t.Helper()
	seen := map[string]bool{}
	for _, e := range s.Events(t) {
		seen[e.Bin] = true
	}
	return seen
}
