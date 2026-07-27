#!/usr/bin/env bash
#
# Tests for Claude profile mounting. The launchers should default to a
# contained Claude profile while sharing only common resource directories from
# the host profile, with an explicit compatibility switch for the old direct
# ~/.claude mount.
#
# Usage: tests/claude-profile-mounts.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"

stub_dir="$(mktemp -d)"
proj="$(mktemp -d)"
for rt in container docker; do
  printf '#!/usr/bin/env bash\nprintf "%%s\\n" "$@"\n' > "${stub_dir}/${rt}"
  chmod +x "${stub_dir}/${rt}"
done

launcher_argv() { # launcher_argv <target> <home> <flags...>
  local target="$1"
  local home="$2"
  shift 2
  env -u CLAUDE_CONTAINED_SHARE_HOST_CLAUDE \
    HOME="$home" PATH="${stub_dir}:$PATH" \
    "${repo_root}/${target}" "$@" -N -s -C "$proj" 2>/dev/null
}

launcher_argv_with_shared_host_env() { # launcher_argv_with_shared_host_env <target> <home>
  local target="$1"
  local home="$2"
  CLAUDE_CONTAINED_SHARE_HOST_CLAUDE=1 \
    HOME="$home" PATH="${stub_dir}:$PATH" \
    "${repo_root}/${target}" -N -s -C "$proj" 2>/dev/null
}

line_has() { # line_has <haystack> <exact-line>
  grep -Fqx -- "$2" <<<"$1"
}

line_missing() { # line_missing <haystack> <exact-line>
  ! line_has "$1" "$2"
}

suite() {
  set +e
  local target="$1"
  local fails=0
  local home out resource

  _check() { # _check "description" <rc-that-should-be-0>
    if [[ "$2" -eq 0 ]]; then
      echo "  PASS: $1"
    else
      echo "  FAIL: $1"
      fails=$((fails + 1))
    fi
  }

  home="$(mktemp -d)"
  mkdir -p "${home}/.claude"
  printf '{}\n' > "${home}/.claude/settings.json"

  out="$(launcher_argv "$target" "$home")"
  line_has "$out" "type=bind,src=${home}/.claude-contained/claude,dst=${home}/.claude"
  _check "default maps contained Claude profile to container ~/.claude" $?
  line_missing "$out" "type=bind,src=${home}/.claude,dst=${home}/.claude"
  _check "default does not mount host ~/.claude wholesale" $?
  line_has "$out" "type=bind,src=${home}/.claude-contained,dst=${home}/.claude-contained"
  _check "default still mounts ~/.claude-contained for account state" $?

  for resource in skills agents commands plugins; do
    line_has "$out" "type=bind,src=${home}/.claude/${resource},dst=${home}/.claude/${resource}"
    _check "default shares host Claude ${resource}" $?
  done

  [[ ! -e "${home}/.claude-contained/claude/settings.json" ]]
  _check "default does not copy host Claude settings.json into contained profile" $?
  ! grep -Fq "${home}/.claude/settings.json" <<<"$out"
  _check "default does not mount host Claude settings.json directly" $?

  out="$(launcher_argv "$target" "$home" --share-host-claude)"
  line_has "$out" "type=bind,src=${home}/.claude,dst=${home}/.claude"
  _check "--share-host-claude restores direct host ~/.claude mount" $?
  line_missing "$out" "type=bind,src=${home}/.claude-contained/claude,dst=${home}/.claude"
  _check "--share-host-claude skips contained Claude profile mount" $?
  line_missing "$out" "type=bind,src=${home}/.claude/skills,dst=${home}/.claude/skills"
  _check "--share-host-claude skips nested resource mounts" $?

  out="$(launcher_argv_with_shared_host_env "$target" "$home")"
  line_has "$out" "type=bind,src=${home}/.claude,dst=${home}/.claude"
  _check "CLAUDE_CONTAINED_SHARE_HOST_CLAUDE=1 restores direct host ~/.claude mount" $?

  rm -rf "$home"
  return "$fails"
}

total=0
read -ra targets <<< "${CLAUDE_CONTAINED_TEST_TARGETS:-claude-contained claude-docked}"
for target in "${targets[@]}"; do
  echo "== ${target} =="
  suite "$target"
  total=$((total + $?))
done

rm -rf "$stub_dir" "$proj"

if [[ "$total" -gt 0 ]]; then
  echo
  echo "${total} Claude profile mount test(s) failed."
  exit 1
fi

echo
echo "All Claude profile mount tests passed."
