package host

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- Step 1: the mutex --------------------------------------------------

func TestMutexAcquireReleaseRoundTrip(t *testing.T) {
	repo := realTempDir(t)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	lockDir := filepath.Join(repo, ".git", "claude-contained-worktree-locks.lock")

	var stderr bytes.Buffer
	m, ok := acquireMutex(repo, &stderr)
	if !ok {
		t.Fatalf("acquireMutex failed: %s", stderr.String())
	}
	if info, err := os.Stat(lockDir); err != nil || !info.IsDir() {
		t.Fatalf("acquire did not create the lock dir: %v", err)
	}
	ownerFile := filepath.Join(lockDir, "owner")
	if _, err := os.Stat(ownerFile); err != nil {
		t.Fatalf("acquire did not write an owner file: %v", err)
	}
	m.release()
	if _, err := os.Stat(lockDir); !os.IsNotExist(err) {
		t.Fatalf("release did not remove the lock dir")
	}
}

// The harness normalizer keys on "^[0-9]+ [0-9]+$" (see
// tests/differential/lib/normalize.sh), so the owner file must be written in
// exactly that shape.
func TestMutexOwnerFileByteFormat(t *testing.T) {
	repo := realTempDir(t)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	m, ok := acquireMutex(repo, &stderr)
	if !ok {
		t.Fatalf("acquireMutex failed: %s", stderr.String())
	}
	defer m.release()

	data, err := os.ReadFile(filepath.Join(m.dir, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	content := strings.TrimRight(string(data), "\n")
	if !regexp.MustCompile(`^[0-9]+ [0-9]+$`).MatchString(content) {
		t.Fatalf("owner file content %q does not match ^[0-9]+ [0-9]+$", content)
	}
	fields := strings.Fields(content)
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid != os.Getpid() {
		t.Errorf("owner pid = %q, want %d", fields[0], os.Getpid())
	}
}

func withStaleFixture(t *testing.T) (lockDir string) {
	t.Helper()
	repo := realTempDir(t)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	lockDir = filepath.Join(repo, ".git", "claude-contained-worktree-locks.lock")
	if err := os.Mkdir(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return lockDir
}

func writeOwner(t *testing.T, lockDir string, pid, ts int64) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(lockDir, "owner"),
		[]byte(strconv.FormatInt(pid, 10)+" "+strconv.FormatInt(ts, 10)+"\n"), 0o666); err != nil {
		t.Fatal(err)
	}
}

// deadPID spawns and reaps a process, returning a PID reliably not alive.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running `true`: %v", err)
	}
	return cmd.Process.Pid
}

func TestMutexHolderIsStaleDeadPID(t *testing.T) {
	lockDir := withStaleFixture(t)
	writeOwner(t, lockDir, int64(deadPID(t)), time.Now().Unix())
	if !mutexHolderIsStale(lockDir) {
		t.Error("dead-PID holder should be stale")
	}
}

func TestMutexHolderIsStaleLiveFreshNotStale(t *testing.T) {
	lockDir := withStaleFixture(t)
	writeOwner(t, lockDir, int64(os.Getpid()), time.Now().Unix())
	if mutexHolderIsStale(lockDir) {
		t.Error("live+fresh holder should NOT be stale")
	}
}

func TestMutexHolderIsStaleAged(t *testing.T) {
	lockDir := withStaleFixture(t)
	// Live PID, but a timestamp far older than mutexStaleAfter -- the age
	// fallback must win even though the PID resolves (guards PID reuse).
	writeOwner(t, lockDir, int64(os.Getpid()), time.Now().Add(-time.Hour).Unix())
	if !mutexHolderIsStale(lockDir) {
		t.Error("aged holder should be stale")
	}
}

func TestMutexHolderIsStaleNoOwnerFile(t *testing.T) {
	lockDir := withStaleFixture(t)
	if !mutexHolderIsStale(lockDir) {
		t.Error("a missing owner file should be stale (crash between mkdir and write)")
	}
}

// The regression under test: a mutex left by a dead launcher must be
// reclaimed promptly, not time out and leave the run unprotected.
func TestMutexReclaimIsPrompt(t *testing.T) {
	restoreGrace, restoreMax, restorePoll := mutexStaleGrace, mutexMaxWaits, mutexPollInterval
	mutexStaleGrace, mutexMaxWaits, mutexPollInterval = 1, 50, time.Millisecond
	defer func() { mutexStaleGrace, mutexMaxWaits, mutexPollInterval = restoreGrace, restoreMax, restorePoll }()

	repo := realTempDir(t)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	lockDir := filepath.Join(repo, ".git", "claude-contained-worktree-locks.lock")
	if err := os.Mkdir(lockDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeOwner(t, lockDir, int64(deadPID(t)), time.Now().Unix())

	var stderr bytes.Buffer
	start := time.Now()
	m, ok := acquireMutex(repo, &stderr)
	elapsed := time.Since(start)
	if !ok {
		t.Fatalf("acquireMutex failed: %s", stderr.String())
	}
	defer m.release()
	if elapsed > time.Second {
		t.Errorf("reclaim took %s, expected a prompt reclaim well under the timeout path", elapsed)
	}
	if !strings.Contains(stderr.String(), "reclaiming stale worktree auto-lock mutex") {
		t.Errorf("stderr = %q, want the reclaim note", stderr.String())
	}

	data, _ := os.ReadFile(filepath.Join(lockDir, "owner"))
	fields := strings.Fields(string(data))
	if len(fields) == 0 || fields[0] != strconv.Itoa(os.Getpid()) {
		t.Errorf("reclaimed mutex owner = %q, want this process's PID", string(data))
	}
}

func TestMutexTimesOutOnLiveHolder(t *testing.T) {
	restoreGrace, restoreMax, restorePoll := mutexStaleGrace, mutexMaxWaits, mutexPollInterval
	mutexStaleGrace, mutexMaxWaits, mutexPollInterval = 2, 4, time.Millisecond
	defer func() { mutexStaleGrace, mutexMaxWaits, mutexPollInterval = restoreGrace, restoreMax, restorePoll }()

	lockDir := withStaleFixture(t)
	writeOwner(t, lockDir, int64(os.Getpid()), time.Now().Unix())

	repo := filepath.Dir(filepath.Dir(lockDir))
	var stderr bytes.Buffer
	_, ok := acquireMutex(repo, &stderr)
	if ok {
		t.Fatal("acquireMutex should have timed out against a live, fresh holder")
	}
	if !strings.Contains(stderr.String(), "timed out waiting for worktree auto-lock mutex") {
		t.Errorf("stderr = %q, want the timeout warning", stderr.String())
	}
}

// --- Step 2: lock-file mechanics, against a real git repo --------------

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// gitFixture builds a real main repo with one linked worktree, returning
// their resolved paths.
func gitFixture(t *testing.T) (main, wt string) {
	t.Helper()
	base := realTempDir(t)
	main = filepath.Join(base, "main")
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, main, "init", "-q")
	runGit(t, main, "-c", "user.email=t@example.com", "-c", "user.name=test", "commit", "-q", "--allow-empty", "-m", "init")

	wt = filepath.Join(base, "wt")
	runGit(t, main, "worktree", "add", "-q", "--detach", wt)
	return main, ResolvePath(wt)
}

func lockReasonFor(t *testing.T, main, wt string) (reason string, locked bool) {
	t.Helper()
	out := runGit(t, main, "worktree", "list", "--porcelain")
	var cur string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			cur = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "locked") && cur == wt:
			locked = true
			reason = strings.TrimPrefix(strings.TrimPrefix(line, "locked"), " ")
		}
	}
	return reason, locked
}

func TestAddAutoLockOwnerCreatesLockThenAppendsSecondOwner(t *testing.T) {
	main, wt := gitFixture(t)

	if err := addAutoLockOwner(main, wt, "aic-one"); err != nil {
		t.Fatalf("addAutoLockOwner: %v", err)
	}
	reason, locked := lockReasonFor(t, main, wt)
	if !locked {
		t.Fatal("worktree not locked after first owner")
	}
	if !strings.HasPrefix(reason, AutoLockPrefix) {
		t.Fatalf("reason = %q, want the auto-lock prefix", reason)
	}
	owners, _ := parseAutoLockOwners(reason)
	if !reflectContains(owners, "aic-one") {
		t.Fatalf("owners = %v, want aic-one", owners)
	}

	if err := addAutoLockOwner(main, wt, "aic-two"); err != nil {
		t.Fatalf("addAutoLockOwner (second owner): %v", err)
	}
	reason, locked = lockReasonFor(t, main, wt)
	if !locked {
		t.Fatal("worktree unexpectedly unlocked")
	}
	owners, _ = parseAutoLockOwners(reason)
	if !reflectContains(owners, "aic-one") || !reflectContains(owners, "aic-two") {
		t.Fatalf("owners = %v, want both aic-one and aic-two", owners)
	}
}

func TestRemoveAutoLockOwnerLeavesOtherOwner(t *testing.T) {
	main, wt := gitFixture(t)
	mustAddOwner(t, main, wt, "aic-one")
	mustAddOwner(t, main, wt, "aic-two")

	removeAutoLockOwner(main, wt, "aic-one")

	reason, locked := lockReasonFor(t, main, wt)
	if !locked {
		t.Fatal("worktree should still be locked: another owner remains")
	}
	owners, _ := parseAutoLockOwners(reason)
	if reflectContains(owners, "aic-one") {
		t.Fatalf("owners = %v, aic-one should have been removed", owners)
	}
	if !reflectContains(owners, "aic-two") {
		t.Fatalf("owners = %v, aic-two should survive", owners)
	}
}

func TestRemoveAutoLockOwnerUnlocksOnLastOwner(t *testing.T) {
	main, wt := gitFixture(t)
	mustAddOwner(t, main, wt, "aic-only")

	removeAutoLockOwner(main, wt, "aic-only")

	_, locked := lockReasonFor(t, main, wt)
	if locked {
		t.Fatal("worktree should be unlocked once the last owner is removed")
	}
}

func TestUserLockUntouchedByAddAndRemove(t *testing.T) {
	main, wt := gitFixture(t)
	runGit(t, main, "worktree", "lock", "--reason", "mine", wt)

	if err := addAutoLockOwner(main, wt, "aic-one"); err == nil {
		t.Error("addAutoLockOwner should refuse to touch a user's own lock")
	}
	reason, locked := lockReasonFor(t, main, wt)
	if !locked || reason != "mine" {
		t.Fatalf("user lock reason = %q, locked=%v, want unchanged \"mine\"", reason, locked)
	}

	removeAutoLockOwner(main, wt, "aic-one")
	reason, locked = lockReasonFor(t, main, wt)
	if !locked || reason != "mine" {
		t.Fatalf("user lock reason after remove = %q, locked=%v, want unchanged \"mine\"", reason, locked)
	}
}

func mustAddOwner(t *testing.T, main, wt, owner string) {
	t.Helper()
	if err := addAutoLockOwner(main, wt, owner); err != nil {
		t.Fatalf("addAutoLockOwner(%q): %v", owner, err)
	}
}

func reflectContains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// --- Step 3: LockWorktrees / ReleaseWorktreeLocks -----------------------

func TestLockWorktreesLocksAndReleaseUnlocks(t *testing.T) {
	main, wt := gitFixture(t)

	var stdout, stderr bytes.Buffer
	locked := LockWorktrees(main, []string{wt}, "aic-test-0000", &stdout, &stderr)
	if len(locked) != 1 || locked[0] != wt {
		t.Fatalf("LockWorktrees returned %v, want [%s]", locked, wt)
	}
	if _, isLocked := lockReasonFor(t, main, wt); !isLocked {
		t.Fatal("worktree should be locked")
	}
	if got := stdout.String(); !strings.Contains(got, "Auto-locked 1 worktree(s).") {
		t.Errorf("stdout = %q, want the Auto-locked summary", got)
	}

	ReleaseWorktreeLocks(main, locked, "aic-test-0000", &stderr)
	if _, isLocked := lockReasonFor(t, main, wt); isLocked {
		t.Fatal("worktree should be unlocked after release (last owner)")
	}
}

func TestReleaseWorktreeLocksSurvivesOtherOwner(t *testing.T) {
	main, wt := gitFixture(t)
	mustAddOwner(t, main, wt, "aic-other-1111")

	var stdout, stderr bytes.Buffer
	locked := LockWorktrees(main, []string{wt}, "aic-mine-2222", &stdout, &stderr)

	ReleaseWorktreeLocks(main, locked, "aic-mine-2222", &stderr)

	reason, isLocked := lockReasonFor(t, main, wt)
	if !isLocked {
		t.Fatal("worktree should still be locked: aic-other-1111 remains an owner")
	}
	owners, _ := parseAutoLockOwners(reason)
	if !reflectContains(owners, "aic-other-1111") {
		t.Fatalf("owners = %v, want aic-other-1111 to survive", owners)
	}
	if reflectContains(owners, "aic-mine-2222") {
		t.Fatalf("owners = %v, aic-mine-2222 should have been removed", owners)
	}
}
