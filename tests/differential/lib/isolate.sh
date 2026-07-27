#!/usr/bin/env bash
#
# Per-side isolation: a fresh HOME, a fresh copy of the project-directory
# template, and a stub PATH that intercepts `container`/`docker` instead of
# starting anything real. Two calls to make_fixture give the two sides of a
# comparison their own state so neither run can observe the other's
# mutations (the Claude account-state relocation, the copied git config, and
# any worktree lock writes all land under the fixture HOME/project, not the
# real host paths).

# write_stub_runtimes <stub_dir>; installs fake `container` and `docker`
# executables. Both share one body and branch on argv[0]'s basename.
#
# Contract, read from the environment at call time by whichever corpus case
# is running:
#   DIFF_ARGV_LOG      - path; every non-probe invocation (the actual
#                        run/exec/build) appends its full argv here. This is
#                        what makes "runtime arguments" independently
#                        diffable from stdout, instead of scraped from it.
#   DIFF_LIST_OUTPUT   - path; its lines are echoed back for the
#                        list-running-containers probe (container `list
#                        --quiet` / docker `ps --format ... --filter ...`).
#                        Missing/unset means no running containers.
#   DIFF_INSPECT_DIR   - dir; `inspect <name>` looks for
#                        `<DIFF_INSPECT_DIR>/<name>.env` (KEY=VALUE lines)
#                        and reports that as the container's environment,
#                        shaped per-runtime (container wraps it in the JSON
#                        the launcher's jq filter expects; docker prints the
#                        lines raw, matching `docker inspect -f
#                        '{{range .Config.Env}}...'`). Missing file means an
#                        empty environment.
#   DIFF_SNAPSHOT_PATHS - colon-separated absolute paths; on every non-probe
#                        invocation, each path that exists is copied to
#                        `<path>.mid-run-snapshot` before returning. This is
#                        how the worktree lock/unlock corpus entry proves the
#                        lock existed *while the container ran*, even though
#                        the stub returns immediately and the launcher's own
#                        EXIT trap unlocks it again before the process ends:
#                        the snapshot lands inside the fixture tree, so the
#                        ordinary manifest walk picks it up with no special
#                        casing.
#
# `system`/`info` (the "is the container runtime up" probe) always succeeds:
# that prompt isn't part of this corpus.
write_stub_runtimes() { # write_stub_runtimes <stub_dir>
  local dir="$1"

  cat > "${dir}/container" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail
self="container"
sub="${1:-}"
shift || true

case "$sub" in
  system|info)
    exit 0
    ;;
  list)
    [[ -n "${DIFF_LIST_OUTPUT:-}" && -f "${DIFF_LIST_OUTPUT}" ]] && cat "${DIFF_LIST_OUTPUT}"
    exit 0
    ;;
  inspect)
    name=""
    for a in "$@"; do name="$a"; done
    envfile="${DIFF_INSPECT_DIR:-}/${name}.env"
    printf '[{"configuration":{"initProcess":{"environment":['
    if [[ -f "$envfile" ]]; then
      first=1
      while IFS= read -r line; do
        [[ -z "$line" ]] && continue
        [[ $first -eq 0 ]] && printf ','
        printf '"%s"' "$line"
        first=0
      done < "$envfile"
    fi
    printf ']}}}]\n'
    exit 0
    ;;
  *)
    if [[ -n "${DIFF_SNAPSHOT_PATHS:-}" ]]; then
      IFS=':' read -ra __snap_paths <<< "${DIFF_SNAPSHOT_PATHS}"
      for __p in "${__snap_paths[@]}"; do
        [[ -e "$__p" ]] && cp -a "$__p" "${__p}.mid-run-snapshot" 2>/dev/null
      done
    fi
    if [[ -n "${DIFF_ARGV_LOG:-}" ]]; then
      {
        printf '== %s %s ==\n' "$self" "$sub"
        printf '%s\n' "$@"
        printf '\n'
      } >> "${DIFF_ARGV_LOG}"
    fi
    exit 0
    ;;
esac
STUB

  cat > "${dir}/docker" <<'STUB'
#!/usr/bin/env bash
set -uo pipefail
self="docker"
sub="${1:-}"
shift || true

case "$sub" in
  system|info)
    exit 0
    ;;
  ps)
    [[ -n "${DIFF_LIST_OUTPUT:-}" && -f "${DIFF_LIST_OUTPUT}" ]] && cat "${DIFF_LIST_OUTPUT}"
    exit 0
    ;;
  inspect)
    name=""
    for a in "$@"; do name="$a"; done
    envfile="${DIFF_INSPECT_DIR:-}/${name}.env"
    [[ -f "$envfile" ]] && cat "$envfile"
    exit 0
    ;;
  *)
    if [[ -n "${DIFF_SNAPSHOT_PATHS:-}" ]]; then
      IFS=':' read -ra __snap_paths <<< "${DIFF_SNAPSHOT_PATHS}"
      for __p in "${__snap_paths[@]}"; do
        [[ -e "$__p" ]] && cp -a "$__p" "${__p}.mid-run-snapshot" 2>/dev/null
      done
    fi
    if [[ -n "${DIFF_ARGV_LOG:-}" ]]; then
      {
        printf '== %s %s ==\n' "$self" "$sub"
        printf '%s\n' "$@"
        printf '\n'
      } >> "${DIFF_ARGV_LOG}"
    fi
    exit 0
    ;;
esac
STUB

  chmod +x "${dir}/container" "${dir}/docker"
}

# make_fixture <template_project_dir>; sets FIXTURE_ROOT, FIXTURE_HOME,
# FIXTURE_PROJ, FIXTURE_STUB as globals (bash-3.2-compatible: no namerefs).
# Call once per side, read the globals immediately -- callers never run two
# fixtures concurrently within one process.
#
# FIXTURE_PROJ and FIXTURE_HOME always get the fixed basenames "project" and
# "home" under a freshly randomized FIXTURE_ROOT. The basename matters: the
# container name embeds sanitize_foldername(project-dir), so two sides with
# different random mktemp basenames would get different container names by
# construction and every comparison downstream would "diverge" on that alone.
# A fixed basename removes that source of noise at the source, the same way
# a fixed template removes it for file contents, instead of trying to
# reverse the sanitizer's transform in a normalizer later. The absolute path
# itself still differs between sides (different FIXTURE_ROOT); that residue
# is handled by neutralize_paths (normalize.sh) at compare time.
make_fixture() {
  local template="$1"

  FIXTURE_ROOT="$(mktemp -d)"
  FIXTURE_HOME="${FIXTURE_ROOT}/home"
  FIXTURE_PROJ="${FIXTURE_ROOT}/project"
  FIXTURE_STUB="${FIXTURE_ROOT}/stub"
  mkdir -p "$FIXTURE_HOME" "$FIXTURE_PROJ" "$FIXTURE_STUB"
  # The launcher resolves paths through realpath, and on macOS /var is a
  # symlink to /private/var, so mktemp paths must be resolved here too or
  # emitted mount arguments (and our own manifest labels) will never match.
  FIXTURE_HOME="$(cd "$FIXTURE_HOME" && pwd -P)"
  FIXTURE_PROJ="$(cd "$FIXTURE_PROJ" && pwd -P)"

  if [[ -d "$template" ]]; then
    cp -R "${template}/." "$FIXTURE_PROJ"/
  fi
  write_stub_runtimes "$FIXTURE_STUB"
}
