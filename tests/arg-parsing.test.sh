#!/usr/bin/env bash
#
# Tests for the flag-only argument surface.
#
# The launcher used to accept `[main_dir] [extra_dir ...]` positionally, which
# forced -a, -R and --new-session to guess whether a bare token was their value
# or a path. Those heuristics are gone: the project directory comes from -C and
# extra mounts from -m, so nothing before `--` is positional.
#
# Three properties matter beyond plumbing. A positional argument must be a hard
# error rather than silently becoming the project directory; an unknown flag must
# be a hard error rather than falling through to the same place; and `--` must
# still separate tool args, including a second `--` that now reaches the tool
# verbatim instead of being dropped.
#
# Usage: tests/arg-parsing.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"
host_kernel="$(uname -s)"

# A developer's exported build-context or tooling-layer override must not change
# any result here, the same way an ambient CLAUDE_CONTAINED_RUNTIME must not.
unset CLAUDE_CONTAINED_BUILD_CONTEXT
unset CLAUDE_CONTAINED_LAYER
unset CLAUDE_CONTAINED_LOG_LEVEL

stub_dir="$(mktemp -d)"
# The launcher resolves paths through realpath, and on macOS /var is a symlink to
# /private/var, so mktemp paths must be resolved here too or the emitted mount
# arguments will never match what we assert.
proj="$(cd "$(mktemp -d)" && pwd -P)"
extra="$(cd "$(mktemp -d)" && pwd -P)"
home="$(cd "$(mktemp -d)" && pwd -P)"
for rt in container docker; do
  printf '#!/bin/bash\n[[ "${1:-}" == system || "${1:-}" == info || "${1:-}" == list || "${1:-}" == ps || "${1:-}" == inspect ]] && exit 0\nprintf "%%s\\n" "$@"\n' \
    > "${stub_dir}/${rt}"
  chmod +x "${stub_dir}/${rt}"
done

out="$(mktemp)"
err="$(mktemp)"

# run <target> <flags...>; captures argv on stdout and diagnostics on stderr.
run() {
  local target="$1"; shift
  HOME="$home" PATH="${stub_dir}:$PATH" "${repo_root}/${target}" "$@" \
    >"$out" 2>"$err" </dev/null
}

# `--` before the pattern: almost every string asserted here starts with a dash,
# which grep would otherwise read as its own option.
line_has() { grep -qxF -- "$2" "$1"; }
file_has() { grep -qF -- "$2" "$1"; }

suite() {
  set +e
  local target="$1"
  local fails=0
  local rc

  _check() {
    if [[ "$2" -eq 0 ]]; then
      echo "  PASS: $1"
    else
      echo "  FAIL: $1"
      fails=$((fails + 1))
    fi
  }

  # 1. A positional used to become main_dir. Now it names its replacement.
  run "$target" "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "positional arguments are no longer accepted" \
    && file_has "$err" "-C/--dir" && file_has "$err" "-m/--mount"
  _check "a positional argument is rejected with a fix-it hint" $?

  # 2. `*) break` used to turn an unknown flag into main_dir, so a typo failed
  #    later with a confusing path error instead of naming the flag.
  run "$target" --bogus -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "unknown flag: --bogus"
  _check "an unknown flag is rejected by name" $?

  # 3. -C selects the project directory and becomes the container workdir.
  run "$target" -N -s -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "$proj"
  _check "-C sets the project directory" $?

  # 4-5. -m carries the :ro/:rw suffix; --readonly-extras sets the default.
  run "$target" -N -s -C "$proj" -m "${extra}:ro"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "type=bind,src=${extra},dst=${extra},readonly"
  _check "-m DIR:ro mounts read-only" $?

  run "$target" -N -s -C "$proj" --readonly-extras -m "$extra"
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "type=bind,src=${extra},dst=${extra},readonly"
  _check "--readonly-extras makes an unsuffixed -m read-only" $?

  run "$target" -N -s -C "$proj" -m "${extra}:rw" --readonly-extras
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "type=bind,src=${extra},dst=${extra}"
  _check "an explicit :rw suffix beats --readonly-extras" $?

  # 6. The project directory is the working directory and cannot be read-only.
  run "$target" -N -s -C "${proj}:ro"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "project directory cannot be read-only"
  _check "-C DIR:ro is rejected" $?

  # 7. Every value-taking flag used to do a bare `shift; x="$1"`, which died with
  #    "unbound variable" under `set -u` when the flag came last.
  local flag
  for flag in -C --dir -m --mount -t --tool -e --env -p -H --dns --allow-host --name --session --share-skills; do
    run "$target" "$flag"
    rc=$?
    [[ $rc -eq 2 ]] && file_has "$err" "requires" && ! file_has "$err" "unbound variable"
    _check "${flag} as the final argument reports a missing value" $?
  done

  # 8. A value that is really the next flag is a missing value, not a swallowed one.
  run "$target" -t --yolo
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "requires"
  _check "-t does not swallow a following flag as its value" $?

  # 9. `--` still separates tool args, and a second `--` now reaches the tool.
  # No -s here: --shell runs bash, which does not take the tool's args.
  run "$target" -N -C "$proj" -- --model sonnet
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "--model" && line_has "$out" "sonnet"
  _check "-- passes tool args through" $?

  run "$target" -N -C "$proj" -- claude -- foo
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "--"
  _check "a second -- reaches the tool verbatim" $?

  # 10. --name replaces -a's old create-on-miss behavior, and is sanitized and
  #     prefixed the same way a generated name is.
  run "$target" -N -s -C "$proj" --name My_Proj
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "aic-my-proj"
  _check "--name is prefixed and sanitized" $?

  run "$target" -N -s -C "$proj" -a something --name other
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "--name cannot be combined with -a/--attach"
  _check "--name and --attach are mutually exclusive" $?

  # 11. --share-skills required the = form; it now takes both, like --dns.
  run "$target" -N -s -C "$proj" --share-skills "$extra"
  rc=$?
  [[ $rc -eq 0 ]]
  _check "--share-skills accepts the space form" $?

  # 12. -R takes an optional value; anything not a flag is the mode.
  run "$target" -R nonsense
  rc=$?
  [[ $rc -ne 0 ]] && file_has "$err" "rebuild mode"
  _check "-R rejects an unknown rebuild mode" $?

  # 12b. A rebuild emits a build, not a run, and exits without a session.
  run "$target" -R
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "build" && file_has "$out" "AI_TOOLS_CACHE_BUST=" \
    && line_has "$out" "claude-contained:latest" && ! line_has "$out" "run"
  _check "-R builds the image and does not start a session" $?

  run "$target" -R full
  rc=$?
  [[ $rc -eq 0 ]] && line_has "$out" "--pull" && line_has "$out" "--no-cache"
  _check "-R full pulls and rebuilds without cache" $?

  # 13. --session is meaningless without --zellij; say so rather than ignoring it.
  run "$target" -N -s -C "$proj" --session review
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "--session is valid only with --zellij"
  _check "--session requires --zellij" $?

  # --- the container-runtime flag ------------------------------------------
  #
  # The discriminator is --ssh, not the Docker socket mount: Apple emits `--ssh`
  # on every platform and Docker emits it on none, so these assertions hold on a
  # Linux host too. Asserting the macOS bridged-socket mount would pass here and
  # fail in CI.
  run "$target" --container-runtime
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "--container-runtime requires apple or docker"
  _check "--container-runtime reports a missing value" $?

  run "$target" --container-runtime=bogus
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "must be apple or docker"
  _check "--container-runtime rejects an unknown runtime" $?

  run "$target" -N -s -S -C "$proj" --container-runtime=docker
  rc=$?
  [[ $rc -eq 0 ]] && ! line_has "$out" "--ssh"
  _check "--container-runtime=docker selects the Docker runtime" $?

  run "$target" -N -s -S -C "$proj" --container-runtime=apple
  rc=$?
  if [[ "$host_kernel" == Darwin ]]; then
    [[ $rc -eq 0 ]] && line_has "$out" "--ssh"
    _check "--container-runtime=apple selects Apple Containers" $?
  else
    [[ $rc -eq 2 ]] && file_has "$err" "available only on macOS"
    _check "--container-runtime=apple is rejected off macOS" $?
  fi

  CLAUDE_CONTAINED_RUNTIME=docker run "$target" -N -s -S -C "$proj"
  rc=$?
  [[ $rc -eq 0 ]] && ! line_has "$out" "--ssh"
  _check "CLAUDE_CONTAINED_RUNTIME selects the runtime" $?

  CLAUDE_CONTAINED_RUNTIME=docker run "$target" -N -s -S -C "$proj" --container-runtime=apple
  rc=$?
  if [[ "$host_kernel" == Darwin ]]; then
    [[ $rc -eq 0 ]] && line_has "$out" "--ssh"
    _check "the flag beats CLAUDE_CONTAINED_RUNTIME" $?
  else
    [[ $rc -eq 2 ]] && file_has "$err" "available only on macOS"
    _check "the apple flag beats the environment before host validation" $?
  fi

  # --- the build-context flag ---------------------------------------------
  run "$target" --build-context
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "--build-context requires a directory"
  _check "--build-context reports a missing value" $?

  run "$target" -R --build-context "$extra"
  rc=$?
  [[ $rc -eq 2 ]] && file_has "$err" "--build-context has no Dockerfile"
  _check "--build-context without a Dockerfile is refused by name" $?

  return "$fails"
}

total=0
read -ra targets <<< "${CLAUDE_CONTAINED_TEST_TARGETS:-bin/claude-contained bin/claude-contained-docked}"
for target in "${targets[@]}"; do
  echo "== ${target} =="
  suite "$target"
  total=$((total + $?))
done

rm -rf "$stub_dir" "$proj" "$extra" "$home"
rm -f "$out" "$err"

if [[ "$total" -gt 0 ]]; then
  echo
  echo "${total} arg-parsing test(s) failed."
  exit 1
fi

echo
echo "All arg-parsing tests passed."
