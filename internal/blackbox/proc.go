package blackbox

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// RunOpts configures a launcher invocation. The environment is built from
// scratch rather than inherited, so a launcher-read variable in the developer's
// shell can never leak into a test: only PATH (with the stub directory
// prepended), HOME, TZ, the stub spec, and ExtraEnv reach the child.
type RunOpts struct {
	Bin      string   // launcher path (Launcher.Primary or .Docked)
	Args     []string // argv after argv[0]
	Home     string   // HOME for the child
	Stubs    *Stubs   // prepends Dir to PATH and enables stub mode
	ExtraEnv []string // additional KEY=VALUE entries (e.g. CLAUDE_CONTAINED_RUNTIME=docker)
	Stdin    string
	OwnPgid  bool // give the child its own process group (for group-vs-solo signaling)
}

// Proc is a running launcher and the means to observe and signal it.
type Proc struct {
	cmd            *exec.Cmd
	Stdout, Stderr *bytes.Buffer

	done    chan struct{}
	exited  atomic.Bool
	waitErr error
	once    sync.Once
}

// Start launches the process and returns immediately.
func Start(t testing.TB, opts RunOpts) *Proc {
	t.Helper()
	cmd := exec.Command(opts.Bin, opts.Args...)
	cmd.Env = buildEnv(t, opts)
	cmd.Stdin = strings.NewReader(opts.Stdin)
	p := &Proc{
		cmd:    cmd,
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		done:   make(chan struct{}),
	}
	cmd.Stdout = p.Stdout
	cmd.Stderr = p.Stderr
	if opts.OwnPgid {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("blackbox: starting launcher: %v", err)
	}
	go func() {
		err := cmd.Wait()
		p.waitErr = err
		p.exited.Store(true)
		close(p.done)
	}()
	return p
}

func buildEnv(t testing.TB, opts RunOpts) []string {
	t.Helper()
	path := os.Getenv("PATH")
	if opts.Stubs != nil {
		path = opts.Stubs.Dir + string(os.PathListSeparator) + path
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + opts.Home,
		"TZ=UTC",
	}
	if opts.Stubs != nil {
		env = append(env, opts.Stubs.LauncherEnv(t))
	}
	env = append(env, opts.ExtraEnv...)
	return env
}

// Run starts the launcher and waits for it to exit under a generous hang guard,
// returning its output and exit code. For tests that never signal the process.
func Run(t testing.TB, opts RunOpts) (stdout, stderr string, code int) {
	t.Helper()
	p := Start(t, opts)
	if !p.WaitFor(30 * time.Second) {
		p.Kill()
		t.Fatalf("blackbox: launcher did not exit within the hang guard\nstdout:\n%s\nstderr:\n%s",
			p.Stdout.String(), p.Stderr.String())
	}
	return p.Stdout.String(), p.Stderr.String(), p.Code()
}

// WaitFor blocks until the process exits or the deadline elapses. It returns
// whether the process exited. The deadline is a hang guard, not sequencing.
func (p *Proc) WaitFor(within time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(within):
		return false
	}
}

// Running reports whether the process has not yet exited.
func (p *Proc) Running() bool { return !p.exited.Load() }

// Code returns the process exit code. Valid only after the process has exited.
func (p *Proc) Code() int {
	if p.cmd.ProcessState != nil {
		return p.cmd.ProcessState.ExitCode()
	}
	return -1
}

// PID is the process (and, when OwnPgid was set, process-group) id.
func (p *Proc) PID() int { return p.cmd.Process.Pid }

// Signal delivers a signal to the launcher process only.
func (p *Proc) Signal(t testing.TB, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(p.PID(), sig); err != nil {
		t.Fatalf("blackbox: signaling launcher: %v", err)
	}
}

// SignalGroup delivers a signal to the whole process group, reaching both the
// launcher and the runtime child it has in the foreground. Requires OwnPgid.
func (p *Proc) SignalGroup(t testing.TB, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(-p.PID(), sig); err != nil {
		t.Fatalf("blackbox: signaling launcher group: %v", err)
	}
}

// Kill is a last-resort teardown for a hung process and its group.
func (p *Proc) Kill() {
	p.once.Do(func() {
		if p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			_ = p.cmd.Process.Kill()
		}
	})
}

// --- observable-readiness primitives ---------------------------------------

// MakeFIFO creates a named pipe and returns its path. A stub arm that blocks on
// it opens it read-only; ReleaseFIFO opens it write-only, and the two rendezvous
// -- an observable release with no polling and no sleep.
func MakeFIFO(t testing.TB, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("blackbox: mkfifo %s: %v", path, err)
	}
	return path
}

// ReleaseFIFO unblocks a stub waiting on the FIFO by opening it for writing.
func ReleaseFIFO(t testing.TB, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("blackbox: releasing FIFO %s: %v", path, err)
	}
	_ = f.Close()
}

// WaitForFile blocks until a readiness marker exists or the deadline elapses,
// returning whether it appeared. The short poll interval is a hang-guarded wait
// on an observable signal, not a production-length sleep used for sequencing.
func WaitForFile(path string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	_, err := os.Stat(path)
	return err == nil
}

// WaitForEvents blocks until the stub log holds at least n invocations or the
// deadline elapses, returning whether the count was reached. It is the
// observable-readiness stand-in for commands the script under test backgrounds
// (e.g. `socat &`) and then exits before they have recorded -- the analogue of
// polling a log for a line count, with the deadline as a hang guard only.
func WaitForEvents(t testing.TB, s *Stubs, n int, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if len(s.Events(t)) >= n {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return len(s.Events(t)) >= n
}

// StaysRunning reports whether the process is still running throughout a short
// bounded window -- the observable proof that the launcher deferred a
// launcher-only signal rather than acting on it immediately. The window is a
// hang guard on a negative condition (there is no event to wait on), not
// sequencing.
func (p *Proc) StaysRunning(within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !p.Running() {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
	return p.Running()
}

// --- filesystem-silence assertion ------------------------------------------

// Manifest is a sorted, path+type+mode listing of a directory tree, for
// asserting that an early-exit run leaves the host untouched.
func Manifest(root string) []string {
	var lines []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		kind := "file"
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			kind = "symlink"
		case info.IsDir():
			kind = "dir"
		}
		lines = append(lines, fmt.Sprintf("%s %s %03o", rel, kind, info.Mode().Perm()))
		return nil
	})
	sort.Strings(lines)
	return lines
}

// AssertUnchanged fails if the tree under root differs from before.
func AssertUnchanged(t testing.TB, root string, before []string) {
	t.Helper()
	after := Manifest(root)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("filesystem under %s changed:\nbefore:\n%s\nafter:\n%s",
			root, strings.Join(before, "\n"), strings.Join(after, "\n"))
	}
}
