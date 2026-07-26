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
      printf '{"configuration":{"initProcess":{"environment":["CLAUDE_CONTAINED_ZELLIJ=1","CLAUDE_CONTAINED_ZELLIJ_SESSION=%s"]}}}\n' "$session1"
      ;;
    aic-z2)
      printf '{"configuration":{"initProcess":{"environment":["CLAUDE_CONTAINED_ZELLIJ=1","CLAUDE_CONTAINED_ZELLIJ_SESSION=%s"]}}}\n' "$session2"
      ;;
    *)
      printf '{"configuration":{"initProcess":{"environment":[]}}}\n'
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

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --new-session=Good_1.2-3 -N -s "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "CLAUDE_CONTAINED_ZELLIJ=1" && line_has "$out" "CLAUDE_CONTAINED_ZELLIJ_SESSION=Good_1.2-3"
  _check "--new-session=NAME accepts valid names and emits env markers" $?
  line_has "$out" "zellij-run" && line_has "$out" "Good_1.2-3" && line_has "$out" "bash"
  _check "--zellij --shell launches bash inside zellij-run" $?
  file_has "$out" "missing Zellij support" && file_has "$out" "${target} --rebuild=full"
  _check "--zellij command includes stale-image rebuild hint" $?
  if [[ "$target" == "claude-docked" ]]; then
    line_has "$out" "claude-contained.zellij=1" && line_has "$out" "claude-contained.zellij.session=Good_1.2-3"
    _check "Docker run emits Zellij labels" $?
  fi

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --new-session= -N -s "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "Zellij session name cannot be empty"
  _check "--new-session= rejects empty names" $?

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --new-session=bad/name -N -s "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "invalid Zellij session name"
  _check "--new-session=NAME rejects slashes" $?

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --new-session loose-name -N -s "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "require --new-session=NAME"
  _check "--new-session NAME is rejected as ambiguous" $?

  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij -N -s "$proj"
  rc=$?
  [[ $rc -eq 1 ]] && file_has "$err" "already running" && file_has "$err" "--zellij --attach"
  _check "plain --zellij refuses when any Zellij container is live" $?

  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij --new-session=alpha -N -s "$proj"
  rc=$?
  [[ $rc -eq 1 ]] && file_has "$err" "already live" && file_has "$err" "--new-session=NAME"
  _check "new/resurrect launch refuses if target session is live" $?

  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij --new-session -N -s "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "zellij-run"
  _check "bare --new-session allows another live Zellij container" $?

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --attach alpha
  rc=$?
  [[ $rc -eq 1 ]] && file_has "$err" "No live Zellij session named alpha" && file_missing "$out" "run"
  _check "--zellij --attach NAME never falls back to container creation" $?

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --attach --shell
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "cannot be combined with --shell"
  _check "--zellij --attach --shell is rejected" $?

  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij --attach alpha
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "exec" && line_has "$out" "aic-z1" && line_has "$out" "srt-run" && line_has "$out" "/usr/local/bin/zellij-attach" && line_has "$out" "alpha"
  _check "--zellij --attach NAME execs zellij-attach through srt-run" $?

  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij --attach --no-sandbox alpha
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "/usr/local/bin/zellij-attach" && ! line_has "$out" "srt-run"
  _check "--zellij attach drops srt-run under --no-sandbox" $?

  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij --attach
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "/usr/local/bin/zellij-attach" && line_has "$out" "alpha"
  _check "bare --zellij --attach directly attaches when exactly one session is live" $?

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --new-session=gamma -N -t codex "$proj" -- --model gpt-5
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "CLAUDE_CONTAINED_ZELLIJ_SESSION=gamma" && line_has "$out" "zellij-run" && line_has "$out" "codex" && line_has "$out" "--model" && line_has "$out" "gpt-5"
  _check "--zellij wraps tool command and preserves tool args" $?

  rm -f "$out" "$err"
  return "$fails"
}

xdg_suite() {
  set +e
  local fails=0
  local out

  _check() {
    if [[ "$2" -eq 0 ]]; then
      echo "  PASS: $1"
    else
      echo "  FAIL: $1"
      fails=$((fails + 1))
    fi
  }

  out="$(
    env \
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
      CLAUDE_CONTAINED_ZELLIJ_CONFIG=/etc/claude-contained/zellij/config.kdl \
      CLAUDE_CONTAINED_ZELLIJ_TMP_DIR=/tmp/zellij-501 \
      "${repo_root}/image/zellij-pane-command.sh" env
  )"

  grep -Fqx "XDG_CACHE_HOME=/before/cache" <<<"$out" && grep -Fqx "XDG_RUNTIME_DIR=/before/run" <<<"$out" && grep -Fqx "TMPDIR=/tmp/claude" <<<"$out"
  _check "pane command restores previously-set XDG/TMPDIR vars" $?
  ! grep -Fq "XDG_DATA_HOME=" <<<"$out"
  _check "pane command unsets previously-unset XDG vars" $?
  ! grep -Fq "CLAUDE_CONTAINED_ZELLIJ_CONFIG=" <<<"$out"
  _check "pane command removes Zellij-only helper env" $?
  ! grep -Fq "CLAUDE_CONTAINED_ZELLIJ_TMP_DIR=" <<<"$out"
  _check "pane command removes Zellij temp helper env" $?

  return "$fails"
}

wrapper_suite() {
  set +e
  local fails=0
  local zbin zhome zlog session layout

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
for arg in "$@"; do
  if [[ "$arg" == "list-sessions" ]]; then
    printf '%s (EXITED)\n' "${ZELLIJ_STUB_SESSION}"
    exit 0
  fi
done
exit 0
EOF
  chmod +x "${zbin}/zellij"

  ZELLIJ_STUB_LOG="$zlog" ZELLIJ_STUB_SESSION="$session" HOME="$zhome" PATH="${zbin}:$PATH" \
    CLAUDE_CONTAINED_ZELLIJ_WAIT_SECONDS=0 \
    "${repo_root}/image/zellij-run.sh" "$session" -- echo "hello world" >/dev/null 2>&1
  _check "zellij-run treats (EXITED) list-sessions output as not live" $?

  grep -Fq -- "--config /etc/claude-contained/zellij/config.kdl --data-dir ${zhome}/.claude-contained/zellij/data --server /tmp/claude-contained-zellij-runtime/${session}.sock attach --create ${session} options --default-layout" "$zlog"
  _check "zellij-run uses pinned config, data dir, server socket, and default layout" $?

  layout="/tmp/claude-contained-zellij-runtime/${session}.layout.kdl"
  grep -Fq 'args "echo" "hello world"' "$layout"
  _check "zellij-run writes the initial pane command into the layout" $?

  [[ -d "/tmp/zellij-$(id -u)/zellij-log" ]]
  _check "zellij-run pre-creates Zellij's temp log directory" $?
  [[ -d "${zhome}/.claude-contained/zellij/cache/org/Zellij-Contributors/Zellij" ]]
  _check "zellij-run pre-creates Zellij's project cache directory" $?

  ZELLIJ_STUB_LOG="$zlog" ZELLIJ_STUB_SESSION="$session" HOME="$zhome" PATH="${zbin}:$PATH" \
    "${repo_root}/image/zellij-attach.sh" "$session" >/dev/null 2>&1
  rc=$?
  [[ $rc -eq 1 ]]
  _check "zellij-attach refuses an exited resurrectable session" $?

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
