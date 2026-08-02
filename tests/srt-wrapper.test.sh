#!/usr/bin/env bash
#
# Tests for preserving child command flags when wrapping with srt.
# shellcheck disable=SC2016 # Assertions intentionally match literal shell syntax.
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

echo "== srt-run =="

grep -Fq 'exec /usr/local/bin/srt --settings "${SRT_SETTINGS_PATH:-/run/srt-settings.json}" -- "$@"' "${repo_root}/image/srt-run.sh"
_check "srt-run uses an absolute sandbox path and separates child flags" $?

echo "== entrypoint =="

grep -Fq 'set -- /usr/local/bin/srt --settings "${SRT_SETTINGS_PATH:-/run/srt-settings.json}" -- "$@"' "${repo_root}/image/entrypoint.sh"
_check "entrypoint uses an absolute sandbox path and separates child flags" $?

if [[ "$fails" -gt 0 ]]; then
  echo
  echo "${fails} srt wrapper test(s) failed."
  exit 1
fi

echo
echo "All srt wrapper tests passed."
