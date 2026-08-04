#!/usr/bin/env bash
#
# Tests for the srt sandbox flags (--no-sandbox, --allow-host) and the attach
# command builders.
#
# The security boundary is applied by image/entrypoint.sh, which wraps the tool
# process in srt. These tests cover the launcher side of that contract: the env
# vars the entrypoint reads must be emitted correctly, and the attach paths --
# which bypass the entrypoint, since `container exec` does not run ENTRYPOINT --
# must route through the srt-run wrapper instead.
#
# The attach-argv checks run black-box against every target: they stub the
# runtime to report a running container and assert on the emitted exec argv.
#
# Usage: tests/srt-flags.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"
# shellcheck source=tests/lib/tmp.sh
. "${here}/lib/tmp.sh"
unset CLAUDE_CONTAINED_LOG_LEVEL

# Emitted container args for a given set of launcher flags, using a stub runtime
# on PATH that just echoes its argv.
launcher_argv() { # launcher_argv <target> <flags...>
  local target="$1"; shift
  HOME="$home" PATH="${stub_dir}:$PATH" "${repo_root}/${target}" "$@" -N -s -C "$proj" 2>/dev/null
}

stub_dir="$(mk_tmpdir)"
proj="$(mk_tmpdir)"
home="$(mk_tmpdir)"

# Stub runtime: reports the (optional) SRT_TEST_LIST fixture for `list`/`ps`
# so the attach paths have something to reconnect to, and otherwise echoes its
# argv one element per line, as before.
for rt in container docker; do
  cat > "${stub_dir}/${rt}" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail
case "${1:-}" in
  system|info) exit 0 ;;
  list|ps) [[ -n "${SRT_TEST_LIST:-}" ]] && printf '%s\n' "${SRT_TEST_LIST}"; exit 0 ;;
  inspect) exit 0 ;;
esac
printf '%s\n' "$@"
STUB
  chmod +x "${stub_dir}/${rt}"
done

# Emitted container argv, flattened to one line, for the black-box attach
# checks below -- these run against every target, not just the bash launchers.
attach_argv() { # attach_argv <target> <flags...>
  local target="$1"; shift
  SRT_TEST_LIST="aic-live" HOME="$home" PATH="${stub_dir}:$PATH" \
    "${repo_root}/${target}" "$@" </dev/null 2>/dev/null | tr '\n' ' '
}

suite() {
  set +e
  local target="$1"
  local fails=0
  local out

  _check() { # _check "description" <rc-that-should-be-0>
    if [[ "$2" -eq 0 ]]; then
      echo "  PASS: $1"
    else
      echo "  FAIL: $1"
      fails=$((fails + 1))
    fi
  }

  # 1. Sandbox is on by default: the launcher stays silent and the entrypoint
  #    decides. Emitting nothing keeps behavior unchanged for existing users.
  out="$(launcher_argv "$target")"
  ! grep -q '^SRT_' <<<"$out"
  _check "default run emits no SRT_ env vars" $?
  grep -qx '/usr/local/bin/shell-run' <<<"$out"
  _check "debug shell run uses shell-run helper" $?

  # 2. --no-sandbox is the documented escape hatch.
  out="$(launcher_argv "$target" --no-sandbox)"
  grep -qx 'SRT_DISABLE=1' <<<"$out"
  _check "--no-sandbox emits SRT_DISABLE=1" $?

  # 3. --allow-host is repeatable and joined with commas for the entrypoint.
  out="$(launcher_argv "$target" --allow-host a.example --allow-host b.example)"
  grep -qx 'SRT_ALLOW_HOSTS=a.example,b.example' <<<"$out"
  _check "--allow-host repeats join comma-separated, in order" $?

  # 4. The --flag=value form parses too, matching --dns.
  out="$(launcher_argv "$target" --allow-host=eq.example)"
  grep -qx 'SRT_ALLOW_HOSTS=eq.example' <<<"$out"
  _check "--allow-host=VALUE equals form parses" $?

  # 5. Sandbox flags must not disturb the DNS flags they sit beside.
  out="$(launcher_argv "$target" --dns 1.1.1.1 --allow-host c.example)"
  grep -qx '1.1.1.1' <<<"$out" && grep -qx 'SRT_ALLOW_HOSTS=c.example' <<<"$out"
  _check "--dns and --allow-host coexist" $?

  # 6-11. Black-box attach checks, run against every target (bash or Go): the
  # attached process is exec'd straight into the runtime, bypassing the
  # entrypoint, so the sandbox wrapper and yolo placement have to come from
  # the launcher's attach path itself.
  out="$(attach_argv "$target" -a live)"
  grep -Fq 'aic-live /usr/local/bin/tool-env /usr/local/bin/srt-run /opt/claude/claude' <<<"$out"
  _check "attach by name runs the tool behind the sandbox wrapper" $?

  out="$(attach_argv "$target" -a live --no-sandbox)"
  grep -Fq 'aic-live /usr/local/bin/tool-env /opt/claude/claude' <<<"$out" && ! grep -Fq 'srt-run' <<<"$out"
  _check "--no-sandbox drops the wrapper on attach" $?

  out="$(attach_argv "$target" -a live -s)"
  grep -Fq 'aic-live /usr/local/bin/tool-env /usr/local/bin/srt-run /usr/local/bin/shell-run' <<<"$out"
  _check "attach debug shell runs behind the sandbox wrapper" $?

  out="$(attach_argv "$target" -a live -s --no-sandbox)"
  grep -Fq 'aic-live /usr/local/bin/tool-env /usr/local/bin/shell-run' <<<"$out" && ! grep -Fq 'srt-run' <<<"$out"
  _check "--no-sandbox drops the wrapper on attach debug shell" $?

  out="$(attach_argv "$target" -a live -y)"
  grep -Fq '/usr/local/bin/tool-env /usr/local/bin/srt-run /opt/claude/claude --dangerously-skip-permissions' <<<"$out"
  _check "attach yolo flag lands after the tool, behind the wrapper" $?

  out="$(attach_argv "$target" -a live -y -t codex)"
  grep -Fq '/usr/local/bin/tool-env /usr/local/bin/srt-run codex --yolo' <<<"$out"
  _check "attach yolo flag for a non-claude tool" $?

  # Attach builders (`build_attach_cmd`/`build_attach_shell_cmd`, which prefix
  # srt-run because `container exec`/`docker exec` skip the entrypoint) used to
  # be additionally unit-tested here by sourcing a bash launcher's shell
  # functions directly with CLAUDE_CONTAINED_LIB_ONLY=1. A compiled binary
  # cannot be sourced, so that block is gone now that both launchers are Go;
  # the Go equivalents are ticket 07's own unit tests
  # (internal/attach/attach_test.go), and the black-box attach-argv checks
  # above already exercise the same four shapes end to end.

  return "$fails"
}

total=0
read -ra targets <<< "${CLAUDE_CONTAINED_TEST_TARGETS:-bin/claude-contained bin/claude-contained-docked}"
for target in "${targets[@]}"; do
  echo "== ${target} =="
  suite "$target"
  total=$((total + $?))
done

safe_rm_rf "$stub_dir" "$proj" "$home"

if [[ "$total" -gt 0 ]]; then
  echo
  echo "${total} srt-flag test(s) failed."
  exit 1
fi

echo
echo "All srt-flag tests passed."
