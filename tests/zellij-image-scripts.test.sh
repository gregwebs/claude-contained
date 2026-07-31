#!/usr/bin/env bash
#
# Tests for the in-container Zellij scripts: image/zellij-pane-command.sh,
# image/zellij-run.sh, and image/zellij-attach.sh. These stay shell by
# ADR-0004 -- they run inside the image, where bash is a given -- and are
# target-independent: nothing here loops over CLAUDE_CONTAINED_TEST_TARGETS,
# since there is no launcher involved.
#
# Split out of tests/zellij-flags.test.sh, which keeps only the launcher-level
# --zellij cases that do run against every target.
#
# Usage: tests/zellij-image-scripts.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"

home="$(mktemp -d)"

xdg_suite() {
  set +e
  local fails=0
  local out path_out restored_path

  _check() {
    if [[ "$2" -eq 0 ]]; then
      echo "  PASS: $1"
    else
      echo "  FAIL: $1"
      fails=$((fails + 1))
    fi
  }

  restored_path="${home}/.local/bin:/opt/claude:/home/dev/.sdkman/candidates/maven/current/bin:/home/dev/.sdkman/candidates/jbang/current/bin:/opt/jbr/bin:/usr/local/bin:/usr/bin:/bin"
  out="$(
    env \
      HOME="$home" \
      PATH=/usr/bin:/bin \
      XDG_CACHE_HOME=/zellij/cache \
      XDG_DATA_HOME=/zellij/data \
      XDG_RUNTIME_DIR=/zellij/run \
      TMPDIR=/tmp \
      CLAUDE_CONTAINED_PRE_ZELLIJ_XDG_CACHE_HOME_SET=1 \
      CLAUDE_CONTAINED_PRE_ZELLIJ_XDG_CACHE_HOME=/before/cache \
      CLAUDE_CONTAINED_PRE_ZELLIJ_XDG_DATA_HOME_SET=0 \
      CLAUDE_CONTAINED_PRE_ZELLIJ_XDG_RUNTIME_DIR_SET=1 \
      CLAUDE_CONTAINED_PRE_ZELLIJ_XDG_RUNTIME_DIR=/before/run \
      CLAUDE_CONTAINED_PRE_ZELLIJ_TMPDIR_SET=1 \
      CLAUDE_CONTAINED_PRE_ZELLIJ_TMPDIR=/tmp/claude \
      CLAUDE_CONTAINED_PRE_ZELLIJ_PATH_SET=1 \
      CLAUDE_CONTAINED_PRE_ZELLIJ_PATH="$restored_path" \
      CLAUDE_CONTAINED_PRE_ZELLIJ_SHELL_SET=1 \
      CLAUDE_CONTAINED_PRE_ZELLIJ_SHELL=/bin/bash \
      CLAUDE_CONTAINED_ZELLIJ_CONFIG=/etc/claude-contained/zellij/config.kdl \
      CLAUDE_CONTAINED_ZELLIJ_SOCKET=/tmp/claude-contained-zellij-runtime/zellij/contract_version_1/test \
      CLAUDE_CONTAINED_ZELLIJ_LAYOUT_DIR=/tmp/claude-contained-zellij-runtime/layouts \
      CLAUDE_CONTAINED_ZELLIJ_TMP_DIR=/tmp/zellij-501 \
      "${repo_root}/image/zellij-pane-command.sh" env
  )"

  grep -Fqx "XDG_CACHE_HOME=/before/cache" <<<"$out" && grep -Fqx "XDG_RUNTIME_DIR=/before/run" <<<"$out" && grep -Fqx "TMPDIR=/tmp/claude" <<<"$out"
  _check "pane command restores previously-set XDG/TMPDIR vars" $?
  grep -Fqx "PATH=${restored_path}" <<<"$out" && grep -Fqx "SHELL=/bin/bash" <<<"$out"
  _check "pane command restores pre-Zellij PATH and SHELL" $?
  ! grep -Fq "XDG_DATA_HOME=" <<<"$out"
  _check "pane command unsets previously-unset XDG vars" $?
  ! grep -Fq "CLAUDE_CONTAINED_PRE_ZELLIJ_PATH=" <<<"$out"
  _check "pane command removes pre-Zellij helper env" $?
  ! grep -Fq "CLAUDE_CONTAINED_ZELLIJ_CONFIG=" <<<"$out"
  _check "pane command removes Zellij-only helper env" $?
  ! grep -Fq "CLAUDE_CONTAINED_ZELLIJ_TMP_DIR=" <<<"$out"
  _check "pane command removes Zellij temp helper env" $?
  ! grep -Fq "CLAUDE_CONTAINED_ZELLIJ_SOCKET=" <<<"$out"
  _check "pane command removes Zellij socket helper env" $?
  ! grep -Fq "CLAUDE_CONTAINED_ZELLIJ_LAYOUT_DIR=" <<<"$out"
  _check "pane command removes Zellij layout helper env" $?

  path_out="$(
    env HOME="$home" PATH=/usr/bin:/bin \
      "${repo_root}/image/zellij-pane-command.sh" /usr/bin/env
  )"
  grep -E "^PATH=" <<<"$path_out" >/dev/null && grep -F "/opt/claude" <<<"$path_out" >/dev/null
  _check "pane command includes contained tool paths when PATH was not stashed" $?

  return "$fails"
}

wrapper_suite() {
  set +e
  local fails=0
  local zbin zhome zlog session layout launch_path saved_state

  _check() {
    if [[ "$2" -eq 0 ]]; then
      echo "  PASS: $1"
    else
      echo "  FAIL: $1"
      fails=$((fails + 1))
    fi
  }

  zbin="$(mktemp -d)"
  zhome="$(mktemp -d)"
  zlog="$(mktemp)"
  session="test-$$"

  cat > "${zbin}/zellij" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail
printf '%s\n' "$*" >> "${ZELLIJ_STUB_LOG}"
printf 'PRE_PATH_SET=%s\n' "${CLAUDE_CONTAINED_PRE_ZELLIJ_PATH_SET:-}" >> "${ZELLIJ_STUB_LOG}"
printf 'PRE_PATH=%s\n' "${CLAUDE_CONTAINED_PRE_ZELLIJ_PATH:-}" >> "${ZELLIJ_STUB_LOG}"
printf 'PRE_SHELL_SET=%s\n' "${CLAUDE_CONTAINED_PRE_ZELLIJ_SHELL_SET:-}" >> "${ZELLIJ_STUB_LOG}"
printf 'PRE_SHELL=%s\n' "${CLAUDE_CONTAINED_PRE_ZELLIJ_SHELL:-}" >> "${ZELLIJ_STUB_LOG}"
for arg in "$@"; do
  if [[ "$arg" == "list-sessions" ]]; then
    if [[ "${ZELLIJ_STUB_LIST_MODE:-exited}" == "exited" ]]; then
      printf '%s (EXITED)\n' "${ZELLIJ_STUB_SESSION}"
    elif [[ "${ZELLIJ_STUB_LIST_MODE:-exited}" == "live" ]]; then
      printf '%s\n' "${ZELLIJ_STUB_SESSION}"
    fi
    exit 0
  fi
done
exit 0
EOF
  chmod +x "${zbin}/zellij"

  launch_path="${zbin}:/opt/claude:/usr/local/bin:/usr/bin:/bin"
  saved_state="${zhome}/.claude-contained/zellij/cache/zellij/contract_version_1/session_info/${session}"
  mkdir -p "$saved_state"
  : > "${saved_state}/session-metadata.kdl"
  ZELLIJ_STUB_LOG="$zlog" ZELLIJ_STUB_LIST_MODE=empty ZELLIJ_STUB_SESSION="$session" HOME="$zhome" PATH="$launch_path" SHELL=/bin/bash \
    CLAUDE_CONTAINED_ZELLIJ_WAIT_SECONDS=0 \
    "${repo_root}/image/zellij-run.sh" "$session" -- echo "hello world" >/dev/null 2>&1
  _check "zellij-run starts a fresh session when none is listed" $?
  if [[ ! -e "$saved_state" ]]; then check_rc=0; else check_rc=1; fi
  _check "zellij-run removes saved cache state before fresh startup" "$check_rc"

  grep -Fq -- "--config /etc/claude-contained/zellij/config.kdl --data-dir ${zhome}/.claude-contained/zellij/data attach --forget --create ${session} options --default-layout ${session}" "$zlog"
  _check "zellij-run forgets stale saved state and uses named layout startup" $?
  ! grep -Fq -- "--server" "$zlog"
  _check "zellij-run lets XDG_RUNTIME_DIR control the Zellij socket" $?
  grep -Fqx "PRE_PATH_SET=1" "$zlog" && grep -Fqx "PRE_PATH=${launch_path}" "$zlog" && grep -Fqx "PRE_SHELL_SET=1" "$zlog" && grep -Fqx "PRE_SHELL=/bin/bash" "$zlog"
  _check "zellij-run stashes launch PATH and SHELL for command panes" $?

  layout="/tmp/claude-contained-zellij-runtime/layouts/${session}.kdl"
  grep -Fq 'args "echo" "hello world"' "$layout"
  _check "zellij-run writes the initial pane command into the layout" $?

  [[ -d "/tmp/zellij-$(id -u)/zellij-log" ]]
  _check "zellij-run pre-creates Zellij's temp log directory" $?
  [[ -d "/tmp/claude-contained-zellij-runtime/zellij/contract_version_1" ]]
  _check "zellij-run pre-creates Zellij's runtime socket directory" $?
  [[ -d "/tmp/claude-contained-zellij-runtime/layouts" ]]
  _check "zellij-run pre-creates Zellij's runtime layout directory" $?
  [[ -d "${zhome}/.claude-contained/zellij/cache/org/Zellij-Contributors/Zellij" ]]
  _check "zellij-run pre-creates Zellij's project cache directory" $?

  : > "$zlog"
  ZELLIJ_STUB_LOG="$zlog" ZELLIJ_STUB_LIST_MODE=exited ZELLIJ_STUB_SESSION="$session" HOME="$zhome" PATH="${zbin}:$PATH" \
    CLAUDE_CONTAINED_ZELLIJ_WAIT_SECONDS=0 \
    "${repo_root}/image/zellij-run.sh" "$session" -- echo "hello world" >/dev/null 2>&1
  _check "zellij-run treats (EXITED) list-sessions output as not live" $?
  grep -Fq -- "--config /etc/claude-contained/zellij/config.kdl --data-dir ${zhome}/.claude-contained/zellij/data attach --forget --create ${session} options --default-layout ${session}" "$zlog"
  _check "zellij-run replaces exited saved sessions with the requested layout" $?

  mkdir -p "$saved_state"
  : > "${saved_state}/session-metadata.kdl"
  ZELLIJ_STUB_LOG="$zlog" ZELLIJ_STUB_LIST_MODE=live ZELLIJ_STUB_SESSION="$session" HOME="$zhome" PATH="${zbin}:$PATH" \
    "${repo_root}/image/zellij-run.sh" "$session" -- echo "hello world" >/dev/null 2>&1
  rc=$?
  [[ $rc -eq 1 && -e "$saved_state" ]]
  _check "zellij-run refuses live sessions without deleting their saved state" $?

  ZELLIJ_STUB_LOG="$zlog" ZELLIJ_STUB_SESSION="$session" HOME="$zhome" PATH="${zbin}:$PATH" \
    "${repo_root}/image/zellij-attach.sh" "$session" >/dev/null 2>&1
  rc=$?
  [[ $rc -eq 1 ]]
  _check "zellij-attach refuses an exited saved session" $?

  rm -rf "$zbin" "$zhome"
  rm -f "$zlog" "$layout"
  return "$fails"
}

total=0

echo "== zellij-pane-command =="
xdg_suite
total=$((total + $?))

echo "== zellij wrappers =="
wrapper_suite
total=$((total + $?))

rm -rf "$home"

if [[ "$total" -gt 0 ]]; then
  echo
  echo "${total} Zellij image-script test(s) failed."
  exit 1
fi

echo
echo "All Zellij image-script tests passed."
