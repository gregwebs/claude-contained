#!/usr/bin/env bash
#
# Tests for the worktree auto-lock mutex.
#
# Regression coverage for the bug where a mutex directory left behind by a
# launcher that died mid-hold (crash, SIGKILL, kill during cleanup) would block
# every subsequent run on that repo: each new launcher timed out acquiring the
# mutex, printed "timed out ... skipping auto-lock update", and ran the
# container WITHOUT locking the hidden worktrees, so an in-container
# `git worktree prune` removed them.
#
# The scripts are sourced with CLAUDE_CONTAINED_LIB_ONLY=1, which defines the
# helper functions and returns before any container is launched. The mutex
# helpers use only mkdir/rmdir/kill/date, so no container runtime is required.
#
# Usage: tests/worktree-mutex.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"

suite() {
  set +e
  local target="$1"
  local fails=0
  local repo lock now deadpid rc start got_pid _

  _check() { # _check "description" <rc-that-should-be-0>
    if [[ "$2" -eq 0 ]]; then
      echo "  PASS: $1"
    else
      echo "  FAIL: $1"
      fails=$((fails + 1))
    fi
  }

  repo="$(mktemp -d)"
  mkdir -p "${repo}/.git"
  lock="${repo}/.git/claude-contained-worktree-locks.lock"
  now="$(date +%s)"

  # A reliably-dead PID: spawn a process and reap it.
  ( exit 0 ) & deadpid=$!; wait "$deadpid" 2>/dev/null

  # 1. Clean acquire / release round-trip.
  WORKTREE_LOCK_MUTEX_DIR=""
  with_worktree_lock_mutex "$repo" 2>/dev/null; _check "clean acquire succeeds" $?
  [[ -d "$lock" ]]; _check "acquire creates the lock dir" $?
  [[ -f "${lock}/owner" ]]; _check "acquire writes an owner file" $?
  [[ "${WORKTREE_LOCK_MUTEX_DIR:-}" == "$lock" ]]; _check "acquire sets WORKTREE_LOCK_MUTEX_DIR" $?
  release_worktree_lock_mutex
  [[ ! -e "$lock" ]]; _check "release removes the lock dir" $?
  [[ -z "${WORKTREE_LOCK_MUTEX_DIR:-}" ]]; _check "release clears WORKTREE_LOCK_MUTEX_DIR" $?

  # 2. A holder whose PID is gone is stale.
  mkdir -p "$lock"; printf '%s %s\n' "$deadpid" "$now" > "${lock}/owner"
  mutex_holder_is_stale "$lock"; _check "dead-PID holder detected as stale" $?
  rm -rf "$lock"

  # 3. A live holder with a fresh timestamp is NOT stale (no false reclaim).
  mkdir -p "$lock"; printf '%s %s\n' "$$" "$now" > "${lock}/owner"
  mutex_holder_is_stale "$lock"; rc=$?; [[ $rc -ne 0 ]]; _check "live+fresh holder NOT stale" $?
  rm -rf "$lock"

  # 4. Age fallback: an old timestamp is stale even if the PID still resolves
  #    (guards against PID reuse handing the slot to an unrelated process).
  mkdir -p "$lock"; printf '%s %s\n' "$$" "$((now - 100))" > "${lock}/owner"
  mutex_holder_is_stale "$lock"; _check "aged holder detected as stale" $?
  rm -rf "$lock"

  # 5. No owner file (crash between mkdir and owner write) is stale.
  mkdir -p "$lock"
  mutex_holder_is_stale "$lock"; _check "owner-less holder detected as stale" $?
  rm -rf "$lock"

  # 6. The regression: a stale dead-PID mutex must be reclaimed, not time out.
  mkdir -p "$lock"; printf '%s %s\n' "$deadpid" "$now" > "${lock}/owner"
  WORKTREE_LOCK_MUTEX_DIR=""
  start=$SECONDS
  with_worktree_lock_mutex "$repo" 2>/dev/null; rc=$?
  [[ $rc -eq 0 ]]; _check "stale mutex is reclaimed (acquire succeeds)" $?
  got_pid=""; read -r got_pid _ < "${lock}/owner" 2>/dev/null
  [[ "$got_pid" == "$$" ]]; _check "reclaimed mutex now owned by this process" $?
  [[ $((SECONDS - start)) -le 3 ]]; _check "reclaim is prompt (not the 5s timeout path)" $?
  release_worktree_lock_mutex
  rm -rf "$lock"

  rm -rf "$repo"
  return "$fails"
}

total_fail=0
for target in claude-contained claude-docked; do
  echo "== ${target} =="
  (
    export CLAUDE_CONTAINED_LIB_ONLY=1
    # shellcheck disable=SC1090
    source "${repo_root}/${target}" -C . >/dev/null 2>&1
    set +e
    suite "$target"
  )
  total_fail=$((total_fail + $?))
done

echo
if [[ $total_fail -ne 0 ]]; then
  echo "FAILED: ${total_fail} assertion(s) failed."
  exit 1
fi
echo "All worktree-mutex tests passed."
