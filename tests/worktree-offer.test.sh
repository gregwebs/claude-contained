#!/usr/bin/env bash
#
# Black-box tests for the worktree auto-lock offer, driven through the full
# launcher rather than by sourcing its internals -- so the suite runs
# unmodified against the Go binary via CLAUDE_CONTAINED_TEST_TARGETS.
#
# The stub `container`/`docker` snapshots a path mid-run (right before the
# fake "run" call returns), which is how this proves a worktree was actually
# LOCKED while the container was up, not merely settled-unlocked afterward:
# the launcher's own cleanup releases every lock it took before the process
# ever exits, so the final on-disk state alone cannot distinguish "correctly
# locked then released" from "never locked at all".
#
# Usage: tests/worktree-offer.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"

fails=0
_check() { # _check "description" <rc-that-should-be-0>
  if [[ "$2" -eq 0 ]]; then
    echo "  PASS: $1"
  else
    echo "  FAIL: $1"
    fails=$((fails + 1))
  fi
}

# resolve_dir <existing-dir>; portable realpath (macOS's /var -> /private/var
# symlink means a bare mktemp path never matches git's own resolved output).
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

lock_reason() { # lock_reason <main-repo> <wt-path>; prints "" if unlocked
  git -C "$1" worktree list --porcelain 2>/dev/null \
    | awk -v p="$2" '
        $1=="worktree"{cur=$2}
        $1=="locked" && cur==p {sub(/^locked ?/,""); print; exit}'
}

# make_repo_with_worktree <root> <wt-name>; echoes "<main> <wt>", both
# resolved to the form git itself will report back.
make_repo_with_worktree() {
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

# launcher_run <target> <home> <project> <stdin-text> [extra args...]; leaves
# stdout/stderr in lr_stdout/lr_stderr.
launcher_run() {
  local target="$1" home="$2" project="$3" stdin_text="$4"
  shift 4
  lr_stdout="$(mktemp)"
  lr_stderr="$(mktemp)"
  printf '%s' "$stdin_text" | env HOME="$home" PATH="${stub_dir}:$PATH" \
    "${repo_root}/${target}" -N -s -C "$project" "$@" \
    >"$lr_stdout" 2>"$lr_stderr"
}

read -ra targets <<< "${CLAUDE_CONTAINED_TEST_TARGETS:-bin/claude-contained bin/claude-contained-docked}"

for target in "${targets[@]}"; do
  echo "== ${target} =="

  # --- Scenario A: -W skips the prompt and locks; released on exit ---
  setup_runtime_stubs
  home="$(mktemp -d)"; root="$(mktemp -d)"
  read -r main wt <<< "$(make_repo_with_worktree "$root" hidden-wt)"
  lock_file="$(git -C "$wt" rev-parse --absolute-git-dir)/locked"

  WT_SNAPSHOT_PATHS="$lock_file" launcher_run "$target" "$home" "$main" "" -W
  if [[ -e "${lock_file}.snapshot" ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: -W locks the hidden worktree while the container runs" "$check_rc"
  if [[ -e "${lock_file}.snapshot" ]]; then
    grep -q '^cc-autolocked-by:' "${lock_file}.snapshot"
    _check "${target}: -W lock reason carries the owner token" $?
  else
    _check "${target}: -W lock reason carries the owner token" 1
  fi
  worktree_is_locked "$main" "$wt"; rc=$?
  if [[ $rc -ne 0 ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: -W releases the lock once the run completes" "$check_rc"
  rm -rf "$stub_dir" "$home" "$root" "$lr_stdout" "$lr_stderr"

  # --- Scenario B: interactive "Y" locks; released on exit ---
  setup_runtime_stubs
  home="$(mktemp -d)"; root="$(mktemp -d)"
  read -r main wt <<< "$(make_repo_with_worktree "$root" hidden-wt)"
  lock_file="$(git -C "$wt" rev-parse --absolute-git-dir)/locked"

  WT_SNAPSHOT_PATHS="$lock_file" launcher_run "$target" "$home" "$main" "Y
"
  if [[ -e "${lock_file}.snapshot" ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: accepting the interactive offer locks the hidden worktree" "$check_rc"
  grep -q "hidden from container (prune risk)" "$lr_stdout"
  _check "${target}: prune-risk line appears on stdout" $?
  grep -qE '^Auto-locked 1 worktree\(s\)\.$' "$lr_stdout"
  _check "${target}: Auto-locked summary appears on stdout" $?
  worktree_is_locked "$main" "$wt"; rc=$?
  if [[ $rc -ne 0 ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: interactive accept releases the lock on exit" "$check_rc"
  rm -rf "$stub_dir" "$home" "$root" "$lr_stdout" "$lr_stderr"

  # --- Scenario C: interactive "n" declines; no lock file is ever written ---
  setup_runtime_stubs
  home="$(mktemp -d)"; root="$(mktemp -d)"
  read -r main wt <<< "$(make_repo_with_worktree "$root" hidden-wt)"
  lock_file="$(git -C "$wt" rev-parse --absolute-git-dir)/locked"

  WT_SNAPSHOT_PATHS="$lock_file" launcher_run "$target" "$home" "$main" "n
"
  if [[ ! -e "${lock_file}.snapshot" && ! -e "$lock_file" ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: declining never writes a lock file" "$check_rc"
  grep -q "hidden from container (prune risk)" "$lr_stdout"
  _check "${target}: prune-risk line still appears even when declined" $?
  ! grep -q "Auto-locked" "$lr_stdout"
  _check "${target}: no Auto-locked line when declined" $?
  rm -rf "$stub_dir" "$home" "$root" "$lr_stdout" "$lr_stderr"

  # --- Scenario D: a user lock is byte-identical afterwards ---
  setup_runtime_stubs
  home="$(mktemp -d)"; root="$(mktemp -d)"
  read -r main wt <<< "$(make_repo_with_worktree "$root" user-wt)"
  git -C "$main" worktree lock --reason mine "$wt" >/dev/null 2>&1
  before="$(lock_reason "$main" "$wt")"

  launcher_run "$target" "$home" "$main" "" -W
  after="$(lock_reason "$main" "$wt")"
  if [[ "$before" == "mine" && "$after" == "mine" ]]; then check_rc=0; else check_rc=1; fi
  _check "${target}: a user lock is byte-identical after the run" "$check_rc"
  rm -rf "$stub_dir" "$home" "$root" "$lr_stdout" "$lr_stderr"

  # --- Scenario E: a pre-seeded second owner survives our release ---
  setup_runtime_stubs
  home="$(mktemp -d)"; root="$(mktemp -d)"
  read -r main wt <<< "$(make_repo_with_worktree "$root" hidden-wt)"
  git -C "$main" worktree lock --reason "cc-autolocked-by: aic-other-1111" "$wt" >/dev/null 2>&1
  lock_file="$(git -C "$wt" rev-parse --absolute-git-dir)/locked"

  WT_SNAPSHOT_PATHS="$lock_file" launcher_run "$target" "$home" "$main" "" -W
  if [[ -e "${lock_file}.snapshot" ]]; then
    grep -q 'aic-other-1111' "${lock_file}.snapshot"
    _check "${target}: mid-run owner list still carries the other owner" $?
  else
    _check "${target}: mid-run owner list still carries the other owner" 1
  fi
  after="$(lock_reason "$main" "$wt")"
  [[ "$after" == *"aic-other-1111"* ]]
  _check "${target}: other owner survives our own release" $?
  worktree_is_locked "$main" "$wt"
  _check "${target}: worktree is still locked (another owner remains)" $?
  rm -rf "$stub_dir" "$home" "$root" "$lr_stdout" "$lr_stderr"

done

echo
if [[ "$fails" -gt 0 ]]; then
  echo "${fails} worktree-offer test(s) failed."
  exit 1
fi
echo "All worktree-offer tests passed."
