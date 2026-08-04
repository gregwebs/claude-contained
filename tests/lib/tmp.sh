# shellcheck shell=bash
#
# Shared temp-dir/-file helpers for the shell test suites.
#
# Why this exists: a bare `mktemp -d` (no template) on macOS creates under the
# Darwin per-user temp dir (_CS_DARWIN_USER_TEMP_DIR, e.g. /var/folders/…/T),
# ignoring $TMPDIR. When that dir is outside a sandbox write allowlist the call
# fails and prints nothing, so `dir="$(cd "$(mktemp -d)" && pwd -P)"` degrades to
# `cd "" && pwd -P` — which in bash succeeds and returns the *current* directory
# (the repo root). A later `rm -rf "$dir"` then deletes the repository. This
# module removes both halves of that footgun:
#
#   1. mk_tmpdir/mk_tmpfile/mk_tmpname template under ${TMPDIR:-/tmp} (an
#      approved, writable location) and hard-fail on an empty result, so the
#      "cd into nothing" step can never happen.
#   2. safe_rm_rf refuses to delete the repo root, an ancestor of it, $HOME, /,
#      . or .., or an empty path — the load-bearing guarantee, independent of
#      whether a caller remembered to check mk_* for failure.

# Resolve this library's location so the repo root is derived from the source
# tree layout (tests/lib/tmp.sh -> repo root two levels up) rather than $0/cwd,
# which are unreliable inside a sourced file. Overridable for self-tests so the
# guard can be exercised against a fake repo root without risking the real one.
_tmp_lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
_tmp_repo_root="${CLAUDE_CONTAINED_TEST_REPO_ROOT:-$(cd "${_tmp_lib_dir}/../.." && pwd -P)}"

_tmp_template() { printf '%s/claude-contained.XXXXXX' "${TMPDIR:-/tmp}"; }

# Create a temp directory under an approved location. Prints its path; returns 1
# without printing if creation fails or yields an empty/non-directory result.
mk_tmpdir() {
  local d
  d="$(mktemp -d "$(_tmp_template)" 2>/dev/null)" || d=""
  if [ -z "$d" ] || [ ! -d "$d" ]; then
    echo "mk_tmpdir: failed to create a temp directory (got [$d])" >&2
    return 1
  fi
  printf '%s\n' "$d"
}

# Like mk_tmpdir, but prints the symlink-resolved path. Callers that compare a
# temp path against the launcher's resolved mount output need this (macOS /tmp
# and $TMPDIR are symlinks into /private). The directory is validated before the
# `cd`, so the `cd ""` footgun cannot occur here.
mk_tmpdir_resolved() {
  local d
  d="$(mk_tmpdir)" || return 1
  (cd "$d" && pwd -P)
}

# Create a temp file under an approved location. Prints its path; returns 1 on
# failure or an empty/non-file result.
mk_tmpfile() {
  local f
  f="$(mktemp "$(_tmp_template)" 2>/dev/null)" || f=""
  if [ -z "$f" ] || [ ! -f "$f" ]; then
    echo "mk_tmpfile: failed to create a temp file (got [$f])" >&2
    return 1
  fi
  printf '%s\n' "$f"
}

# Reserve a temp path name (like `mktemp -u`) under an approved location without
# creating it. Prints the name; returns 1 on an empty result.
mk_tmpname() {
  local n
  n="$(mktemp -u "$(_tmp_template)" 2>/dev/null)" || n=""
  if [ -z "$n" ]; then
    echo "mk_tmpname: failed to reserve a temp name" >&2
    return 1
  fi
  printf '%s\n' "$n"
}

# Pure predicate (never deletes anything): returns 0 if $1 is safe to `rm -rf`,
# or 1 with a reason on stderr if it must be refused. Split out from safe_rm_rf
# so the self-tests can assert every refusal path with zero deletion risk.
_tmp_path_is_safe_to_delete() {
  local p="${1-}" rp
  if [ -z "$p" ]; then
    echo "safe_rm_rf: refusing empty path" >&2
    return 1
  fi
  case "$p" in
    /|.|..)
      echo "safe_rm_rf: refusing dangerous path: $p" >&2
      return 1
      ;;
  esac
  if [ -n "${HOME:-}" ] && [ "$p" = "$HOME" ]; then
    echo "safe_rm_rf: refusing \$HOME: $p" >&2
    return 1
  fi
  # Resolve the target when it exists so the repo-root comparison is robust to
  # symlinks and trailing slashes; fall back to the literal path otherwise.
  if [ -d "$p" ]; then
    rp="$(cd "$p" 2>/dev/null && pwd -P)" || rp="$p"
    [ -n "$rp" ] || rp="$p"
  else
    rp="$p"
  fi
  # Refuse the repo root itself ("$rp") and any ancestor of it ("$rp"/*). The
  # ancestor case also covers /, /Users, $HOME-above-the-repo, and similar.
  case "$_tmp_repo_root" in
    "$rp"|"$rp"/*)
      echo "safe_rm_rf: refusing repo-owning path: $p" >&2
      return 1
      ;;
  esac
  return 0
}

# `rm -rf` each argument that passes the guard; refuse (and skip) any that do
# not, so one bad path never blocks cleanup of the rest. Returns non-zero if any
# argument was refused.
safe_rm_rf() {
  local p status=0
  for p in "$@"; do
    if _tmp_path_is_safe_to_delete "$p"; then
      rm -rf -- "$p"
    else
      status=1
    fi
  done
  return "$status"
}
