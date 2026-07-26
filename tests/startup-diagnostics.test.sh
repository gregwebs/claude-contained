#!/usr/bin/env bash
#
# Regression tests for startup diagnostics that should not appear in contained
# Claude sessions.
#
# Usage: tests/startup-diagnostics.test.sh
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

setup_runtime_stubs() {
  stub_dir="$(mktemp -d)"
  cat > "${stub_dir}/container" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail

if [[ "${1:-}" == "system" ]]; then
  exit 0
fi
if [[ "${1:-}" == "list" ]]; then
  exit 0
fi
if [[ "${1:-}" == "run" && -n "${SRT_STUB_PLACEHOLDER_ROOTS:-}" ]]; then
  IFS=':' read -ra roots <<< "$SRT_STUB_PLACEHOLDER_ROOTS"
  for root in "${roots[@]}"; do
    [[ -n "$root" ]] && : > "${root}/.mcp.json"
  done
fi
printf '%s\n' "$@"
EOF

  cat > "${stub_dir}/docker" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail

if [[ "${1:-}" == "info" || "${1:-}" == "ps" ]]; then
  exit 0
fi
if [[ "${1:-}" == "run" && -n "${SRT_STUB_PLACEHOLDER_ROOTS:-}" ]]; then
  IFS=':' read -ra roots <<< "$SRT_STUB_PLACEHOLDER_ROOTS"
  for root in "${roots[@]}"; do
    [[ -n "$root" ]] && : > "${root}/.mcp.json"
  done
fi
printf '%s\n' "$@"
EOF
  chmod +x "${stub_dir}/container" "${stub_dir}/docker"
}

launcher_run() { # launcher_run <target> <home> <project> [extra env...]
  local target="$1" home="$2" project="$3"
  shift 3

  env "$@" HOME="$home" PATH="${stub_dir}:$PATH" \
    "${repo_root}/${target}" -N -s "$project" >/dev/null 2>&1
}

echo "== srt placeholder cleanup =="
for target in claude-contained claude-docked; do
  setup_runtime_stubs
  home="$(mktemp -d)"
  project="$(mktemp -d)"

  : > "${project}/.mcp.json"
  : > "${project}/.bashrc"
  printf '{}\n' > "${project}/.ripgreprc"
  launcher_run "$target" "$home" "$project"
  [[ ! -e "${project}/.mcp.json" && ! -e "${project}/.bashrc" && -s "${project}/.ripgreprc" ]]
  _check "${target}: removes pre-existing zero-byte srt placeholders only" $?

  SRT_STUB_PLACEHOLDER_ROOTS="$project" launcher_run "$target" "$home" "$project"
  [[ ! -e "${project}/.mcp.json" ]]
  _check "${target}: removes zero-byte srt placeholders created during run" $?

  rm -rf "$stub_dir" "$home" "$project"
done

echo "== Claude native link =="
link_home="$(mktemp -d)"
fake_bin_dir="$(mktemp -d)"
fake_claude="${fake_bin_dir}/claude"
cat > "$fake_claude" <<'EOF'
#!/usr/bin/env bash
printf '1.2.3 (Claude Code)\n'
EOF
chmod +x "$fake_claude"

CLAUDE_CONTAINED_CLAUDE_BIN="$fake_claude" "${repo_root}/image/claude-native-link.sh" "$link_home"
bin_target="$(readlink "${link_home}/.local/bin/claude")"
version_target="$(readlink "${link_home}/.local/share/claude/versions/1.2.3")"
[[ "$bin_target" == "${link_home}/.local/share/claude/versions/1.2.3" && "$version_target" == "$fake_claude" ]]
_check "creates a native-shaped Claude versions symlink" $?

preserve_home="$(mktemp -d)"
mkdir -p "${preserve_home}/.local/bin"
existing_target="${fake_bin_dir}/existing-claude"
: > "$existing_target"
ln -s "$existing_target" "${preserve_home}/.local/bin/claude"
CLAUDE_CONTAINED_CLAUDE_BIN="$fake_claude" "${repo_root}/image/claude-native-link.sh" "$preserve_home"
[[ "$(readlink "${preserve_home}/.local/bin/claude")" == "$existing_target" ]]
_check "preserves an existing Claude launcher link" $?

rm -rf "$link_home" "$preserve_home" "$fake_bin_dir"

if [[ "$fails" -gt 0 ]]; then
  echo
  echo "${fails} startup diagnostic test(s) failed."
  exit 1
fi

echo
echo "All startup diagnostic tests passed."
