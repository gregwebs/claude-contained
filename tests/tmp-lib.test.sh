#!/usr/bin/env bash
#
# Tests for tests/lib/tmp.sh — the temp-dir/-file helpers and the safe_rm_rf
# guard that keep a failed mktemp from letting rm -rf delete the repository.
#
# Every refusal case is exercised through the pure predicate
# _tmp_path_is_safe_to_delete, which never calls rm, so a broken guard cannot
# damage anything while these tests run.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tests/lib/tmp.sh
. "${here}/lib/tmp.sh"

fails=0
_check() { # _check "description" <rc-that-should-be-0>
  if [[ "$2" -eq 0 ]]; then
    echo "  PASS: $1"
  else
    echo "  FAIL: $1"
    fails=$((fails + 1))
  fi
}

echo "== tmp-lib =="

# --- mk_tmpdir / mk_tmpdir_resolved / mk_tmpfile land under an approved dir ---

d="$(mk_tmpdir)"
if [[ -n "$d" && -d "$d" && "$d" == "${TMPDIR:-/tmp}"/* ]]; then rc=0; else rc=1; fi
_check "mk_tmpdir creates a dir under \$TMPDIR" "$rc"
[[ -n "${d:-}" ]] && rmdir "$d" 2>/dev/null

dr="$(mk_tmpdir_resolved)"
if [[ -n "$dr" && -d "$dr" ]]; then rc=0; else rc=1; fi
_check "mk_tmpdir_resolved returns a real directory" "$rc"
[[ -n "${dr:-}" ]] && rmdir "$dr" 2>/dev/null

f="$(mk_tmpfile)"
if [[ -n "$f" && -f "$f" && "$f" == "${TMPDIR:-/tmp}"/* ]]; then rc=0; else rc=1; fi
_check "mk_tmpfile creates a file under \$TMPDIR" "$rc"
[[ -n "${f:-}" ]] && rm -f "$f" 2>/dev/null

n="$(mk_tmpname)"
if [[ -n "$n" && ! -e "$n" && "$n" == "${TMPDIR:-/tmp}"/* ]]; then rc=0; else rc=1; fi
_check "mk_tmpname reserves an unused name under \$TMPDIR" "$rc"

# --- mk_tmpdir hard-fails (empty output, non-zero rc) on a denied location ---

# Point the template at a path that cannot be created under; mktemp must fail.
denied="$(TMPDIR=/proc/nonexistent-claude-contained mk_tmpdir 2>/dev/null)"
if [[ -z "$denied" ]]; then rc=0; else rc=1; fi
_check "mk_tmpdir prints nothing when mktemp fails" "$rc"

TMPDIR=/proc/nonexistent-claude-contained mk_tmpdir >/dev/null 2>&1
mk_rc=$?
if [[ $mk_rc -ne 0 ]]; then rc=0; else rc=1; fi
_check "mk_tmpdir returns non-zero when mktemp fails" "$rc"

# --- safe_rm_rf guard refuses dangerous paths (predicate: never deletes) ---

refuses() { ! _tmp_path_is_safe_to_delete "$1" 2>/dev/null; }
allows()  {   _tmp_path_is_safe_to_delete "$1" 2>/dev/null; }

refuses ""
_check "refuses empty path" $?
refuses "/"
_check "refuses /" $?
refuses "."
_check "refuses ." $?
refuses ".."
_check "refuses .." $?
refuses "$HOME"
_check "refuses \$HOME" $?

# Refuse the real repo root and an ancestor of it.
real_repo="$(cd "${here}/.." && pwd -P)"
refuses "$real_repo"
_check "refuses the repo root" $?
refuses "$(dirname "$real_repo")"
_check "refuses an ancestor of the repo root" $?

# With the repo root overridden to a fake location, that fake root and its
# ancestors are refused while unrelated temp dirs remain deletable.
fake_root="$(mk_tmpdir_resolved)"
mkdir -p "${fake_root}/repo/sub"
(
  export CLAUDE_CONTAINED_TEST_REPO_ROOT="${fake_root}/repo"
  # shellcheck source=tests/lib/tmp.sh
  . "${here}/lib/tmp.sh"
  ! _tmp_path_is_safe_to_delete "${fake_root}/repo" 2>/dev/null
) ; _check "refuses an overridden (fake) repo root" $?
(
  export CLAUDE_CONTAINED_TEST_REPO_ROOT="${fake_root}/repo"
  # shellcheck source=tests/lib/tmp.sh
  . "${here}/lib/tmp.sh"
  ! _tmp_path_is_safe_to_delete "$fake_root" 2>/dev/null   # ancestor of fake repo
) ; _check "refuses an ancestor of the fake repo root" $?

# A sibling temp dir that is neither the repo root nor an ancestor is allowed.
sibling="$(mk_tmpdir_resolved)"
allows "$sibling"
_check "allows an unrelated temp directory" $?

# --- safe_rm_rf actually deletes an allowed path, refuses a dangerous one ---

victim="$(mk_tmpdir_resolved)"
touch "${victim}/file"
safe_rm_rf "$victim"
if [[ ! -e "$victim" ]]; then rc=0; else rc=1; fi
_check "safe_rm_rf removes an allowed temp directory" "$rc"

guarded="$(mk_tmpdir_resolved)"
# Mix a dangerous arg (repo root) with a safe one: dangerous refused, safe gone.
safe_rm_rf "$real_repo" "$guarded" 2>/dev/null
srf_rc=$?
if [[ $srf_rc -ne 0 && -d "$real_repo" && ! -e "$guarded" ]]; then rc=0; else rc=1; fi
_check "safe_rm_rf refuses repo root but still cleans the safe arg" "$rc"

# Cleanup helper leftovers.
safe_rm_rf "$fake_root" "$sibling"

if [[ "$fails" -gt 0 ]]; then
  echo
  echo "${fails} tmp-lib test(s) failed."
  exit 1
fi

echo
echo "All tmp-lib tests passed."
