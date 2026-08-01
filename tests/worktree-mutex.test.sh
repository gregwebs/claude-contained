#!/usr/bin/env bash
#
# Black-box tests for the worktree auto-lock mutex, driven through the full
# launcher (-W reaches the offer with no interactive prompt) rather than by
# sourcing internals -- so this suite runs unmodified against the Go binary
# via CLAUDE_CONTAINED_TEST_TARGETS.
#
# Regression coverage for the bug where a mutex directory left behind by a
# launcher that died mid-hold (crash, SIGKILL, kill during cleanup) would block
# every subsequent run on that repo: each new launcher timed out acquiring the
# mutex and ran the container WITHOUT locking the hidden worktrees, so an
# in-container `git worktree prune` removed them.
#
# The live-holder scenario genuinely waits out the ~5s acquire timeout, twice
# over (once for the offer, once for cleanup -- see that scenario's comment):
# there is no override hook from outside the launcher for the mutex's polling
# constants (bash's are literals; Go's are test-only). That cost is paid here,
# once per target, rather than in the golden test suite (cmd/claude-contained),
# which has no teardown hook for a live holder process either -- the U1/U2
# unit tests in internal/host/worktreelock_test.go cover the same fail-safe
# and fail-open warnings with a synthesized (non-live) holder instead.
#
# Usage: tests/worktree-mutex.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"
unset CLAUDE_CONTAINED_LOG_LEVEL

fails=0
_check() { # _check "description" <rc-that-should-be-0>
  if [[ "$2" -eq 0 ]]; then
    echo "  PASS: $1"
  else
    echo "  FAIL: $1"
    fails=$((fails + 1))
  fi
}

resolve_dir() { (cd "$1" && pwd -P); }

setup_runtime_stubs() { # sets stub_dir
  stub_dir="$(mktemp -d)"
  cat > "${stub_dir}/container" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
case "${1:-}" in
  system) exit 0 ;;
  list) exit 0 ;;
esac
if [[ "${1:-}" == "run" && -n "${WT_SNAPSHOT_PATHS:-}" ]]; then
  IFS=':' read -ra __p <<< "$WT_SNAPSHOT_PATHS"
  for p in "${__p[@]}"; do
    [[ -e "$p" ]] && cp -a "$p" "${p}.snapshot"
  done
fi
exit 0
EOF
  cat > "${stub_dir}/docker" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
case "${1:-}" in
  info|ps) exit 0 ;;
esac
if [[ "${1:-}" == "run" && -n "${WT_SNAPSHOT_PATHS:-}" ]]; then
  IFS=':' read -ra __p <<< "$WT_SNAPSHOT_PATHS"
  for p in "${__p[@]}"; do
    [[ -e "$p" ]] && cp -a "$p" "${p}.snapshot"
  done
fi
exit 0
EOF
  chmod +x "${stub_dir}/container" "${stub_dir}/docker"
}

worktree_is_locked() { # worktree_is_locked <main-repo> <wt-path>
  git -C "$1" worktree list --porcelain 2>/dev/null \
    | awk -v p="$2" '
        $1=="worktree"{cur=$2}
        $1=="locked" && cur==p {found=1}
        END{exit found?0:1}'
}

make_repo_with_worktree() { # echoes "<main> <wt>"
  local root="$1" name="$2" main wt
  main="${root}/main"
  wt="${root}/${name}"
  mkdir -p "$main"
  git init -q "$main"
  git -C "$main" -c user.email=t@example.com -c user.name=test commit -q --allow-empty -m init
  git -C "$main" worktree add -q --detach "$wt" >/dev/null 2>&1
  main="$(resolve_dir "$main")"
  wt="$(resolve_dir "$wt")"
  echo "$main $wt"
}

launcher_run() { # launcher_run <target> <home> <project> <stdin-text> [args...]
  local target="$1" home="$2" project="$3" stdin_text="$4"
  shift 4
  lr_stdout="$(mktemp)"
  lr_stderr="$(mktemp)"
  printf '%s' "$stdin_text" | env HOME="$home" PATH="${stub_dir}:$PATH" \
    "${repo_root}/${target}" -N -s -C "$project" "$@" \
    >"$lr_stdout" 2>"$lr_stderr"
}

deadpid() { # echoes a PID reliably not alive
  ( exit 0 ) & local p=$!
  wait "$p" 2>/dev/null
  echo "$p"
}

read -ra targets <<< "${CLAUDE_CONTAINED_TEST_TARGETS:-bin/claude-contained bin/claude-contained-docked}"

for target in "${targets[@]}"; do
  echo "== ${target} =="

  # --- Scenario A: a dead-PID mutex is reclaimed promptly, not timed out ---
  setup_runtime_stubs
  home="$(mktemp -d)"; root="$(mktemp -d)"
  read -r main wt <<< "$(make_repo_with_worktree "$root" hidden-wt)"
  lock_file="$(git -C "$wt" rev-parse --absolute-git-dir)/locked"
  mutex_dir="${main}/.git/claude-contained-worktree-locks.lock"
  dead="$(deadpid)"
  mkdir -p "$mutex_dir"
  printf '%s %s\n' "$dead" "$(date +%s)" > "${mutex_dir}/owner"

  start=$SECONDS
  WT_SNAPSHOT_PATHS="$lock_file" launcher_run "$target" "$home" "$main" "" -W
  elapsed=$((SECONDS - start))

  if [[ -e "${lock_file}.snapshot" ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: dead-PID mutex is reclaimed and the worktree is locked" "$check_rc"
  if [[ ! -e "$mutex_dir" ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: reclaimed mutex directory is gone afterward" "$check_rc"
  grep -q "reclaiming stale worktree auto-lock mutex" "$lr_stderr"
  _check "${target}: stderr carries the reclaim note" $?
  if [[ $elapsed -le 3 ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: reclaim is prompt, not the timeout path (${elapsed}s)" "$check_rc"
  worktree_is_locked "$main" "$wt"; rc=$?
  if [[ $rc -ne 0 ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: worktree is unlocked once the run completes" "$check_rc"
  rm -rf "$stub_dir" "$home" "$root" "$lr_stdout" "$lr_stderr"

  # --- Scenario B: an aged live-PID mutex is likewise reclaimed (guards PID
  #     reuse: a resolvable PID must not by itself block reclaiming) ---
  setup_runtime_stubs
  home="$(mktemp -d)"; root="$(mktemp -d)"
  read -r main wt <<< "$(make_repo_with_worktree "$root" hidden-wt)"
  lock_file="$(git -C "$wt" rev-parse --absolute-git-dir)/locked"
  mutex_dir="${main}/.git/claude-contained-worktree-locks.lock"
  mkdir -p "$mutex_dir"
  printf '%s %s\n' "$$" "$(( $(date +%s) - 100 ))" > "${mutex_dir}/owner"

  start=$SECONDS
  WT_SNAPSHOT_PATHS="$lock_file" launcher_run "$target" "$home" "$main" "" -W
  elapsed=$((SECONDS - start))

  if [[ -e "${lock_file}.snapshot" ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: aged live-PID mutex is reclaimed and the worktree is locked" "$check_rc"
  if [[ ! -e "$mutex_dir" ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: reclaimed mutex directory is gone afterward (aged case)" "$check_rc"
  grep -q "reclaiming stale worktree auto-lock mutex" "$lr_stderr"
  _check "${target}: stderr carries the reclaim note (aged case)" $?
  if [[ $elapsed -le 3 ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: aged reclaim is prompt, not the timeout path (${elapsed}s)" "$check_rc"
  worktree_is_locked "$main" "$wt"; rc=$?
  if [[ $rc -ne 0 ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: worktree is unlocked once the run completes (aged case)" "$check_rc"
  rm -rf "$stub_dir" "$home" "$root" "$lr_stdout" "$lr_stderr"

  # --- Scenario C: a live, fresh holder is not reclaimed -- the launcher
  #     times out (twice: once locking, fail-safe; once at cleanup,
  #     fail-open) but applies the lock anyway and leaves it in place. ---
  setup_runtime_stubs
  home="$(mktemp -d)"; root="$(mktemp -d)"
  read -r main wt <<< "$(make_repo_with_worktree "$root" hidden-wt)"
  lock_file="$(git -C "$wt" rev-parse --absolute-git-dir)/locked"
  mutex_dir="${main}/.git/claude-contained-worktree-locks.lock"
  mkdir -p "$mutex_dir"
  sleep 30 & holder_pid=$!
  printf '%s %s\n' "$holder_pid" "$(date +%s)" > "${mutex_dir}/owner"

  start=$SECONDS
  WT_SNAPSHOT_PATHS="$lock_file" launcher_run "$target" "$home" "$main" "" -W
  elapsed=$((SECONDS - start))
  kill "$holder_pid" 2>/dev/null
  wait "$holder_pid" 2>/dev/null

  # Two ~5s timeouts back to back: the offer's own acquire, then cleanup's.
  [[ $elapsed -ge 8 ]]
  _check "${target}: live+fresh holder makes the launcher wait out both timeouts (${elapsed}s)" $?
  grep -q "timed out waiting for worktree auto-lock mutex" "$lr_stderr"
  _check "${target}: stderr carries the timeout warning" $?
  grep -q "proceeding to auto-lock without the serialization mutex" "$lr_stderr"
  _check "${target}: stderr carries the fail-safe (lock-anyway) warning" $?
  grep -q "could not acquire worktree auto-lock mutex during cleanup" "$lr_stderr"
  _check "${target}: stderr carries the fail-open (leave-in-place) warning" $?
  [[ -e "${lock_file}.snapshot" ]]
  _check "${target}: the worktree is locked despite the unavailable mutex (fail-safe)" $?
  worktree_is_locked "$main" "$wt"
  _check "${target}: the lock is left in place since cleanup couldn't acquire the mutex (fail-open)" $?

  # Manual teardown: the launcher could not release this one, so we must.
  git -C "$main" worktree unlock "$wt" >/dev/null 2>&1
  rm -rf "$mutex_dir"
  rm -rf "$stub_dir" "$home" "$root" "$lr_stdout" "$lr_stderr"

done

echo
if [[ "$fails" -gt 0 ]]; then
  echo "${fails} worktree-mutex test(s) failed."
  exit 1
fi
echo "All worktree-mutex tests passed."
