package host

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"claude-contained/internal/diagnostic"
)

// Tunable only so tests need not sleep five seconds; the values mirror the
// bash literals (claude-contained:1099, :1149, :1156, :1160) exactly. Only
// tests may change them.
var (
	mutexStaleAfter   = 30 * time.Second // WORKTREE_LOCK_MUTEX_STALE_SECS
	mutexPollInterval = 100 * time.Millisecond
	mutexStaleGrace   = 5  // iterations before a reclaim is attempted
	mutexMaxWaits     = 50 // iterations before giving up
)

// mutex is the mkdir-based lock that serializes owner-list edits. macOS has
// no flock(1), which is why this is a directory and not a file lock.
type mutex struct{ dir string }

// acquireMutex mirrors with_worktree_lock_mutex (claude-contained:1142-1167).
// ok=false means the caller must decide for itself which way to fail: the
// launch path locks anyway, the cleanup path leaves the locks alone.
func acquireMutex(repoRoot string, stderr io.Writer) (m *mutex, ok bool) {
	return acquireMutexContext(context.Background(), repoRoot, stderr)
}

func acquireMutexContext(ctx context.Context, repoRoot string, stderr io.Writer) (m *mutex, ok bool) {
	lockDir := filepath.Join(repoRoot, ".git", "claude-contained-worktree-locks.lock")
	waited := 0
	logger := diagnostic.For(ctx, diagnostic.ComponentWorktree)
	logger.Debug("worktree auto-lock mutex acquisition started", diagnostic.String("lock_dir", lockDir))
	var lastErr error

	for {
		if err := os.Mkdir(lockDir, 0o777); err == nil {
			break
		} else {
			lastErr = err
		}
		if waited >= mutexStaleGrace {
			observation := observeMutexHolder(lockDir)
			if observation.stale {
				attrs := []diagnostic.Attr{
					diagnostic.String("lock_dir", lockDir),
					diagnostic.Int("wait_count", waited),
					diagnostic.String("stale_reason", observation.reason),
				}
				if observation.pid != 0 {
					attrs = append(attrs, diagnostic.Int("holder_pid", observation.pid))
				}
				if observation.age > 0 {
					attrs = append(attrs, diagnostic.Duration("holder_duration", observation.age))
				}
				if observation.err != nil {
					attrs = append(attrs, diagnostic.ErrorAttr(observation.err))
				}
				logger.Warn("stale worktree auto-lock mutex detected", attrs...)
				_, _ = fmt.Fprintln(stderr, "Note: reclaiming stale worktree auto-lock mutex from a defunct launcher")
				if err := os.Remove(filepath.Join(lockDir, "owner")); err != nil && !os.IsNotExist(err) {
					logger.Warn("stale worktree auto-lock mutex owner removal failed",
						diagnostic.String("owner_path", filepath.Join(lockDir, "owner")), diagnostic.ErrorAttr(err))
				}
				if err := os.Remove(lockDir); err != nil && !os.IsNotExist(err) {
					logger.Warn("stale worktree auto-lock mutex removal failed",
						diagnostic.String("lock_dir", lockDir), diagnostic.ErrorAttr(err))
				}
				waited = 0
				continue
			}
		}
		if waited >= mutexMaxWaits {
			logger.Warn("worktree auto-lock mutex acquisition timed out",
				diagnostic.String("lock_dir", lockDir),
				diagnostic.Int("wait_count", waited),
				diagnostic.ErrorAttr(lastErr),
			)
			_, _ = fmt.Fprintln(stderr, "Warning: timed out waiting for worktree auto-lock mutex")
			return nil, false
		}
		time.Sleep(mutexPollInterval)
		waited++
	}

	// Record holder identity + time so a later run can detect a stale hold.
	// Best-effort, matching bash's `|| true` on the write.
	if f, err := os.Create(filepath.Join(lockDir, "owner")); err == nil {
		_, _ = fmt.Fprintf(f, "%d %d\n", os.Getpid(), time.Now().Unix())
		_ = f.Close()
	} else {
		logger.Warn("worktree auto-lock mutex owner identity could not be recorded",
			diagnostic.String("owner_path", filepath.Join(lockDir, "owner")), diagnostic.ErrorAttr(err))
	}
	logger.Debug("worktree auto-lock mutex acquired",
		diagnostic.String("lock_dir", lockDir), diagnostic.Int("wait_count", waited),
		diagnostic.Int("holder_pid", os.Getpid()))

	return &mutex{dir: lockDir}, true
}

// release mirrors release_worktree_lock_mutex (claude-contained:1170-1176).
func (m *mutex) release() {
	if m == nil {
		return
	}
	_ = os.Remove(filepath.Join(m.dir, "owner"))
	_ = os.Remove(m.dir)
}

// mutexHolderIsStale mirrors mutex_holder_is_stale (claude-contained:1116-1140).
func mutexHolderIsStale(lockDir string) bool {
	return observeMutexHolder(lockDir).stale
}

type mutexHolderObservation struct {
	stale  bool
	reason string
	pid    int
	age    time.Duration
	err    error
}

func observeMutexHolder(lockDir string) mutexHolderObservation {
	data, err := os.ReadFile(filepath.Join(lockDir, "owner"))
	if err != nil {
		// A live holder writes its owner file within microseconds of creating
		// the directory; a persistently missing one means the holder died
		// mid-acquire.
		return mutexHolderObservation{stale: true, reason: "owner-unreadable", err: err}
	}

	fields := strings.Fields(string(data))
	var holderPID int
	var holderTS int64
	if len(fields) > 0 {
		holderPID, _ = strconv.Atoi(fields[0])
	}
	if len(fields) > 1 {
		holderTS, _ = strconv.ParseInt(fields[1], 10, 64)
	}

	// Holder process is gone -> stale. Any error from the liveness check --
	// not just "no such process" -- is treated as gone, matching kill -0's
	// non-zero exit under EPERM as well as ESRCH: a cross-user holder of a
	// mutex inside this user's own .git is pathological, and diverging here
	// would be a silent behavior change in the one place a mistake destroys
	// data.
	if holderPID != 0 {
		if err := syscall.Kill(holderPID, 0); err != nil {
			return mutexHolderObservation{stale: true, reason: "holder-unreachable", pid: holderPID, err: err}
		}
	}

	// Age fallback guards against PID reuse and platforms with unreliable
	// kill -0.
	now := time.Now().Unix()
	if holderTS > 0 && now > holderTS {
		age := now - holderTS
		if age >= int64(mutexStaleAfter/time.Second) {
			return mutexHolderObservation{
				stale: true, reason: "holder-expired", pid: holderPID,
				age: time.Duration(age) * time.Second,
			}
		}
		return mutexHolderObservation{reason: "holder-active", pid: holderPID, age: time.Duration(age) * time.Second}
	}

	return mutexHolderObservation{reason: "holder-active", pid: holderPID}
}

// worktreeLockFile mirrors get_worktree_lock_file (claude-contained:1104-1111).
func worktreeLockFile(wtPath string) (string, error) {
	out, err := exec.Command("git", "-C", wtPath, "rev-parse", "--git-dir").Output()
	if err != nil {
		return "", err
	}
	gitDir := ResolvePathIn(wtPath, strings.TrimSpace(string(out)))
	return filepath.Join(gitDir, "locked"), nil
}

// parseAutoLockOwners returns the owners recorded in a reason and whether the
// reason is one of ours at all. content is the raw lock-file content (as
// `cat` would read it); trailing newlines are stripped first, matching
// bash's `reason="$(cat "$lock_file")"`, which strips *all* trailing
// newlines via command substitution, not just one. Splitting uses
// strings.Fields on the result, matching bash word-splitting -- which
// collapses runs of whitespace and newlines -- rather than
// strings.Split(s, " "), which would produce empty owners from any run of
// spaces.
func parseAutoLockOwners(content string) (owners []string, ours bool) {
	reason := strings.TrimRight(content, "\n")
	if !strings.HasPrefix(reason, AutoLockPrefix) {
		return nil, false
	}
	return strings.Fields(strings.TrimPrefix(reason, AutoLockPrefix)), true
}

// autoLockReason mirrors write_auto_lock_reason's reason construction
// (claude-contained:1188-1197), without the trailing newline the write adds.
func autoLockReason(owners []string) string {
	reason := AutoLockPrefix
	for _, owner := range owners {
		reason += " " + owner
	}
	return reason
}

// writeAutoLockReason mirrors write_auto_lock_reason's file write: `printf
// '%s\n' "$reason" > "$lock_file"`.
func writeAutoLockReason(lockFile string, owners []string) error {
	return os.WriteFile(lockFile, []byte(autoLockReason(owners)+"\n"), 0o666)
}

// appendAutoLockOwner mirrors append_auto_lock_owner (claude-contained:1200-1218).
// It reads the lock file itself -- callers never pass content in -- so every
// write path re-checks the *current* content's prefix, not what was scanned
// earlier.
func appendAutoLockOwner(lockFile, owner string) error {
	content, _ := os.ReadFile(lockFile) // best-effort, matches `cat ... || true`
	owners, ours := parseAutoLockOwners(string(content))
	if !ours {
		return fmt.Errorf("worktree lock reason is not ours: %s", lockFile)
	}
	for _, existing := range owners {
		if existing == owner {
			return nil
		}
	}
	owners = append(owners, owner)
	return writeAutoLockReason(lockFile, owners)
}

// addAutoLockOwner mirrors add_auto_worktree_lock_owner (claude-contained:1230-1248).
//
// Three-way: existing lock file -> append; else `git worktree lock` -> on
// failure re-read the file and append only if it carries the prefix; else
// fail. The failure case is what produces "Warning: could not auto-lock ...;
// leaving it unchanged" and is the guard that never overwrites a user's lock.
func addAutoLockOwner(repoRoot, wtPath, owner string) error {
	lockFile, err := worktreeLockFile(wtPath)
	if err != nil {
		return err
	}

	if info, statErr := os.Stat(lockFile); statErr == nil && info.Mode().IsRegular() {
		return appendAutoLockOwner(lockFile, owner)
	}

	cmd := exec.Command("git", "-C", repoRoot, "worktree", "lock",
		"--reason", AutoLockPrefix+" "+owner, wtPath)
	if err := cmd.Run(); err != nil {
		return appendAutoLockOwner(lockFile, owner)
	}
	return nil
}

// removeAutoLockOwner mirrors remove_auto_worktree_lock_owner
// (claude-contained:1250-1280). Five early returns, each meaning "leave it
// alone": missing worktree dir, unresolvable git dir, missing lock file,
// non-prefixed reason, our owner not present. Only when the remaining owner
// list is empty does it unlock (falling back to removing the file).
func removeAutoLockOwner(repoRoot, wtPath, owner string) {
	info, err := os.Stat(wtPath)
	if err != nil || !info.IsDir() {
		return
	}
	lockFile, err := worktreeLockFile(wtPath)
	if err != nil {
		return
	}
	if info, err := os.Stat(lockFile); err != nil || !info.Mode().IsRegular() {
		return
	}
	content, err := os.ReadFile(lockFile)
	if err != nil {
		return
	}
	owners, ours := parseAutoLockOwners(string(content))
	if !ours {
		return
	}

	found := false
	var remaining []string
	for _, existing := range owners {
		if existing == owner {
			found = true
		} else {
			remaining = append(remaining, existing)
		}
	}
	if !found {
		return
	}

	if len(remaining) == 0 {
		if err := exec.Command("git", "-C", repoRoot, "worktree", "unlock", wtPath).Run(); err != nil {
			_ = os.Remove(lockFile)
		}
		return
	}
	_ = writeAutoLockReason(lockFile, remaining)
}

// LockWorktrees applies this run's auto-lock and returns the worktrees
// actually locked -- the exact list Release must later be given.
//
// Fail-safe: a mutex it cannot take is a warning, never a reason to launch
// with the worktrees unprotected. The mutex only serializes owner-list
// bookkeeping; a lone racing append at worst leaks a lock, which cannot
// destroy data.
func LockWorktrees(repo string, worktrees []string, owner string, stdout, stderr io.Writer) []string {
	return LockWorktreesContext(context.Background(), repo, worktrees, owner, stdout, stderr)
}

// LockWorktreesContext is LockWorktrees with contributor diagnostics attached
// to the caller's stream.
func LockWorktreesContext(
	ctx context.Context, repo string, worktrees []string, owner string, stdout, stderr io.Writer,
) []string {
	logger := diagnostic.For(ctx, diagnostic.ComponentWorktree)
	m, ok := acquireMutexContext(ctx, repo, stderr)
	if !ok {
		logger.Warn("worktree auto-lock proceeding without serialization mutex",
			diagnostic.String("repo", repo), diagnostic.Int("worktree_count", len(worktrees)))
		_, _ = fmt.Fprintln(stderr, "Warning: proceeding to auto-lock without the serialization mutex;")
		_, _ = fmt.Fprintln(stderr, "         a concurrent launcher on this repo could race on lock bookkeeping.")
	}

	var locked []string
	seen := make(map[string]bool, len(worktrees))
	for _, wt := range worktrees {
		if err := addAutoLockOwner(repo, wt, owner); err != nil {
			logger.Warn("worktree auto-lock failed",
				diagnostic.String("repo", repo), diagnostic.String("worktree", wt), diagnostic.ErrorAttr(err))
			_, _ = fmt.Fprintf(stderr, "Warning: could not auto-lock %s; leaving it unchanged\n", wt)
			continue
		}
		if !seen[wt] {
			seen[wt] = true
			locked = append(locked, wt)
		}
		logger.Debug("worktree auto-lock applied",
			diagnostic.String("repo", repo), diagnostic.String("worktree", wt))
	}

	m.release() // no-op when ok was false: m is nil

	_, _ = fmt.Fprintf(stdout, "Auto-locked %d worktree(s).\n", len(locked))
	return locked
}

// ReleaseWorktreeLocks removes owner from each worktree's owner list,
// unlocking only those it was the last owner of.
//
// Fail-open: a mutex it cannot take means the locks stay. Erring toward
// over-locking cannot destroy data; dropping a still-live owner can.
func ReleaseWorktreeLocks(repo string, worktrees []string, owner string, stderr io.Writer) {
	ReleaseWorktreeLocksContext(context.Background(), repo, worktrees, owner, stderr)
}

// ReleaseWorktreeLocksContext is ReleaseWorktreeLocks with cleanup diagnostics.
func ReleaseWorktreeLocksContext(
	ctx context.Context, repo string, worktrees []string, owner string, stderr io.Writer,
) {
	if repo == "" || len(worktrees) == 0 {
		return
	}

	logger := diagnostic.For(ctx, diagnostic.ComponentWorktree)
	m, ok := acquireMutexContext(ctx, repo, stderr)
	if !ok {
		logger.Warn("worktree auto-lock cleanup left locks in place",
			diagnostic.String("repo", repo), diagnostic.Int("worktree_count", len(worktrees)))
		_, _ = fmt.Fprintln(stderr, "Warning: could not acquire worktree auto-lock mutex during cleanup;")
		_, _ = fmt.Fprintf(stderr, "         leaving auto-locks in place (release with 'git -C \"%s\" worktree unlock <path>').\n", repo)
		return
	}

	for _, wt := range worktrees {
		removeAutoLockOwner(repo, wt, owner)
		logger.Debug("worktree auto-lock owner release attempted",
			diagnostic.String("repo", repo), diagnostic.String("worktree", wt))
	}
	m.release()
}
