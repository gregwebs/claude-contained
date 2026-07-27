#!/usr/bin/env bash
#
# Tests for --zellij launcher behavior across Apple Containers and Docker.
#
# The runtime commands are stubbed. The important contract is visible at the
# launcher boundary: public flags parse the same way for both launchers,
# Zellij-backed containers are discovered from inspectable env markers, attach
# never falls back to creation, and the top-level container command is the
# repo-owned zellij-run wrapper.
#
# Usage: tests/zellij-flags.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"

stub_dir="$(mktemp -d)"
proj="$(mktemp -d)"
home="$(mktemp -d)"

cat > "${stub_dir}/container" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail

mode="${ZELLIJ_STUB_MODE:-none}"
session1="${ZELLIJ_STUB_SESSION1:-alpha}"
session2="${ZELLIJ_STUB_SESSION2:-beta}"

if [[ "${1:-}" == "system" ]]; then
  exit 0
fi

if [[ "${1:-}" == "list" ]]; then
  case "$mode" in
    one|same) echo "aic-z1" ;;
    two) echo "aic-z1"; echo "aic-z2" ;;
    normal) echo "aic-normal" ;;
  esac
  exit 0
fi

if [[ "${1:-}" == "inspect" ]]; then
  case "${2:-}" in
    aic-z1)
      printf '[{"configuration":{"initProcess":{"environment":["CLAUDE_CONTAINED_ZELLIJ=1","CLAUDE_CONTAINED_ZELLIJ_SESSION=%s"]}}}]\n' "$session1"
      ;;
    aic-z2)
      printf '[{"configuration":{"initProcess":{"environment":["CLAUDE_CONTAINED_ZELLIJ=1","CLAUDE_CONTAINED_ZELLIJ_SESSION=%s"]}}}]\n' "$session2"
      ;;
    *)
      printf '[{"configuration":{"initProcess":{"environment":[]}}}]\n'
      ;;
  esac
  exit 0
fi

printf '%s\n' "$@"
EOF

cat > "${stub_dir}/docker" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail

mode="${ZELLIJ_STUB_MODE:-none}"
session1="${ZELLIJ_STUB_SESSION1:-alpha}"
session2="${ZELLIJ_STUB_SESSION2:-beta}"

if [[ "${1:-}" == "info" ]]; then
  exit 0
fi

if [[ "${1:-}" == "ps" ]]; then
  case "$mode" in
    one|same) echo "aic-z1" ;;
    two) echo "aic-z1"; echo "aic-z2" ;;
    normal) echo "aic-normal" ;;
  esac
  exit 0
fi

if [[ "${1:-}" == "inspect" ]]; then
  last=""
  for arg in "$@"; do
    last="$arg"
  done
  case "$last" in
    aic-z1)
      printf 'CLAUDE_CONTAINED_ZELLIJ=1\nCLAUDE_CONTAINED_ZELLIJ_SESSION=%s\n' "$session1"
      ;;
    aic-z2)
      printf 'CLAUDE_CONTAINED_ZELLIJ=1\nCLAUDE_CONTAINED_ZELLIJ_SESSION=%s\n' "$session2"
      ;;
  esac
  exit 0
fi

printf '%s\n' "$@"
EOF

chmod +x "${stub_dir}/container" "${stub_dir}/docker"

launcher_run() { # launcher_run <target> <stdout-file> <stderr-file> <flags...>
  local target="$1" out_file="$2" err_file="$3"
  shift 3

  HOME="$home" PATH="${stub_dir}:$PATH" "${repo_root}/${target}" "$@" >"$out_file" 2>"$err_file"
}

line_has() { # line_has <file> <exact-line>
  grep -Fqx -- "$2" "$1"
}

file_has() { # file_has <file> <fixed-string>
  grep -Fq -- "$2" "$1"
}

file_missing() { # file_missing <file> <fixed-string>
  ! file_has "$1" "$2"
}

suite() {
  set +e
  local target="$1"
  local fails=0
  local out err rc

  _check() { # _check "description" <rc-that-should-be-0>
    if [[ "$2" -eq 0 ]]; then
      echo "  PASS: $1"
    else
      echo "  FAIL: $1"
      fails=$((fails + 1))
    fi
  }

  out="$(mktemp)"
  err="$(mktemp)"

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --session=Good_1.2-3 -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "CLAUDE_CONTAINED_ZELLIJ=1" && line_has "$out" "CLAUDE_CONTAINED_ZELLIJ_SESSION=Good_1.2-3"
  _check "--session=NAME accepts valid names and emits env markers" $?
  line_has "$out" "zellij-run" && line_has "$out" "Good_1.2-3" && line_has "$out" "bash" && ! line_has "$out" "/usr/local/bin/shell-run"
  _check "--zellij --shell launches bash inside zellij-run" $?
  file_has "$out" "missing Zellij support" && file_has "$out" "${target} --rebuild=full"
  _check "--zellij command includes stale-image rebuild hint" $?
  if [[ "$target" == "claude-docked" ]]; then
    line_has "$out" "claude-contained.zellij=1" && line_has "$out" "claude-contained.zellij.session=Good_1.2-3"
    _check "Docker run emits Zellij labels" $?
  fi

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --session= -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "--session requires a non-empty name"
  _check "--session= rejects empty names" $?

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --session=bad/name -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "invalid Zellij session name"
  _check "--session=NAME rejects slashes" $?

  # With no positionals left, the space form is unambiguous and accepted.
  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --session loose-name -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "CLAUDE_CONTAINED_ZELLIJ_SESSION=loose-name"
  _check "--session NAME space form is accepted" $?

  # --new-session is a force flag now; it must not swallow a following name.
  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --new-session=review -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "use --session=NAME"
  _check "--new-session=NAME is rejected in favor of --session" $?

  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 1 ]] && file_has "$err" "already running" && file_has "$err" "--zellij --attach"
  _check "plain --zellij refuses when any Zellij container is live" $?

  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij --session=alpha -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 1 ]] && file_has "$err" "already live" && file_has "$err" "--session=NAME"
  _check "new launch refuses if target session is live" $?

  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij --new-session -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "zellij-run"
  _check "bare --new-session allows another live Zellij container" $?

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --attach --session alpha
  rc=$?
  [[ $rc -eq 1 ]] && file_has "$err" "No live Zellij session named alpha" && file_missing "$out" "run"
  _check "--zellij --attach --session NAME never falls back to container creation" $?

  # Under --zellij the session is named only by --session; -a must refuse a name.
  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij --attach alpha
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "takes no name with --zellij"
  _check "--zellij --attach NAME is rejected in favor of --session" $?

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --attach --shell
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "cannot be combined with --shell"
  _check "--zellij --attach --shell is rejected" $?

  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij --attach --session alpha
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "exec" && line_has "$out" "aic-z1" && line_has "$out" "srt-run" && line_has "$out" "/usr/local/bin/zellij-attach" && line_has "$out" "alpha"
  _check "--zellij --attach --session NAME execs zellij-attach through srt-run" $?

  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij --attach --no-sandbox --session alpha
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "/usr/local/bin/zellij-attach" && ! line_has "$out" "srt-run"
  _check "--zellij attach drops srt-run under --no-sandbox" $?

  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij --attach
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "/usr/local/bin/zellij-attach" && line_has "$out" "alpha"
  _check "bare --zellij --attach directly attaches when exactly one session is live" $?

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --session=gamma -N -t codex -C "$proj" -- --model gpt-5
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "CLAUDE_CONTAINED_ZELLIJ_SESSION=gamma" && line_has "$out" "zellij-run" && line_has "$out" "codex" && line_has "$out" "--model" && line_has "$out" "gpt-5"
  _check "--zellij wraps tool command and preserves tool args" $?

  rm -f "$out" "$err"
  return "$fails"
}

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
  [[ ! -e "$saved_state" ]]
  _check "zellij-run removes saved cache state before fresh startup" $?

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
for target in claude-contained claude-docked; do
  echo "== ${target} =="
  suite "$target"
  total=$((total + $?))
done

echo "== zellij-pane-command =="
xdg_suite
total=$((total + $?))

echo "== zellij wrappers =="
wrapper_suite
total=$((total + $?))

rm -rf "$stub_dir" "$proj" "$home"

if [[ "$total" -gt 0 ]]; then
  echo
  echo "${total} Zellij test(s) failed."
  exit 1
fi

echo
echo "All Zellij tests passed."
