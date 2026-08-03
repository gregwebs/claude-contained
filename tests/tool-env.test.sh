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

out="$(HOME=/host/home PATH=/bin:/usr/bin "$resolver" --directory "$tmp/missing" \
  sh -c 'printf "%s\n%s\n%s\n%s" "${JAVA_HOME-unset}" "${JAVA_TOOL_OPTIONS-unset}" "${MAVEN_OPTS-unset}" "$PATH"')"
if [[ "$out" == $'unset\nunset\nunset\n/host/home/.local/bin:/opt/claude:/bin:/usr/bin' ]]; then status=0; else status=1; fi
check "base resolution adds only generic paths and no Java environment" "$status"

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

printf '%s\n' \
  'JAVA_HOME=/opt/custom-java' \
  'JAVA_TOOL_OPTIONS=-XX:+UseG1GC -XX:+AllowEnhancedClassRedefinition -Dvaadin.productionMode=false' \
  'MAVEN_OPTS=-Dmaven.repo.local=$HOME/.claude-contained/cache/maven' \
  'PATH=/opt/custom-java/bin:/opt/maven/bin:/opt/jbang/bin:$PATH' \
  >"$tmp/fragments/10-tool"
rm -f "$tmp/fragments/20-path"
out="$(HOME=/host/home PATH=/bin:/usr/bin "$resolver" --directory "$tmp/fragments" \
  sh -c 'printf "%s\n%s\n%s\n%s\n" "$JAVA_HOME" "$JAVA_TOOL_OPTIONS" "$MAVEN_OPTS" "$PATH"')"
grep -Fqx '/opt/custom-java' <<<"$out" \
  && grep -Fqx -- '-XX:+UseG1GC -XX:+AllowEnhancedClassRedefinition -Dvaadin.productionMode=false' <<<"$out" \
  && grep -Fqx -- '-Dmaven.repo.local=/host/home/.claude-contained/cache/maven' <<<"$out" \
  && grep -Fqx '/opt/custom-java/bin:/opt/maven/bin:/opt/jbang/bin:/host/home/.local/bin:/opt/claude:/bin:/usr/bin' <<<"$out"
check "a layer fragment supplies the complete Java environment" $?

out="$(JAVA_HOME=/explicit HOME=/host/home PATH=/bin:/usr/bin "$resolver" --directory "$tmp/fragments" \
  sh -c 'printf "%s" "$JAVA_HOME"')"
if [[ "$out" == /explicit ]]; then status=0; else status=1; fi
check "an explicit JAVA_HOME wins over a layer default" "$status"

out="$(
  JAVA_HOME=/opt/jbr \
    JAVA_TOOL_OPTIONS='from-image' \
    MAVEN_OPTS='-Dmaven.repo.local=/home/dev/.claude-contained/cache/maven' \
    HOME=/home/dev \
    PATH=/bin:/usr/bin \
    "$resolver" --directory "$tmp/fragments" \
    sh -c 'printf "%s\n%s\n%s" "$JAVA_HOME" "$JAVA_TOOL_OPTIONS" "$MAVEN_OPTS"'
)"
if [[ "$out" == $'/opt/jbr\nfrom-image\n-Dmaven.repo.local=/home/dev/.claude-contained/cache/maven' ]]; then status=0; else status=1; fi
check "direct-image execution preserves its image-level Java defaults" "$status"

out="$(
  CLAUDE_CONTAINED_EXPLICIT_ENV_KEYS=JAVA_HOME \
    JAVA_HOME=/explicit \
    JAVA_TOOL_OPTIONS='from-image' \
    MAVEN_OPTS='-Dmaven.repo.local=/home/dev/.claude-contained/cache/maven' \
    HOME=/host/home \
    PATH=/bin:/usr/bin \
    "$resolver" --directory "$tmp/fragments" \
    sh -c 'printf "%s\n%s\n%s\n%s" "$JAVA_HOME" "$JAVA_TOOL_OPTIONS" "$MAVEN_OPTS" "${CLAUDE_CONTAINED_EXPLICIT_ENV_KEYS-unset}"'
)"
grep -Fqx '/explicit' <<<"$out" \
  && grep -Fqx -- '-XX:+UseG1GC -XX:+AllowEnhancedClassRedefinition -Dvaadin.productionMode=false' <<<"$out" \
  && grep -Fqx -- '-Dmaven.repo.local=/host/home/.claude-contained/cache/maven' <<<"$out" \
  && grep -Fqx 'unset' <<<"$out"
check "launcher and attach defaults resolve Maven beneath effective HOME without overriding explicit keys" $?

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
