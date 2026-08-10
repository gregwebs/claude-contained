#!/usr/bin/env bash
# Verifies that an external directory-symlink target remains reachable through
# the shared skills destination in a real container runtime.
set -euo pipefail

repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
launcher=${CLAUDE_CONTAINED_LAUNCHER:-"$repo_dir/bin/claude-contained"}

if [[ ! -x $launcher ]]; then
  echo "error: launcher is not executable: $launcher" >&2
  echo "       Run 'make build' or set CLAUDE_CONTAINED_LAUNCHER." >&2
  exit 2
fi

fixture_dir=$(mktemp -d /private/tmp/claude-contained-shared-skills.XXXXXX)
trap 'rm -rf -- "$fixture_dir"' EXIT

shared_dir=$fixture_dir/shared-skills
target_dir=$fixture_dir/external-root/.agents/skills/implement
mkdir -p "$shared_dir/.system" "$target_dir"
printf '%s\n' 'verified shared skill' > "$target_dir/SKILL.md"
ln -s "$target_dir" "$shared_dir/implement"

"$launcher" --no-sandbox -C "$repo_dir" --share-skills "$shared_dir" \
  -e "LIVE_SHARED_SKILLS_TARGET=$target_dir" -- bash -ceu '
    test -f "$HOME/.agents/skills/implement/SKILL.md"
    test -f "$LIVE_SHARED_SKILLS_TARGET/SKILL.md"
    test "$(cat "$HOME/.agents/skills/implement/SKILL.md")" = "verified shared skill"
    if touch "$LIVE_SHARED_SKILLS_TARGET/should-not-write"; then
      echo "shared skills target was unexpectedly writable" >&2
      exit 1
    fi
  '

echo "shared-skills live verification passed"
