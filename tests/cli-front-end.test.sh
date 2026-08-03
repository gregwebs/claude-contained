#!/usr/bin/env bash
#
# Proves the built launcher's two-pass CLI front end without touching a real
# container runtime or persistent user state. The in-process Go tests inject a
# host platform and runner; this suite instead exercises the shipped binary and
# verifies exact help/diagnostic streams, runtime non-invocation on early exits,
# runtime selection across scanner boundaries, and host-filesystem silence.
#
# Usage: tests/cli-front-end.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"

total=0
active_harness=''

cleanup() {
  if [[ -n "$active_harness" ]]; then
    rm -rf "$active_harness"
  fi
}
trap cleanup EXIT

suite() {
  local target="$1"
  local harness_root stub_dir home_dir project_dir
  local out err runtime_log baseline current
  local captured_rc=0
  local fails=0

  harness_root="$(mktemp -d /private/tmp/claude-contained-cli-front-end.XXXXXX 2>/dev/null \
    || mktemp -d "${TMPDIR:-/tmp}/claude-contained-cli-front-end.XXXXXX")"
  active_harness="$harness_root"
  stub_dir="$harness_root/stubs"
  home_dir="$harness_root/home"
  project_dir="$harness_root/project"
  out="$harness_root/stdout"
  err="$harness_root/stderr"
  runtime_log="$harness_root/runtime.log"
  baseline="$harness_root/baseline.manifest"
  current="$harness_root/current.manifest"

  mkdir -p "$stub_dir" "$project_dir"
  mkdir -p \
    "$home_dir/.claude/skills" \
    "$home_dir/.claude/agents" \
    "$home_dir/.claude/commands" \
    "$home_dir/.claude/plugins" \
    "$home_dir/.claude-contained/claude/skills" \
    "$home_dir/.claude-contained/claude/agents" \
    "$home_dir/.claude-contained/claude/commands" \
    "$home_dir/.claude-contained/claude/plugins" \
    "$home_dir/.codex" \
    "$home_dir/.copilot" \
    "$home_dir/.gemini" \
    "$home_dir/.vibe"
  : > "$home_dir/.claude-contained/.claude.json"
  ln -s "$home_dir/.claude-contained/.claude.json" "$home_dir/.claude.json"

  printf '%s\n' \
    '#!/bin/sh' \
    'printf "CALL %s\n" "$(basename "$0")" >> "$STUB_LOG"' \
    'printf "%s\n" "$@" >> "$STUB_LOG"' \
    'touch "$STUB_DIR/$(basename "$0").called"' \
    'exit 0' > "$stub_dir/runtime-stub"
  chmod +x "$stub_dir/runtime-stub"
  ln -s runtime-stub "$stub_dir/docker"
  ln -s runtime-stub "$stub_dir/container"

  manifest() {
    find "$home_dir" "$project_dir" -print | LC_ALL=C sort
  }

  manifest > "$baseline"

  reset_observations() {
    rm -f "$stub_dir/docker.called" "$stub_dir/container.called" "$runtime_log"
    : > "$out"
    : > "$err"
  }

  capture() {
    reset_observations
    HOME="$home_dir" \
      PATH="$stub_dir:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
      CLAUDE_CONTAINED_RUNTIME=apple \
      CLAUDE_CONTAINED_BUILD_CONTEXT='' \
      CLAUDE_CONTAINED_LAYER='' \
      CLAUDE_CONTAINED_LOG_LEVEL='' \
      AI_GH_TOKEN='' \
      CLAUDE_DNS='' \
      STUB_DIR="$stub_dir" \
      STUB_LOG="$runtime_log" \
      "$repo_root/$target" "$@" > "$out" 2> "$err" </dev/null
    captured_rc=$?
  }

  unchanged_filesystem() {
    manifest > "$current"
    cmp -s "$baseline" "$current" || return 1
    [[ -L "$home_dir/.claude.json" ]] || return 1
    [[ "$(readlink "$home_dir/.claude.json")" == "$home_dir/.claude-contained/.claude.json" ]] || return 1
    cmp -s /dev/null "$home_dir/.claude-contained/.claude.json"
  }

  no_runtime_invocation() {
    [[ ! -e "$stub_dir/docker.called" ]] &&
      [[ ! -e "$stub_dir/container.called" ]] &&
      [[ ! -e "$runtime_log" ]]
  }

  report() {
    local label="$1"
    local failed="$2"
    if [[ "$failed" -eq 0 ]]; then
      printf '  PASS: %s\n' "$label"
    else
      printf '  FAIL: %s\n' "$label"
      fails=$((fails + 1))
    fi
  }

  check_help() {
    local label="$1"
    local expected="$2"
    local failed=0
    shift 2

    capture "$@"
    [[ "$captured_rc" -eq 0 ]] || failed=1
    if ! cmp -s "$expected" "$out"; then
      diff -u "$expected" "$out" || true
      failed=1
    fi
    if ! cmp -s /dev/null "$err"; then
      diff -u /dev/null "$err" || true
      failed=1
    fi
    no_runtime_invocation || failed=1
    if ! unchanged_filesystem; then
      diff -u "$baseline" "$current" || true
      failed=1
    fi
    report "$label" "$failed"
  }

  check_error() {
    local label="$1"
    local expected_rc="$2"
    local expected_err="$3"
    local failed=0
    shift 3

    capture "$@"
    [[ "$captured_rc" -eq "$expected_rc" ]] || failed=1
    if ! cmp -s /dev/null "$out"; then
      diff -u /dev/null "$out" || true
      failed=1
    fi
    if ! diff -u <(printf '%s' "$expected_err") "$err"; then
      failed=1
    fi
    no_runtime_invocation || failed=1
    if ! unchanged_filesystem; then
      diff -u "$baseline" "$current" || true
      failed=1
    fi
    report "$label" "$failed"
  }

  check_docker_launch() {
    local failed=0

    capture --container-runtime=docker -N -s -C "$project_dir"
    [[ "$captured_rc" -eq 0 ]] || failed=1
    cmp -s /dev/null "$out" || failed=1
    cmp -s /dev/null "$err" || failed=1
    [[ -e "$stub_dir/docker.called" ]] || failed=1
    [[ ! -e "$stub_dir/container.called" ]] || failed=1
    grep -qxF 'CALL docker' "$runtime_log" || failed=1
    grep -qxF 'info' "$runtime_log" || failed=1
    grep -qxF 'ps' "$runtime_log" || failed=1
    grep -qxF 'run' "$runtime_log" || failed=1
    grep -qxF -- '-w' "$runtime_log" || failed=1
    grep -qxF "$project_dir" "$runtime_log" || failed=1
    grep -qxF 'claude-contained:latest' "$runtime_log" || failed=1
    grep -qxF '/usr/local/bin/shell-run' "$runtime_log" || failed=1
    if ! unchanged_filesystem; then
      diff -u "$baseline" "$current" || true
      failed=1
    fi
    report 'explicit Docker launch invokes only the Docker stub' "$failed"
  }

  check_docker_launch

  check_help 'invalid runtime followed by help uses Apple fallback' \
    "$repo_root/internal/runtime/help_contained.txt" \
    --container-runtime=bogus --help
  check_help 'help followed by Docker runtime uses Docker help' \
    "$repo_root/internal/runtime/help_docked.txt" \
    --help --container-runtime=docker
  check_help 'explicit Apple runtime uses Apple help' \
    "$repo_root/internal/runtime/help_contained.txt" \
    --container-runtime=apple --help

  check_error 'runtime flag before help requires a value' 2 \
    $'error: --container-runtime requires apple or docker\n' \
    --container-runtime --help

  check_help 'help wins over a trailing runtime flag without a value' \
    "$repo_root/internal/runtime/help_contained.txt" \
    --help --container-runtime

  check_error 'unknown flag emits its exact diagnostic' 2 \
    $'error: unknown flag: --wat\n       run \'claude-contained --help\' for the supported flags\n' \
    --wat
  check_error 'positional argument emits exact replacement guidance' 2 \
    $'error: positional arguments are no longer accepted: some-path\n       use -C/--dir for the project directory:  claude-contained -C some-path\n       use -m/--mount for extra directories:    claude-contained -m some-path\n       (bare \'claude-contained\' uses the current directory)\n' \
    some-path
  check_error 'new-session value emits exact replacement guidance' 2 \
    $'error: --new-session no longer takes a name; use --session=NAME\n' \
    --new-session=name

  check_help 'required-value flag masks a runtime-looking value' \
    "$repo_root/internal/runtime/help_contained.txt" \
    --help -e --container-runtime=docker
  check_help 'malformed runtime leaves a following runtime visible' \
    "$repo_root/internal/runtime/help_docked.txt" \
    --help --container-runtime --container-runtime=docker
  check_help 'tool boundary hides a following runtime' \
    "$repo_root/internal/runtime/help_contained.txt" \
    --help --container-runtime -- --container-runtime=docker
  check_help 'consumed boundary leaves a following runtime visible' \
    "$repo_root/internal/runtime/help_docked.txt" \
    --help -e -- --container-runtime=docker

  rm -rf "$harness_root"
  active_harness=''
  return "$fails"
}

read -ra targets <<< "${CLAUDE_CONTAINED_TEST_TARGETS:-bin/claude-contained}"
for target in "${targets[@]}"; do
  echo "== $target =="
  suite "$target"
  total=$((total + $?))
done

if [[ "$total" -gt 0 ]]; then
  echo
  echo "$total CLI front-end test(s) failed."
  exit 1
fi

echo
echo 'All CLI front-end tests passed.'
