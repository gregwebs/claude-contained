#!/usr/bin/env bash
#
# Regression test for the in-container Claude native-link script
# (image/claude-native-link.sh). This image script stays shell -- it runs
# inside the image, where bash is a given -- and is covered here until slice 4
# (#33) moves the remaining image-script tests to Go.
#
# The launcher-side srt placeholder-cleanup cases that used to live here now run
# in Go: cmd/claude-contained TestPlaceholderCreatedDuringRunIsSweptAfterExit
# and golden case 50-placeholder-cleanup-mounted-roots, plus the internal/host
# placeholder tests.
#
# Usage: tests/startup-diagnostics.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"
# shellcheck source=tests/lib/tmp.sh
. "${here}/lib/tmp.sh"
unset CLAUDE_CONTAINED_LOG_LEVEL

fails=0
_check() { # _check "description" <rc-that-should-be-0>
  if [[ "$2" -eq 0 ]]; then
    echo "  PASS: $1"
  else
    echo "  FAIL: $1"
    fails=$((fails + 1))
  fi
}

echo "== Claude native link =="
link_home="$(mk_tmpdir)"
fake_bin_dir="$(mk_tmpdir)"
fake_claude="${fake_bin_dir}/claude"
cat > "$fake_claude" <<'EOF'
#!/usr/bin/env bash
printf '1.2.3 (Claude Code)\n'
EOF
chmod +x "$fake_claude"

CLAUDE_CONTAINED_CLAUDE_BIN="$fake_claude" "${repo_root}/image/claude-native-link.sh" "$link_home"
bin_target="$(readlink "${link_home}/.local/bin/claude")"
version_target="$(readlink "${link_home}/.local/share/claude/versions/1.2.3")"
if [[ "$bin_target" == "${link_home}/.local/share/claude/versions/1.2.3" && "$version_target" == "$fake_claude" ]]; then check_rc=0; else check_rc=1; fi
_check "creates a native-shaped Claude versions symlink" "$check_rc"

preserve_home="$(mk_tmpdir)"
mkdir -p "${preserve_home}/.local/bin"
existing_target="${fake_bin_dir}/existing-claude"
: > "$existing_target"
ln -s "$existing_target" "${preserve_home}/.local/bin/claude"
CLAUDE_CONTAINED_CLAUDE_BIN="$fake_claude" "${repo_root}/image/claude-native-link.sh" "$preserve_home"
[[ "$(readlink "${preserve_home}/.local/bin/claude")" == "$existing_target" ]]
_check "preserves an existing Claude launcher link" $?

safe_rm_rf "$link_home" "$preserve_home" "$fake_bin_dir"

if [[ "$fails" -gt 0 ]]; then
  echo
  echo "${fails} startup diagnostic test(s) failed."
  exit 1
fi

echo
echo "All startup diagnostic tests passed."
