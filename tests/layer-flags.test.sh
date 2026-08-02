#!/usr/bin/env bash
#
# Proves the shipped binary's tooling-layer flag surface without a real
# container runtime.
#
# The in-process Go tests cover the layer decision itself; this suite exists for
# the one thing they cannot reach -- the binary as installed, with its own
# argv[0], its own help text, and a real process environment. The properties
# that matter here are that the three flags are documented, that the conflict
# and required-value diagnoses are byte-exact, that a named layer directory
# without a Dockerfile refuses rather than falling through to the base image,
# and that --no-layer really does run the base image.
#
# Usage: tests/layer-flags.test.sh
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
  local harness_root stub_dir home_dir project_dir id_dir
  local out err runtime_log baseline current
  local captured_rc=0
  local fails=0

  harness_root="$(mktemp -d /private/tmp/claude-contained-layer-flags.XXXXXX 2>/dev/null \
    || mktemp -d "${TMPDIR:-/tmp}/claude-contained-layer-flags.XXXXXX")"
  active_harness="$harness_root"
  stub_dir="$harness_root/stubs"
  home_dir="$harness_root/home"
  project_dir="$harness_root/project"
  id_dir="$harness_root/image-ids"
  out="$harness_root/stdout"
  err="$harness_root/stderr"
  runtime_log="$harness_root/runtime.log"
  baseline="$harness_root/baseline.manifest"
  current="$harness_root/current.manifest"

  mkdir -p "$stub_dir" "$home_dir" "$project_dir" "$id_dir"

  # The stub answers `image inspect` to Runtime.ImageID's contract rather than
  # falling through to a bare `exit 0`: that default means "succeeded and said
  # nothing", which the launcher classifies as a *fault*, not an absence. No
  # case below reaches a probe today, and the arm is here so that the first one
  # that does fails for its own reason rather than for the stub's.
  printf '%s\n' \
    '#!/bin/sh' \
    'self="$(basename "$0")"' \
    'printf "CALL %s\n" "$self" >> "$STUB_LOG"' \
    'printf "%s\n" "$@" >> "$STUB_LOG"' \
    'touch "$STUB_DIR/$self.called"' \
    'if [ "${1:-}" = image ]; then' \
    '  for a in "$@"; do' \
    '    [ "$a" = --help ] && exit 0' \
    '  done' \
    '  ref=""' \
    '  for a in "$@"; do ref="$a"; done' \
    '  idfile="${STUB_IMAGE_ID_DIR:-}/$(printf "%s" "$ref" | tr ":/" "__").id"' \
    '  [ -f "$idfile" ] || exit 1' \
    '  if [ "$self" = container ]; then' \
    '    printf "[{\"descriptor\":{\"digest\":\"%s\"}}]\n" "$(cat "$idfile")"' \
    '  else' \
    '    cat "$idfile"' \
    '  fi' \
    '  exit 0' \
    'fi' \
    'exit 0' > "$stub_dir/runtime-stub"
  chmod +x "$stub_dir/runtime-stub"
  ln -s runtime-stub "$stub_dir/docker"
  ln -s runtime-stub "$stub_dir/container"

  manifest() {
    find "$home_dir" "$project_dir" -print | LC_ALL=C sort
  }

  manifest > "$baseline"

  # The runtime is left unselected on purpose. Forcing apple would make every
  # case that gets past ValidateSelection exit 2 on a Linux CI runner ("the
  # apple container runtime is available only on macOS"), and the cases below
  # deliberately reach further than the CLI validation that cli-front-end.test.sh
  # stops at. Both stub binaries point at the same script, so whichever the host
  # or the target's argv[0] selects behaves identically.
  capture() {
    rm -f "$stub_dir/docker.called" "$stub_dir/container.called" "$runtime_log"
    : > "$out"
    : > "$err"
    HOME="$home_dir" \
      PATH="$stub_dir:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
      CLAUDE_CONTAINED_RUNTIME='' \
      CLAUDE_CONTAINED_BUILD_CONTEXT='' \
      CLAUDE_CONTAINED_LAYER="${LAYER_ENV:-}" \
      CLAUDE_CONTAINED_LOG_LEVEL='' \
      AI_GH_TOKEN='' \
      CLAUDE_DNS='' \
      STUB_DIR="$stub_dir" \
      STUB_LOG="$runtime_log" \
      STUB_IMAGE_ID_DIR="$id_dir" \
      "$repo_root/$target" "$@" > "$out" 2> "$err" </dev/null
    captured_rc=$?
  }

  unchanged_filesystem() {
    manifest > "$current"
    cmp -s "$baseline" "$current"
  }

  no_runtime_invocation() {
    [[ ! -e "$stub_dir/docker.called" ]] &&
      [[ ! -e "$stub_dir/container.called" ]] &&
      [[ ! -e "$runtime_log" ]]
  }

  # A layer refusal happens after the runtime-liveness probe, so the runtime is
  # legitimately invoked -- what must never happen is a build or a container.
  no_build_or_run() {
    [[ ! -e "$runtime_log" ]] && return 0
    ! grep -qxF 'build' "$runtime_log" && ! grep -qxF 'run' "$runtime_log"
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

  check_help_documents_layer() {
    local failed=0
    capture --help
    [[ "$captured_rc" -eq 0 ]] || failed=1
    local text
    for text in '--layer DIR' '--build-layer' '--no-layer' 'CLAUDE_CONTAINED_LAYER'; do
      grep -qF -- "$text" "$out" || { printf '    help does not document %s\n' "$text"; failed=1; }
    done
    no_runtime_invocation || failed=1
    report 'help documents the tooling layer flags and variable' "$failed"
  }

  # A usage error caught by the CLI: nothing on stdout, an exact stderr, and the
  # runtime never contacted at all.
  check_usage_error() {
    local label="$1"
    local expected_err="$2"
    local failed=0
    shift 2

    capture "$@"
    [[ "$captured_rc" -eq 2 ]] || failed=1
    cmp -s /dev/null "$out" || { diff -u /dev/null "$out" || true; failed=1; }
    diff -u <(printf '%s' "$expected_err") "$err" || failed=1
    no_runtime_invocation || failed=1
    if ! unchanged_filesystem; then
      diff -u "$baseline" "$current" || true
      failed=1
    fi
    report "$label" "$failed"
  }

  # A layer refusal on a run: exit 2, exact stderr, no build and no container,
  # and the host left as it was found.
  check_layer_refusal() {
    local label="$1"
    local expected_err="$2"
    local failed=0
    shift 2

    capture "$@"
    [[ "$captured_rc" -eq 2 ]] || failed=1
    cmp -s /dev/null "$out" || { diff -u /dev/null "$out" || true; failed=1; }
    diff -u <(printf '%s' "$expected_err") "$err" || failed=1
    no_build_or_run || failed=1
    if ! unchanged_filesystem; then
      diff -u "$baseline" "$current" || true
      failed=1
    fi
    report "$label" "$failed"
  }

  check_help_documents_layer

  check_usage_error '--no-layer conflicts with --build-layer' \
    $'error: --no-layer cannot be combined with --layer or --build-layer\n       --no-layer runs the base image; the others select or build a tooling layer.\n' \
    --no-layer --build-layer
  check_usage_error '--no-layer conflicts with --layer' \
    $'error: --no-layer cannot be combined with --layer or --build-layer\n       --no-layer runs the base image; the others select or build a tooling layer.\n' \
    --no-layer --layer "$project_dir"
  check_usage_error '--layer requires a directory' \
    $'error: --layer requires a directory\n' \
    --layer
  check_usage_error '--layer= requires a non-empty directory' \
    $'error: --layer requires a non-empty directory\n' \
    --layer=

  check_layer_refusal '--layer naming a Dockerfile-less directory is refused' \
    "error: --layer has no Dockerfile: $harness_root/no-such-layer"$'\n' \
    -N -s -C "$project_dir" --layer "$harness_root/no-such-layer"

  LAYER_ENV="$harness_root/no-such-layer"
  check_layer_refusal 'CLAUDE_CONTAINED_LAYER naming a Dockerfile-less directory is refused' \
    "error: CLAUDE_CONTAINED_LAYER has no Dockerfile: $harness_root/no-such-layer"$'\n' \
    -N -s -C "$project_dir"
  LAYER_ENV=''

  # --no-layer with a real layer checked in: the container starts from the base
  # image, and nothing is built.
  check_no_layer_runs_the_base_image() {
    local failed=0
    mkdir -p "$project_dir/.claude-contained/layer"
    printf 'ARG BASE_IMAGE=claude-contained:latest\nFROM ${BASE_IMAGE}\n' \
      > "$project_dir/.claude-contained/layer/Dockerfile"

    capture -N -s -C "$project_dir" --no-layer
    [[ "$captured_rc" -eq 0 ]] || failed=1
    grep -qxF 'run' "$runtime_log" || failed=1
    grep -qxF 'claude-contained:latest' "$runtime_log" || failed=1
    grep -qF 'claude-contained-layer:' "$runtime_log" && failed=1
    grep -qxF 'build' "$runtime_log" && failed=1
    report '--no-layer runs the base image with a layer checked in' "$failed"
  }

  check_no_layer_runs_the_base_image

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
  echo "$total tooling-layer flag test(s) failed."
  exit 1
fi

echo
echo 'All tooling-layer flag tests passed.'
