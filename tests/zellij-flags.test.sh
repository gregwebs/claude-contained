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
# The launcher cases in suite() run against every target -- bin/claude-contained
# and the bin/claude-contained-docked symlink alike, selected via
# CLAUDE_CONTAINED_TEST_TARGETS. The in-container Zellij scripts
# (zellij-pane-command.sh, zellij-run.sh, zellij-attach.sh) are covered
# separately, target-independent, in tests/zellij-image-scripts.test.sh.
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

target_is_docker() { # target_is_docker <target>
  [[ "$1" == *dock* ]]
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
  file_has "$out" "missing Zellij support" && file_has "$out" "claude-contained --rebuild=full"
  _check "--zellij command includes stale-image rebuild hint" $?
  if target_is_docker "$target"; then
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

  ZELLIJ_STUB_MODE=normal launcher_run "$target" "$out" "$err" --zellij -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "zellij-run" && file_missing "$err" "already running"
  _check "plain --zellij ignores non-Zellij containers" $?

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

  # --new-session forces past "another session is live", never past "this
  # session is live": the target check (claude-contained:1457) runs first and
  # does not consult the flag. Refusing here is what stops a second Zellij
  # server from being started for a session that already has one.
  ZELLIJ_STUB_MODE=one launcher_run "$target" "$out" "$err" --zellij --session=alpha --new-session -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 1 ]] && file_has "$err" "already live" && file_missing "$out" "zellij-run"
  _check "--new-session does not override a live target session" $?

  ZELLIJ_STUB_MODE=none launcher_run "$target" "$out" "$err" --zellij --attach --session alpha
  rc=$?
  [[ $rc -eq 1 ]] && file_has "$err" "No live Zellij session named alpha" && file_missing "$out" "run"
  _check "--zellij --attach --session NAME never falls back to container creation" $?

  ZELLIJ_STUB_MODE=normal launcher_run "$target" "$out" "$err" --zellij --attach
  rc=$?
  [[ $rc -eq 0 ]] && file_has "$out" "No live Zellij sessions" && file_missing "$out" "/usr/local/bin/zellij-attach"
  _check "--zellij --attach ignores non-Zellij containers" $?

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

total=0
read -ra targets <<< "${CLAUDE_CONTAINED_TEST_TARGETS:-bin/claude-contained bin/claude-contained-docked}"
for target in "${targets[@]}"; do
  echo "== ${target} =="
  suite "$target"
  total=$((total + $?))
done

rm -rf "$stub_dir" "$proj" "$home"

if [[ "$total" -gt 0 ]]; then
  echo
  echo "${total} Zellij test(s) failed."
  exit 1
fi

echo
echo "All Zellij tests passed."
