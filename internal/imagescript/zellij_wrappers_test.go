package imagescript

// Tests for image/zellij-run.sh and image/zellij-attach.sh: session liveness
// discovery, stale saved-state cleanup, the named-layout startup argv, the
// initial pane command written into the layout, the runtime directories the
// scripts pre-create, and the refusals (a live session is not restarted and its
// saved state is kept; an exited session is not attached to).
//
// The zellij client is modeled by the re-exec stub, armed to answer
// list-sessions with a chosen liveness and to record the attach argv.
// image/zellij-run.sh hardcodes /tmp/claude-contained-zellij-runtime with no env
// override, so full isolation is impossible without changing production. Instead
// every run gets a unique session name (so its session-specific paths under the
// shared runtime dir never collide) and a stubbed `id -u` (so its /tmp/zellij-*
// tree is per-test and removable); t.Cleanup removes both. These tests do not
// run in parallel: they share the runtime-dir root that each run mkdir/chmods.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"claude-contained/internal/blackbox"
)

const zellijRuntimeDir = "/tmp/claude-contained-zellij-runtime"

var zellijSeq atomic.Int64

func sanitizeSession(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			return r
		default:
			return '_'
		}
	}, name)
}

// zellijFixture is one armed run: a unique session, a fake uid (so /tmp/zellij-*
// is per-test), HOME isolation, and cleanup of everything the run writes outside
// HOME.
type zellijFixture struct {
	stubs      *blackbox.Stubs
	home       string
	session    string
	launchPath string
	cacheDir   string
	dataDir    string
	layoutFile string
	logDir     string // /tmp/zellij-<uid>/zellij-log, the temp log dir the run pre-creates
}

// newZellijFixture arms the zellij stub to report the given list-sessions
// liveness ("empty", "exited", or "live") and to record attach, plus a fake
// `id -u`, and registers cleanup of the per-test /tmp trees.
func newZellijFixture(t *testing.T, liveness string) *zellijFixture {
	t.Helper()
	home := t.TempDir()
	n := zellijSeq.Add(1)
	session := "ccimg_" + sanitizeSession(t.Name()) + "_" + strconv.FormatInt(n, 10)
	fakeUID := strconv.FormatInt(910000000+int64(os.Getpid()%100000)*1000+n, 10)

	var listOut string
	switch liveness {
	case "empty":
		listOut = ""
	case "exited":
		listOut = session + " (EXITED)\n"
	case "live":
		listOut = session + "\n"
	default:
		t.Fatalf("unknown liveness %q", liveness)
	}

	stubs := blackbox.NewStubs(t, "zellij", "id")
	stubs.Arm(t, "zellij", blackbox.ArmConfig{MatchContains: "list-sessions", Stdout: listOut})
	stubs.Arm(t, "zellij", blackbox.ArmConfig{MatchContains: "attach", Exit: 0})
	stubs.Arm(t, "id", blackbox.ArmConfig{Match: "*", Stdout: fakeUID + "\n"})
	stubs.CaptureEnv(t,
		"CLAUDE_CONTAINED_PRE_ZELLIJ_PATH",
		"CLAUDE_CONTAINED_PRE_ZELLIJ_PATH_SET",
		"CLAUDE_CONTAINED_PRE_ZELLIJ_SHELL",
		"CLAUDE_CONTAINED_PRE_ZELLIJ_SHELL_SET",
	)

	f := &zellijFixture{
		stubs:      stubs,
		home:       home,
		session:    session,
		launchPath: stubs.Dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		cacheDir:   filepath.Join(home, ".claude-contained", "zellij", "cache"),
		dataDir:    filepath.Join(home, ".claude-contained", "zellij", "data"),
		layoutFile: filepath.Join(zellijRuntimeDir, "layouts", session+".kdl"),
		logDir:     filepath.Join("/tmp", "zellij-"+fakeUID, "zellij-log"),
	}
	t.Cleanup(func() {
		_ = os.RemoveAll("/tmp/zellij-" + fakeUID)
		_ = os.Remove(f.layoutFile)
		_ = os.RemoveAll(filepath.Join(zellijRuntimeDir, "zellij", "contract_version_1", session))
	})
	return f
}

// savedStatePath is where zellij-run looks for (and forgets) a session's saved
// cache metadata.
func (f *zellijFixture) savedStatePath() string {
	return filepath.Join(f.cacheDir, "zellij", "contract_version_1", "session_info", f.session)
}

func (f *zellijFixture) seedSavedState(t *testing.T) {
	t.Helper()
	dir := f.savedStatePath()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session-metadata.kdl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *zellijFixture) runRun(t *testing.T) scriptResult {
	t.Helper()
	return runScript(t, scriptOpts{
		Script: scriptPath(t, "zellij-run.sh"),
		Args:   []string{f.session, "--", "echo", "hello world"},
		Env:    []string{"SHELL=/bin/bash", "CLAUDE_CONTAINED_ZELLIJ_WAIT_SECONDS=0"},
		Home:   f.home,
		Stubs:  f.stubs,
	})
}

func hasArg(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func (f *zellijFixture) attachEvent(t *testing.T) blackbox.Invocation {
	t.Helper()
	for _, e := range f.stubs.Events(t) {
		if e.Bin == "zellij" && hasArg(e.Argv, "attach") {
			return e
		}
	}
	t.Fatalf("no zellij attach invocation was recorded; events: %v", f.stubs.Events(t))
	return blackbox.Invocation{}
}

func TestZellijRunStartsFreshSession(t *testing.T) {
	f := newZellijFixture(t, "empty")
	if res := f.runRun(t); res.Code != 0 {
		t.Fatalf("exit %d, want 0 for a fresh session\nstderr:\n%s", res.Code, res.Stderr)
	}
	f.attachEvent(t) // proves it proceeded to startup
}

func TestZellijRunRemovesSavedState(t *testing.T) {
	f := newZellijFixture(t, "empty")
	f.seedSavedState(t)
	f.runRun(t)
	if _, err := os.Stat(f.savedStatePath()); err == nil {
		t.Error("stale saved session state was not forgotten before fresh startup")
	}
}

func TestZellijRunNamedLayoutStartupArgv(t *testing.T) {
	f := newZellijFixture(t, "empty")
	f.runRun(t)
	want := []string{
		"--config", "/etc/claude-contained/zellij/config.kdl",
		"--data-dir", f.dataDir,
		"attach", "--forget", "--create", f.session,
		"options", "--default-layout", f.session,
	}
	if got := f.attachEvent(t).Argv; !equalStrings(got, want) {
		t.Errorf("zellij startup argv =\n  %v\nwant\n  %v", got, want)
	}
}

func TestZellijRunNoServerFlag(t *testing.T) {
	f := newZellijFixture(t, "empty")
	f.runRun(t)
	for _, e := range f.stubs.Events(t) {
		if hasArg(e.Argv, "--server") {
			t.Errorf("zellij was invoked with --server, overriding the XDG_RUNTIME_DIR socket: %v", e.Argv)
		}
	}
}

func TestZellijRunStashesPathAndShell(t *testing.T) {
	f := newZellijFixture(t, "empty")
	f.runRun(t)
	env := f.attachEvent(t).Env
	if env["CLAUDE_CONTAINED_PRE_ZELLIJ_PATH_SET"] != "1" {
		t.Errorf("PRE_ZELLIJ_PATH_SET = %q, want 1", env["CLAUDE_CONTAINED_PRE_ZELLIJ_PATH_SET"])
	}
	if env["CLAUDE_CONTAINED_PRE_ZELLIJ_PATH"] != f.launchPath {
		t.Errorf("PRE_ZELLIJ_PATH = %q, want the launch PATH %q", env["CLAUDE_CONTAINED_PRE_ZELLIJ_PATH"], f.launchPath)
	}
	if env["CLAUDE_CONTAINED_PRE_ZELLIJ_SHELL_SET"] != "1" {
		t.Errorf("PRE_ZELLIJ_SHELL_SET = %q, want 1", env["CLAUDE_CONTAINED_PRE_ZELLIJ_SHELL_SET"])
	}
	if env["CLAUDE_CONTAINED_PRE_ZELLIJ_SHELL"] != "/bin/bash" {
		t.Errorf("PRE_ZELLIJ_SHELL = %q, want /bin/bash", env["CLAUDE_CONTAINED_PRE_ZELLIJ_SHELL"])
	}
}

func TestZellijRunWritesPaneCommandLayout(t *testing.T) {
	f := newZellijFixture(t, "empty")
	f.runRun(t)
	data, err := os.ReadFile(f.layoutFile)
	if err != nil {
		t.Fatalf("reading layout file: %v", err)
	}
	if !strings.Contains(string(data), `args "echo" "hello world"`) {
		t.Errorf("layout does not carry the initial pane command:\n%s", data)
	}
}

func TestZellijRunPreCreatesRuntimeDirectories(t *testing.T) {
	f := newZellijFixture(t, "empty")
	f.runRun(t)
	for name, dir := range map[string]string{
		"temp log dir":       f.logDir,
		"runtime socket dir": filepath.Join(zellijRuntimeDir, "zellij", "contract_version_1"),
		"runtime layout dir": filepath.Join(zellijRuntimeDir, "layouts"),
		"project cache dir":  filepath.Join(f.cacheDir, "org", "Zellij-Contributors", "Zellij"),
	} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("%s was not pre-created: %s", name, dir)
		}
	}
}

func TestZellijRunExitedTreatedNotLiveAndReplaced(t *testing.T) {
	f := newZellijFixture(t, "exited")
	if res := f.runRun(t); res.Code != 0 {
		t.Fatalf("exit %d, want 0: an (EXITED) session must be treated as not live\nstderr:\n%s", res.Code, res.Stderr)
	}
	if !hasArg(f.attachEvent(t).Argv, "--default-layout") {
		t.Error("an exited saved session was not replaced with the requested layout startup")
	}
}

func TestZellijRunRefusesLiveSession(t *testing.T) {
	f := newZellijFixture(t, "live")
	f.seedSavedState(t)
	res := f.runRun(t)
	if res.Code != 1 {
		t.Errorf("exit %d, want 1 for an already-live session", res.Code)
	}
	if _, err := os.Stat(f.savedStatePath()); err != nil {
		t.Error("a live session's saved state was deleted; it must be kept")
	}
	for _, e := range f.stubs.Events(t) {
		if e.Bin == "zellij" && hasArg(e.Argv, "attach") {
			t.Error("a live session should not be restarted, but attach was invoked")
		}
	}
}

func TestZellijAttachRefusesExitedSession(t *testing.T) {
	f := newZellijFixture(t, "exited")
	res := runScript(t, scriptOpts{
		Script: scriptPath(t, "zellij-attach.sh"),
		Args:   []string{f.session},
		Home:   f.home,
		Stubs:  f.stubs,
	})
	if res.Code != 1 {
		t.Errorf("exit %d, want 1: zellij-attach must refuse an exited saved session", res.Code)
	}
	for _, e := range f.stubs.Events(t) {
		if e.Bin == "zellij" && hasArg(e.Argv, "attach") {
			t.Error("zellij-attach reached the attach exec for an exited session")
		}
	}
}
