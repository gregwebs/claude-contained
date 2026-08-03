#!/usr/bin/env bash
# Exercises the built launcher's diagnostic configuration, routing, redaction,
# and file security without contacting a real container runtime.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"
task_tmp="$(mktemp -d "${TMPDIR:-/tmp}/claude-contained-diagnostic-stream.XXXXXX")"
trap 'rm -rf "$task_tmp"' EXIT

fails=0
check() {
  local label="$1" result="$2"
  if [[ "$result" -eq 0 ]]; then
    printf '  PASS: %s\n' "$label"
  else
    printf '  FAIL: %s\n' "$label"
    fails=$((fails + 1))
  fi
}

stub_dir="$task_tmp/stubs"
mkdir -p "$stub_dir"
for runtime_bin in container docker; do
  stub="$stub_dir/$runtime_bin"
  printf '%s\n' \
    '#!/bin/sh' \
    'case "${1:-}" in' \
    '  system|info|list|inspect) exit 0 ;;' \
    '  ps) [ -n "${STUB_LIST:-}" ] && printf "%s\n" "$STUB_LIST"; exit 0 ;;' \
    '  exec) printf "attached stdout\n"; printf "attached stderr\n" >&2; exit 0 ;;' \
    'esac' \
    'printf "%s\n" "$@"' > "$stub"
  chmod +x "$stub"
done

file_mode() {
  stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1"
}

read -ra targets <<< "${CLAUDE_CONTAINED_TEST_TARGETS:-bin/claude-contained bin/claude-contained-docked}"
for target in "${targets[@]}"; do
  printf '== %s ==\n' "$target"
  root="$task_tmp/$(basename "$target")"
  home="$root/home"
  project="$root/project"
  out="$root/stdout"
  err="$root/stderr"
  mkdir -p "$home" "$project" "$project/.claude-contained"

  run_launcher() {
    HOME="$home" \
      PATH="$stub_dir:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
      CLAUDE_CONTAINED_RUNTIME='' \
      CLAUDE_CONTAINED_BUILD_CONTEXT='' \
      CLAUDE_CONTAINED_LAYER='' \
      CLAUDE_CONTAINED_SHARE_HOST_CLAUDE='' \
      CLAUDE_CONTAINED_LOG_LEVEL="${TEST_LOG_LEVEL:-}" \
      CLAUDE_MEMORY='' CLAUDE_DNS='system' TZ='' \
      AI_GH_TOKEN="${TEST_AI_GH_TOKEN:-}" SSH_AUTH_SOCK='' \
      STUB_LIST="${STUB_LIST:-}" \
      "$repo_root/$target" --container-runtime=docker "$@" >"$out" 2>"$err" </dev/null
  }

  run_launcher -N -s -C "$project"
  rc=$?
  if [[ "$rc" -eq 0 ]] && ! grep -q 'kind=diagnostic' "$err"; then result=0; else result=1; fi
  check "default remains diagnostic-silent" "$result"

  TEST_LOG_LEVEL=debug run_launcher -N -s -C "$project"
  rc=$?
  if [[ "$rc" -eq 0 ]] && grep -q 'component=cli' "$err"; then result=0; else result=1; fi
  check "environment level enables diagnostics" "$result"

  TEST_LOG_LEVEL=debug run_launcher --log-level=off -N -s -C "$project"
  rc=$?
  if [[ "$rc" -eq 0 ]] && ! grep -q 'kind=diagnostic' "$err"; then result=0; else result=1; fi
  check "explicit off beats the environment" "$result"

  sentinel='DIAGNOSTIC-SHELL-LEAK-SENTINEL'
  printf 'FILE_SECRET=%s\n' "$sentinel" > "$project/.claude-contained/env"
  TEST_AI_GH_TOKEN="$sentinel" run_launcher --log-level=debug \
    -e "FLAG_SECRET=$sentinel" -N -s -C "$project"
  rc=$?
  if [[ "$rc" -eq 0 ]] &&
    grep -q 'component=cli' "$err" && grep -q 'component=runtime' "$err" &&
    grep -q 'FLAG_SECRET' "$err" && grep -q 'FILE_SECRET' "$err" &&
    ! grep -qF "$sentinel" "$err"; then result=0; else result=1; fi
  check "debug anchors redact flag, file, and token values" "$result"
  rm -f "$project/.claude-contained/env"

  log_file="$root/diagnostic.log"
  printf 'old contents\n' > "$log_file"
  chmod 666 "$log_file"
  run_launcher --log-level=info --log-file "$log_file" -N -s -C "$project"
  rc=$?
  if [[ "$rc" -eq 0 ]] && [[ "$(file_mode "$log_file")" == 600 ]] &&
    grep -q 'container runtime selected' "$log_file" &&
    ! grep -q 'old contents' "$log_file" && [[ ! -s "$err" ]]; then result=0; else result=1; fi
  check "log file is exclusive, truncated, and does not tee" "$result"

  off_file="$root/off.log"
  printf 'old contents\n' > "$off_file"
  chmod 666 "$off_file"
  run_launcher --log-file "$off_file" -N -s -C "$project"
  rc=$?
  if [[ "$rc" -eq 0 ]] && [[ "$(file_mode "$off_file")" == 600 ]] &&
    [[ ! -s "$off_file" ]]; then result=0; else result=1; fi
  check "log file alone stays empty while securing and truncating" "$result"

  run_launcher --log-only --log-level=error --wat
  rc=$?
  if [[ "$rc" -eq 2 ]] && grep -q 'kind=output stream=stderr' "$err" &&
    grep -q 'unknown flag: --wat' "$err" &&
    ! grep -q 'command line validation failed' "$err"; then result=0; else result=1; fi
  check "relocated warning survives error threshold" "$result"

  STUB_LIST=aic-live run_launcher --log-only --log-level=error --attach live
  rc=$?
  if [[ "$rc" -eq 0 ]] && [[ ! -s "$out" ]] &&
    grep -q 'msg="attached stdout" kind=output stream=stdout' "$err" &&
    grep -q 'msg="attached stderr" kind=output stream=stderr' "$err"; then result=0; else result=1; fi
  check "log-only proxies post-attach child output" "$result"

  help_file="$root/help.log"
  rm -f "$help_file"
  run_launcher --help --log-file "$help_file"
  rc=$?
  if [[ "$rc" -eq 0 ]] && grep -q '^Usage:' "$out" && [[ ! -e "$help_file" ]]; then result=0; else result=1; fi
  check "help bypasses the stream without file side effects" "$result"

  run_launcher --log-level=debug --log-file "$root/missing/diagnostic.log"
  rc=$?
  if [[ "$rc" -eq 2 ]] && grep -q 'cannot open diagnostic file' "$err" &&
    ! grep -q 'kind=' "$err"; then result=0; else result=1; fi
  check "file setup failure stays on original stderr" "$result"
done

if [[ "$fails" -ne 0 ]]; then
  printf '\n%d diagnostic stream test(s) failed.\n' "$fails"
  exit 1
fi
