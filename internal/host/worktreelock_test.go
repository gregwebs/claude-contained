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

// mutexHolderIsStale parses the owner file with strings.Fields
// (worktreelock.go:84-92), so the file must be written in exactly the
// "<PID> <EPOCH>" shape this test pins. (Not a dependency on the golden
// normalizer's N7 substitution: the owner file's bytes are never inlined
// into a golden at all -- goldenfixture_test.go's manifest walk inlines
// content under .git only for "locked" and "*.mid-run-snapshot", and case
// 56, the one golden that exercises a stale-mutex reclaim, asserts the
// mutex directory is gone by the time the manifest is captured.)
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

// --- §3.2 U1-U8: the genuinely uncovered paths -------------------------
//
// These are characterization tests for code that already exists (plan §3.2):
// each of U1-U4 was watched to fail once, against a deliberately broken
// production line, before being left in its passing form. What was broken
// and observed to fail is recorded on each test below rather than left in
// the code, per the plan's instruction not to leave debug scaffolding
// behind -- a characterization test nobody watched fail is a comment, and
// this one records that it *was* watched.

// mutexBlockingFixture pre-occupies the mutex directory with a live, fresh
// (not reclaimable) holder, so acquireMutex inside LockWorktrees/
// ReleaseWorktreeLocks reliably times out. Shrinks the wait tunables first,
// the same way TestMutexTimesOutOnLiveHolder does, so the timeout itself
// stays fast.
func mutexBlockingFixture(t *testing.T, main string) {
	t.Helper()
	restoreGrace, restoreMax, restorePoll := mutexStaleGrace, mutexMaxWaits, mutexPollInterval
	mutexStaleGrace, mutexMaxWaits, mutexPollInterval = 2, 4, time.Millisecond
	t.Cleanup(func() { mutexStaleGrace, mutexMaxWaits, mutexPollInterval = restoreGrace, restoreMax, restorePoll })

	mutexDir := filepath.Join(main, ".git", "claude-contained-worktree-locks.lock")
	if err := os.Mkdir(mutexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeOwner(t, mutexDir, int64(os.Getpid()), time.Now().Unix())
}

// U1: LockWorktrees' fail-safe path -- a mutex it cannot acquire is a
// warning, never a reason to leave the worktrees unprotected.
//
// Watched to fail: with acquireMutex's `if err := os.Mkdir(lockDir, 0o777);
// err == nil { break }` changed to always `break` (i.e. acquireMutex always
// reports ok=true, as if `return &mutex{dir: lockDir}, true` were forced
// unconditionally at the top), this test's fail-safe-warning assertion
// failed as expected: no warning was printed because the (real) code path
// never took the "mutex unavailable" branch at all. Reverted.
func TestLockWorktreesFailSafeWithoutMutex(t *testing.T) {
	main, wt := gitFixture(t)
	mutexBlockingFixture(t, main)

	var stdout, stderr bytes.Buffer
	locked := LockWorktrees(main, []string{wt}, "aic-test-0000", &stdout, &stderr)

	if !strings.Contains(stderr.String(), "Warning: proceeding to auto-lock without the serialization mutex;") {
		t.Errorf("stderr = %q, want the fail-safe warning's first line", stderr.String())
	}
	if !strings.Contains(stderr.String(), "a concurrent launcher on this repo could race on lock bookkeeping.") {
		t.Errorf("stderr = %q, want the fail-safe warning's second line", stderr.String())
	}
	if len(locked) != 1 || locked[0] != wt {
		t.Fatalf("LockWorktrees returned %v, want [%s]: fail-safe means it locks anyway", locked, wt)
	}
	if _, isLocked := lockReasonFor(t, main, wt); !isLocked {
		t.Fatal("worktree should be locked despite the unavailable mutex (fail-safe)")
	}
}

// U2: ReleaseWorktreeLocks' fail-open path -- a mutex it cannot acquire
// during cleanup leaves the locks in place rather than risk dropping a
// still-live owner.
//
// Watched to fail alongside U1, with the same break described above
// (acquireMutex forced to always report ok=true): with the mutex never
// actually reporting itself unavailable, ReleaseWorktreeLocks' fail-open
// branch was never taken, and this test's "worktree remains locked"
// assertion failed -- the mutex-blocking fixture's own owner file was
// simply overwritten by the "successful" acquire, and the worktree was
// unlocked as a normal release. Reverted.
func TestReleaseWorktreeLocksFailOpenWithoutMutex(t *testing.T) {
	main, wt := gitFixture(t)
	mustAddOwner(t, main, wt, "aic-test-0000")
	mutexBlockingFixture(t, main)

	var stderr bytes.Buffer
	ReleaseWorktreeLocks(main, []string{wt}, "aic-test-0000", &stderr)

	if !strings.Contains(stderr.String(), "Warning: could not acquire worktree auto-lock mutex during cleanup;") {
		t.Errorf("stderr = %q, want the fail-open warning", stderr.String())
	}
	if !strings.Contains(stderr.String(), "worktree unlock") {
		t.Errorf("stderr = %q, want it to name the manual git worktree unlock command", stderr.String())
	}
	if !strings.Contains(stderr.String(), main) {
		t.Errorf("stderr = %q, want it to name the repo path", stderr.String())
	}
	if _, isLocked := lockReasonFor(t, main, wt); !isLocked {
		t.Fatal("worktree should remain locked: fail-open means the lock is left in place")
	}
}

// U3: LockWorktrees' warning path -- addAutoLockOwner failing (a user's own
// lock) is named on stderr, the worktree is absent from the returned list,
// and the Auto-locked count reflects the omission.
//
// Watched to fail: with the `if err := addAutoLockOwner(...); err != nil {
// ... continue }` guard changed to ignore the error (append to `locked`
// unconditionally instead of `continue`-ing past it), this test's "locked
// should be empty" assertion failed -- the user's own worktree was reported
// as auto-locked. Reverted.
func TestLockWorktreesWarnsAndOmitsOnUserLock(t *testing.T) {
	main, wt := gitFixture(t)
	runGit(t, main, "worktree", "lock", "--reason", "mine", wt)

	var stdout, stderr bytes.Buffer
	locked := LockWorktrees(main, []string{wt}, "aic-test-0000", &stdout, &stderr)

	if len(locked) != 0 {
		t.Fatalf("locked = %v, want empty: a user's own lock must be left unchanged", locked)
	}
	if !strings.Contains(stderr.String(), "Warning: could not auto-lock "+wt+"; leaving it unchanged") {
		t.Errorf("stderr = %q, want the per-worktree warning naming %s", stderr.String(), wt)
	}
	if !strings.Contains(stdout.String(), "Auto-locked 0 worktree(s).") {
		t.Errorf("stdout = %q, want the count to reflect the omission", stdout.String())
	}
	reason, isLocked := lockReasonFor(t, main, wt)
	if !isLocked || reason != "mine" {
		t.Fatalf("user lock = %q locked=%v, want unchanged \"mine\"", reason, isLocked)
	}
}

// U4: acquireMutex reclaims an aged live-PID holder end to end, not just at
// the mutexHolderIsStale predicate TestMutexHolderIsStaleAged already
// covers.
//
// Watched to fail: with mutexHolderIsStale's age-fallback block (the
// `if holderTS > 0 && now > holderTS { ... }` clause) deleted so only the
// PID-liveness check could report staleness, this test's "reclaim note"
// assertion failed -- acquireMutex timed out instead of reclaiming, because
// the holder PID (this test process's own, still alive) never resolves as
// dead. Reverted.
func TestAcquireMutexReclaimsAgedLivePIDHolder(t *testing.T) {
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
	// A live PID (this test process's own) but a timestamp far older than
	// mutexStaleAfter (30s default): the age fallback must drive the
	// reclaim, since the PID itself resolves as alive.
	writeOwner(t, lockDir, int64(os.Getpid()), time.Now().Add(-time.Hour).Unix())

	var stderr bytes.Buffer
	m, ok := acquireMutex(repo, &stderr)
	if !ok {
		t.Fatalf("acquireMutex failed: %s", stderr.String())
	}
	defer m.release()
	if !strings.Contains(stderr.String(), "reclaiming stale worktree auto-lock mutex") {
		t.Errorf("stderr = %q, want the reclaim note", stderr.String())
	}
	data, _ := os.ReadFile(filepath.Join(lockDir, "owner"))
	fields := strings.Fields(string(data))
	if len(fields) == 0 || fields[0] != strconv.Itoa(os.Getpid()) {
		t.Errorf("reclaimed mutex owner = %q, want this process's PID", string(data))
	}
}

// U5: removeAutoLockOwner's remaining early returns -- worktree directory
// gone, lock file absent, our owner not in the list -- each a silent no-op.
func TestRemoveAutoLockOwnerEarlyReturns(t *testing.T) {
	t.Run("worktree directory gone", func(t *testing.T) {
		main, wt := gitFixture(t)
		mustAddOwner(t, main, wt, "aic-one")
		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}
		// Must not panic: os.Stat(wtPath) fails and removeAutoLockOwner
		// returns immediately.
		removeAutoLockOwner(main, wt, "aic-one")
	})

	t.Run("lock file absent", func(t *testing.T) {
		main, wt := gitFixture(t)
		// Never locked at all: worktreeLockFile resolves, but the file
		// itself (<git-dir>/locked) does not exist.
		removeAutoLockOwner(main, wt, "aic-one")
		if _, locked := lockReasonFor(t, main, wt); locked {
			t.Fatal("a no-op remove must not itself create a lock")
		}
	})

	t.Run("owner not present", func(t *testing.T) {
		main, wt := gitFixture(t)
		mustAddOwner(t, main, wt, "aic-one")
		removeAutoLockOwner(main, wt, "aic-someone-else")
		reason, locked := lockReasonFor(t, main, wt)
		if !locked {
			t.Fatal("worktree should still be locked: the removed owner was never in the list")
		}
		owners, _ := parseAutoLockOwners(reason)
		if !reflectContains(owners, "aic-one") {
			t.Fatalf("owners = %v, want aic-one untouched", owners)
		}
	})
}

// U6: LockWorktrees deduplicates a repeated worktree in the slice it
// returns (the `seen` guard, worktreelock.go:274-277) -- distinct from U7's
// appendAutoLockOwner idempotence, which is what actually stops a duplicate
// owner *line* in the lock file.
func TestLockWorktreesDeduplicatesReturnedSlice(t *testing.T) {
	main, wt := gitFixture(t)

	var stdout, stderr bytes.Buffer
	locked := LockWorktrees(main, []string{wt, wt}, "aic-test-0000", &stdout, &stderr)

	if len(locked) != 1 || locked[0] != wt {
		t.Fatalf("locked = %v, want exactly one entry for the repeated worktree", locked)
	}
	if !strings.Contains(stdout.String(), "Auto-locked 1 worktree(s).") {
		t.Errorf("stdout = %q, want the count to reflect the dedup", stdout.String())
	}
}

// U7: appendAutoLockOwner is idempotent -- appending an owner already
// present returns without rewriting the lock file at all. This, not U6's
// `seen` guard on the returned slice, is what stops a duplicate owner line
// from ever reaching the lock file on disk.
func TestAppendAutoLockOwnerIsIdempotent(t *testing.T) {
	main, wt := gitFixture(t)
	mustAddOwner(t, main, wt, "aic-one")

	lockFile, err := worktreeLockFile(wt)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(lockFile)
	if err != nil {
		t.Fatal(err)
	}

	if err := appendAutoLockOwner(lockFile, "aic-one"); err != nil {
		t.Fatalf("appendAutoLockOwner (repeat): %v", err)
	}

	reason, locked := lockReasonFor(t, main, wt)
	if !locked {
		t.Fatal("worktree should still be locked")
	}
	owners, _ := parseAutoLockOwners(reason)
	count := 0
	for _, o := range owners {
		if o == "aic-one" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("owners = %v, want exactly one aic-one -- a duplicate append must not add a second", owners)
	}

	after, err := os.Stat(lockFile)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the lock file was rewritten for a no-op append")
	}
}

// U8: removeAutoLockOwner's last-owner path falls back to removing the lock
// file directly when `git worktree unlock` itself fails.
//
// Forcing that failure without touching filesystem permissions (which would
// break the os.Remove fallback identically, defeating the point): deleting
// the worktree admin directory's own `gitdir` back-pointer file makes `git
// worktree unlock <path>` fail with "fatal: '<path>' is not a working tree"
// (verified empirically against git 2.47), while `git -C <wt> rev-parse
// --git-dir` -- what worktreeLockFile itself calls -- is unaffected, so
// removeAutoLockOwner still reaches the unlock attempt and its fallback.
func TestRemoveAutoLockOwnerFallsBackWhenUnlockFails(t *testing.T) {
	main, wt := gitFixture(t)
	mustAddOwner(t, main, wt, "aic-only")

	lockFile, err := worktreeLockFile(wt)
	if err != nil {
		t.Fatal(err)
	}
	adminDir := filepath.Dir(lockFile)
	if err := os.Remove(filepath.Join(adminDir, "gitdir")); err != nil {
		t.Fatal(err)
	}

	removeAutoLockOwner(main, wt, "aic-only")

	if _, err := os.Stat(lockFile); !os.IsNotExist(err) {
		t.Fatalf("lock file still present after the unlock-failure fallback (err=%v)", err)
	}
}
