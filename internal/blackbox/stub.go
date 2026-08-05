// Package blackbox is a shared test-support harness for compiled-binary
// black-box tests of the launcher. It builds the launcher once per test process
// into an isolated temporary directory, provides a reusable command stub that
// models external runtimes (docker, container) and records their invocations
// structurally, and offers process/FIFO/readiness helpers so signal and
// exit-propagation behavior can be observed without a real container runtime.
//
// The stub is this test binary re-executed under a runtime name. A test package
// calls RunStubIfInvoked from TestMain; NewStubs symlinks names such as docker
// and container to the test binary (os.Executable); and the launcher -- a
// separately built binary that does not import this package -- execs those
// symlinks off a PATH the harness controls. Stub mode is entered only when the
// BLACKBOX_STUB_SPEC environment variable is set, which happens exclusively in a
// launcher subprocess's inherited environment, never in the go-test parent, so
// an ordinary test run is unaffected.
//
// This is a normal (non-_test.go) package so several test packages can import
// it. It imports testing for its testing.TB helpers -- the testify pattern --
// and never links into the shipped launcher, which does not import it.
package blackbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// stubEnvVar names the spec file the re-executed stub reads. Presence of this
// variable is the sole trigger for stub mode.
const stubEnvVar = "BLACKBOX_STUB_SPEC"

// Invocation is one recorded stub call. Argv excludes argv[0]; Bin is its
// basename (the runtime name the launcher invoked, e.g. "docker").
type Invocation struct {
	Bin        string   `json:"bin"`
	Argv       []string `json:"argv"`
	PID        int      `json:"pid"`
	StartNanos int64    `json:"startNanos"`
}

// stubArm is one behavior rule for a stubbed runtime, matched against argv[1].
// The order of side effects models a foreground container run: signal readiness,
// block until released, record completion, then exit -- so a signal delivered
// while blocked is observable through which of ReadyFile/DoneFile exist and the
// launcher's own exit status.
type stubArm struct {
	// Match is the argv[1] subcommand this arm answers, or "*" for any.
	Match string `json:"match"`
	// ReadyFile, if set, is created (empty) before the arm blocks, signaling to
	// the harness that the child has started.
	ReadyFile string `json:"readyFile,omitempty"`
	// BlockOnFIFO, if set, is opened read-only -- which blocks until the harness
	// opens it for writing. This is the observable-readiness stand-in for a
	// foreground run's duration; no sleep is involved.
	BlockOnFIFO string `json:"blockOnFifo,omitempty"`
	// DoneFile, if set, is created after the block is released and before exit,
	// proving the run completed naturally rather than being killed mid-block.
	DoneFile string `json:"doneFile,omitempty"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Exit     int    `json:"exit,omitempty"`
}

// stubSpec is the whole stub configuration, written by NewStubs and read by the
// re-executed stub.
//
// Only arms explicitly configured are answered; every other subcommand exits 0
// with no output, which is enough for the launcher's liveness and discovery
// probes (docker info/ps, container system/list/inspect) in the cases these
// tests drive. A test that configures a tooling layer would additionally have to
// answer `image inspect` the way the golden stubs do: a bare exit 0 with no
// output is classified as a probe fault by internal/plan's probeImageID, so the
// permissive default is deliberately not extended to that subcommand here.
type stubSpec struct {
	LogPath string               `json:"logPath"`
	Bins    map[string][]stubArm `json:"bins"`
}

// RunStubIfInvoked runs the command stub and exits when this process was started
// as a stub (BLACKBOX_STUB_SPEC set), and otherwise returns false so TestMain
// can proceed with the ordinary test run.
func RunStubIfInvoked() bool {
	specPath := os.Getenv(stubEnvVar)
	if specPath == "" {
		return false
	}
	os.Exit(runStub(specPath))
	return true // unreachable; keeps the signature honest.
}

func runStub(specPath string) int {
	data, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blackbox stub: read spec: %v\n", err)
		return 127
	}
	var spec stubSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		fmt.Fprintf(os.Stderr, "blackbox stub: parse spec: %v\n", err)
		return 127
	}

	bin := filepath.Base(os.Args[0])
	recordInvocation(spec.LogPath, bin, os.Args[1:])

	arm, ok := matchArm(spec.Bins[bin], os.Args[1:])
	if !ok {
		return 0 // permissive default: probes succeed.
	}

	if arm.ReadyFile != "" {
		_ = os.WriteFile(arm.ReadyFile, nil, 0o600)
	}
	if arm.BlockOnFIFO != "" {
		// Opening for read blocks until the harness opens the FIFO for writing.
		// A signal delivered to this process while it blocks here ends it via
		// the signal's default disposition -- which is the whole point of the
		// group signal test.
		if f, err := os.OpenFile(arm.BlockOnFIFO, os.O_RDONLY, 0); err == nil {
			_ = f.Close()
		}
	}
	if arm.DoneFile != "" {
		_ = os.WriteFile(arm.DoneFile, nil, 0o600)
	}
	if arm.Stdout != "" {
		_, _ = fmt.Fprint(os.Stdout, arm.Stdout)
	}
	if arm.Stderr != "" {
		_, _ = fmt.Fprint(os.Stderr, arm.Stderr)
	}
	return arm.Exit
}

// matchArm returns the first arm whose Match equals argv[1] or is "*".
func matchArm(arms []stubArm, argv []string) (stubArm, bool) {
	sub := ""
	if len(argv) > 0 {
		sub = argv[0]
	}
	for _, a := range arms {
		if a.Match == "*" || a.Match == sub {
			return a, true
		}
	}
	return stubArm{}, false
}

// recordInvocation appends one JSON line describing this call. O_APPEND makes
// concurrent single-line writes from separate stub processes atomic.
func recordInvocation(logPath, bin string, argv []string) {
	if logPath == "" {
		return
	}
	line, err := json.Marshal(Invocation{
		Bin:        bin,
		Argv:       argv,
		PID:        os.Getpid(),
		StartNanos: time.Now().UnixNano(),
	})
	if err != nil {
		return
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}
