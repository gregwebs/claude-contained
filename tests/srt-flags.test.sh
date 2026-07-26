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
# The scripts are sourced with CLAUDE_CONTAINED_LIB_ONLY=1, which defines the
# helper functions and returns before any container is launched, so no container
# runtime is required.
#
# Usage: tests/srt-flags.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"

# Emitted container args for a given set of launcher flags, using a stub runtime
# on PATH that just echoes its argv.
launcher_argv() { # launcher_argv <target> <flags...>
  local target="$1"; shift
  HOME="$home" PATH="${stub_dir}:$PATH" "${repo_root}/${target}" "$@" -N -s "$proj" 2>/dev/null
}

stub_dir="$(mktemp -d)"
proj="$(mktemp -d)"
home="$(mktemp -d)"
for rt in container docker; do
  printf '#!/bin/bash\nprintf "%%s\\n" "$@"\n' > "${stub_dir}/${rt}"
  chmod +x "${stub_dir}/${rt}"
done

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

  # 6-9. Attach builders. `container exec` skips the entrypoint, so the wrapper
  #      has to be prepended here or the attached process runs unsandboxed.
  (
    export CLAUDE_CONTAINED_LIB_ONLY=1
    # shellcheck disable=SC1090
    source "${repo_root}/${target}" . >/dev/null 2>&1

    tool="claude"; yolo_mode=0; srt_disable=0
    build_attach_cmd
    [[ "${attach_cmd[0]}" == "srt-run" && "${attach_cmd[1]}" == "/opt/claude/claude" ]]
  )
  _check "attach tool command is prefixed with srt-run" $?

  (
    export CLAUDE_CONTAINED_LIB_ONLY=1
    # shellcheck disable=SC1090
    source "${repo_root}/${target}" . >/dev/null 2>&1

    tool="claude"; yolo_mode=0; srt_disable=1
    build_attach_cmd
    [[ "${attach_cmd[0]}" == "/opt/claude/claude" ]]
  )
  _check "attach tool command drops srt-run under --no-sandbox" $?

  (
    export CLAUDE_CONTAINED_LIB_ONLY=1
    # shellcheck disable=SC1090
    source "${repo_root}/${target}" . >/dev/null 2>&1

    srt_disable=0
    build_attach_shell_cmd
    [[ "${attach_cmd[0]}" == "srt-run" && "${attach_cmd[1]}" == "bash" ]]
  )
  _check "attach debug shell is prefixed with srt-run" $?

  (
    export CLAUDE_CONTAINED_LIB_ONLY=1
    # shellcheck disable=SC1090
    source "${repo_root}/${target}" . >/dev/null 2>&1

    srt_disable=1
    build_attach_shell_cmd
    [[ "${attach_cmd[0]}" == "bash" && ${#attach_cmd[@]} -eq 1 ]]
  )
  _check "attach debug shell drops srt-run under --no-sandbox" $?

  # 10. Yolo flags must still land after the tool, not before the wrapper.
  (
    export CLAUDE_CONTAINED_LIB_ONLY=1
    # shellcheck disable=SC1090
    source "${repo_root}/${target}" . >/dev/null 2>&1

    tool="claude"; yolo_mode=1; srt_disable=0
    build_attach_cmd
    [[ "${attach_cmd[*]}" == "srt-run /opt/claude/claude --dangerously-skip-permissions" ]]
  )
  _check "yolo flag stays after the tool, behind the wrapper" $?

  return "$fails"
}

total=0
for target in claude-contained claude-docked; do
  echo "== ${target} =="
  suite "$target"
  total=$((total + $?))
done

rm -rf "$stub_dir" "$proj" "$home"

if [[ "$total" -gt 0 ]]; then
  echo
  echo "${total} srt-flag test(s) failed."
  exit 1
fi

echo
echo "All srt-flag tests passed."
