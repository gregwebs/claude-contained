#!/usr/bin/env bash
#
# Tests for --share-skills mount behavior. Shared skills are mounted read-only,
# and symlink targets are mounted read-only at path-parity locations so absolute
# symlinks inside shared skill trees keep resolving inside the container.
#
# Usage: tests/shared-skills-mounts.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"

stub_dir="$(mktemp -d)"
for rt in container docker; do
  printf '#!/usr/bin/env bash\n[[ "${1:-}" == system || "${1:-}" == info || "${1:-}" == list || "${1:-}" == ps || "${1:-}" == inspect ]] && exit 0\nprintf "%%s\\n" "$@"\n' \
    > "${stub_dir}/${rt}"
  chmod +x "${stub_dir}/${rt}"
done

out="$(mktemp)"
err="$(mktemp)"

run_launcher() { # run_launcher <target> <home> <proj> <share> [flags...]
  local target="$1" home="$2" proj="$3" share="$4"
  shift 4
  HOME="$home" PATH="${stub_dir}:$PATH" "${repo_root}/${target}" \
    -N -s -C "$proj" "$@" --share-skills "$share" >"$out" 2>"$err" </dev/null
}

line_has() { grep -qxF -- "$2" "$1"; }
line_missing() { ! line_has "$1" "$2"; }
line_count() { grep -xcF -- "$2" "$1"; }
file_has() { grep -qF -- "$2" "$1"; }

suite() {
  set +e
  local target="$1"
  local fails=0
  local home proj share targets dir_target file_parent nested_target system_target conflict_dir
  local rc dst mount_line count

  _check() {
    if [[ "$2" -eq 0 ]]; then
      echo "  PASS: $1"
    else
      echo "  FAIL: $1"
      fails=$((fails + 1))
    fi
  }

  home="$(cd "$(mktemp -d)" && pwd -P)"
  proj="$(cd "$(mktemp -d)" && pwd -P)"
  share="$(cd "$(mktemp -d)" && pwd -P)"
  targets="${home}/skill targets"
  dir_target="${targets}/dir target"
  file_parent="${targets}/file parent"
  nested_target="${targets}/nested target"
  system_target="${targets}/system target"
  mkdir -p "$dir_target" "$file_parent" "$nested_target" "$system_target" "${home}/.codex/skills/.system"
  printf 'name: linked-file\n' > "${file_parent}/skill file.md"
  ln -s "$dir_target" "${share}/dir-link"
  ln -s "$dir_target" "${share}/dir-link-duplicate"
  ln -s "${file_parent}/skill file.md" "${share}/file-link.md"
  ln -s "$nested_target" "${dir_target}/nested-link"
  ln -s "$system_target" "${home}/.codex/skills/.system/system-link"

  run_launcher "$target" "$home" "$proj" "$share"
  rc=$?
  [[ $rc -eq 0 ]]
  _check "--share-skills run succeeds with symlinked skill entries" $?

  for dst in \
    "${home}/.claude/skills" \
    "${home}/.codex/skills" \
    "${home}/.agents/skills" \
    "${home}/.copilot/skills" \
    "${home}/.gemini/skills" \
    "${home}/.vibe/skills"; do
    line_has "$out" "type=bind,src=${share},dst=${dst},readonly"
    _check "shared skills mount is read-only at ${dst}" $?
  done
  line_has "$out" "type=bind,src=${share},dst=${share},readonly"
  _check "shared skills source is also mounted read-only at its path" $?

  line_missing "$out" "type=bind,src=${home}/.claude/skills,dst=${home}/.claude/skills"
  _check "--share-skills replaces the default host Claude skills mount" $?

  for mount_line in \
    "type=bind,src=${dir_target},dst=${dir_target},readonly" \
    "type=bind,src=${file_parent},dst=${file_parent},readonly" \
    "type=bind,src=${nested_target},dst=${nested_target},readonly" \
    "type=bind,src=${home}/.codex/skills/.system,dst=${home}/.codex/skills/.system,readonly" \
    "type=bind,src=${system_target},dst=${system_target},readonly"; do
    line_has "$out" "$mount_line"
    _check "symlink-related mount emitted: ${mount_line}" $?
  done

  count="$(line_count "$out" "type=bind,src=${dir_target},dst=${dir_target},readonly")"
  [[ "$count" -eq 1 ]]
  _check "duplicate symlink targets emit one read-only mount" $?

  run_launcher "$target" "$home" "$proj" "$share" -m "${share}:rw"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "conflicts with writable mount" && file_has "$err" "$share"
  _check "writable exact extra mount conflicts with shared skills source" $?

  run_launcher "$target" "$home" "$proj" "$share" -m "${share}:ro"
  rc=$?
  [[ $rc -eq 0 ]]
  _check "read-only exact extra mount can satisfy shared skills source" $?

  conflict_dir="$(cd "$(mktemp -d)" && pwd -P)"
  rm -rf "$share"
  share="$(cd "$(mktemp -d)" && pwd -P)"
  ln -s "$conflict_dir" "${share}/conflict-link"

  run_launcher "$target" "$home" "$proj" "$share" -m "${conflict_dir}:rw"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "conflicts with writable mount" && file_has "$err" "$conflict_dir"
  _check "writable exact extra mount conflicts with symlink target" $?

  run_launcher "$target" "$home" "$proj" "$share" -m "${conflict_dir}:ro"
  rc=$?
  [[ $rc -eq 0 ]]
  _check "read-only exact extra mount can satisfy symlink target" $?

  rm -rf "$share"
  share="$(cd "$(mktemp -d)" && pwd -P)"
  ln -s "${targets}/missing" "${share}/broken-link"
  run_launcher "$target" "$home" "$proj" "$share"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "symlink target does not exist" && file_has "$err" "${share}/broken-link"
  _check "broken shared-skill symlink fails with a clear error" $?

  rm -rf "$home" "$proj" "$share" "$conflict_dir"
  return "$fails"
}

total=0
for target in claude-contained claude-docked; do
  echo "== ${target} =="
  suite "$target"
  total=$((total + $?))
done

rm -rf "$stub_dir"
rm -f "$out" "$err"

if [[ "$total" -gt 0 ]]; then
  echo
  echo "${total} shared-skills mount test(s) failed."
  exit 1
fi

echo
echo "All shared-skills mount tests passed."
