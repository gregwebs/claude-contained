#!/usr/bin/env bash
# Tests the in-container tool environment resolver used by run and attach paths.
# shellcheck disable=SC2016 # Fixtures exercise literal shell-looking text.
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
resolver="${repo_root}/image/tool-env.sh"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/claude-contained-tool-env.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT

passes=0 failures=0
check() {
  local name="$1" status="$2"
  if [[ "$status" -eq 0 ]]; then
    passes=$((passes + 1))
  else
    printf 'FAIL: %s\n' "$name" >&2
    failures=$((failures + 1))
  fi
}

mkdir -p "$tmp/fragments"
printf 'CACHE=${HOME}/.cache/tool\nPATH=${HOME}/bin:$PATH\nLITERAL=$(touch /tmp/must-not-run)\n' >"$tmp/fragments/10-tool"
printf 'PATH=/opt/tool/bin:$PATH\n' >"$tmp/fragments/20-path"

out="$(HOME=/host/home PATH=/bin:/usr/bin "$resolver" --directory "$tmp/fragments" \
  sh -c 'printf "%s\n%s\n%s\n" "$CACHE" "$PATH" "$LITERAL"' 2>"$tmp/err")"
status=$?
[[ $status -eq 0 ]] && grep -Fqx '/host/home/.cache/tool' <<<"$out" \
  && grep -Fq '/opt/tool/bin:/host/home/bin:/host/home/.local/bin:/opt/claude:' <<<"$out" \
  && grep -Fqx '$(touch /tmp/must-not-run)' <<<"$out" \
  && [[ ! -e /tmp/must-not-run ]]
check "fragments expand only HOME and PATH and are never evaluated" $?

for key in STAY_ROOT SRT_SETTINGS_PATH HOST_UID HOME LD_PRELOAD LD_AUDIT BASH_ENV ENV NODE_OPTIONS CLAUDE_CONTAINED_ZELLIJ; do
  printf '%s=bad\n' "$key" >"$tmp/fragments/10-tool"
  HOME=/host/home PATH=/bin:/usr/bin "$resolver" --directory "$tmp/fragments" true >"$tmp/out" 2>"$tmp/err"
  status=$?
  [[ $status -ne 0 ]] && grep -Fq "$key is reserved" "$tmp/err"
  check "fragment refuses $key" $?
done

printf 'JAVA_HOME=/opt/custom-java\nPATH=$JAVA_HOME/bin:$PATH\n' >"$tmp/fragments/10-tool"
rm -f "$tmp/fragments/20-path"
out="$(HOME=/host/home PATH=/bin:/usr/bin "$resolver" --directory "$tmp/fragments" \
  sh -c 'printf "%s\n%s\n" "$JAVA_HOME" "$PATH"')"
grep -Fqx '/opt/custom-java' <<<"$out" && grep -Fq '$JAVA_HOME/bin:/host/home/.local/bin:/opt/claude:' <<<"$out"
check "JAVA_HOME is allowed while non-HOME/PATH references stay literal" $?

printf 'CHOICE=from-layer\n' >"$tmp/fragments/10-tool"
out="$(CHOICE=from-user HOME=/host/home PATH=/bin:/usr/bin "$resolver" --directory "$tmp/fragments" \
  sh -c 'printf "%s" "$CHOICE"')"
if [[ "$out" == from-user ]]; then status=0; else status=1; fi
check "an explicit process environment value wins over a fragment" "$status"

printf 'PREFIX=$HOMELESS:$PATHOLOGY\nSENTINEL=__CLAUDE_CONTAINED_HOME_REF__\n' >"$tmp/fragments/10-tool"
out="$(HOME=/host/home PATH=/bin:/usr/bin "$resolver" --directory "$tmp/fragments" \
  sh -c 'printf "%s\n%s\n" "$PREFIX" "$SENTINEL"')"
[[ "$out" == $'$HOMELESS:$PATHOLOGY\n__CLAUDE_CONTAINED_HOME_REF__' ]]
check "only complete HOME and PATH references expand" $?

printf '%d passed, %d failed\n' "$passes" "$failures"
[[ "$failures" -eq 0 ]]
