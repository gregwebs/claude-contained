#!/usr/bin/env bash
#
# Differential harness: proves that a launcher behaves identically for a
# given invocation across two independent runs, comparing the full observable
# result -- runtime arguments, stdout, stderr, exit status, and a filesystem
# manifest -- rather than just the command line. See README.md in this
# directory for why target-vs-itself is still the default mode, how --compare
# crosses two launchers, and how prompts are handled under non-terminal stdin.
#
# Usage:
#   tests/differential/harness.sh [--target NAME]... [--compare REF:CANDIDATE]...
#                                 [--case GLOB]...
#
# With neither --target nor --compare, runs against both claude-contained and
# claude-docked. Each is compared against itself: two fresh, isolated
# invocations of the same corpus entry against the same target, which is what
# proves the isolation and normalization are complete.
#
# --compare REF:CANDIDATE runs the two sides against *different* launchers,
# which is how the Go launcher is proven equivalent to a bash reference.
# --case restricts the corpus by basename glob, so a caller can assert the
# subset a given ticket implements while the rest stays wired up.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$(dirname "$here")")"
corpus_dir="${here}/corpus"

# shellcheck source=lib/normalize.sh
source "${here}/lib/normalize.sh"
# shellcheck source=lib/manifest.sh
source "${here}/lib/manifest.sh"
# shellcheck source=lib/isolate.sh
source "${here}/lib/isolate.sh"

targets=()
compares=()
case_globs=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --target)
      targets+=("$2")
      shift 2
      ;;
    --compare)
      # REF:CANDIDATE -- run each corpus entry once per side against two
      # *different* launchers, instead of twice against the same one.
      compares+=("$2")
      shift 2
      ;;
    --case)
      # Repeatable glob over corpus basenames, so a caller can assert the
      # subset a given ticket actually implements while the rest of the corpus
      # stays wired up.
      case_globs+=("$2")
      shift 2
      ;;
    *)
      echo "error: unknown flag: $1" >&2
      exit 2
      ;;
  esac
done
if [[ ${#targets[@]} -eq 0 && ${#compares[@]} -eq 0 ]]; then
  targets=(claude-contained claude-docked)
fi

project_template="$(mktemp -d)"

total_entries=0
total_failed=0
total_harness_errors=0

# reset_case_defaults: clears whatever the previous corpus file defined, so a
# case file only needs to set what it actually uses.
reset_case_defaults() {
  CASE_DESCRIPTION=""
  CASE_EXPECT_RUNTIME_ARGS=1
  case_setup() { :; }
  case_args()  { CASE_ARGS_OUT=(); }
  case_stdin() { CASE_STDIN_OUT=""; }
}

# run_side <target> <proj> <home> <stub> <out> <err> <exitfile> <argvlog>
# Calls the current case's hooks (case_setup/case_args/case_stdin), then
# invokes the target once. DIFF_* env vars are left to whatever case_setup
# exported; they are unset first so a previous entry's fixtures can't leak.
run_side() {
  local target="$1" proj="$2" home="$3" stub="$4"
  local out="$5" err="$6" exitfile="$7" argvlog="$8"
  local stdin_file rc

  # Every launcher-observed env var a corpus case is allowed to set via
  # case_setup (export ...) must be unset here first, or one case's export
  # would leak into every later case that doesn't touch it.
  unset DIFF_LIST_OUTPUT DIFF_INSPECT_DIR DIFF_SNAPSHOT_PATHS
  unset CLAUDE_DNS CLAUDE_CONTAINED_SHARE_HOST_CLAUDE CLAUDE_MEMORY AI_GH_TOKEN
  CASE_ARGS_OUT=()
  CASE_STDIN_OUT=""

  case_setup "$proj" "$home"
  case_args "$proj" "$home"
  case_stdin "$proj" "$home"

  if [[ -n "$CASE_STDIN_OUT" ]]; then
    stdin_file="$(mktemp)"
    printf '%s' "$CASE_STDIN_OUT" > "$stdin_file"
  else
    stdin_file=/dev/null
  fi

  : > "$argvlog"
  DIFF_ARGV_LOG="$argvlog" \
    HOME="$home" PATH="${stub}:${PATH}" \
    "${repo_root}/${target}" "${CASE_ARGS_OUT[@]}" <"$stdin_file" >"$out" 2>"$err"
  rc=$?
  printf '%s' "$rc" > "$exitfile"

  [[ "$stdin_file" != "/dev/null" ]] && rm -f "$stdin_file"
}

# side_is_empty <out> <err> <exitfile> <argvlog> <baseline_manifest> <post_manifest>
# The "no observable result at all" guard: true only when nothing about this
# run distinguishes it from doing nothing, on any of the four in-process
# axes or the filesystem. A corpus entry that trips this on either side is a
# harness error, not a pass -- symmetric emptiness must never read as
# "identical".
side_is_empty() {
  local out="$1" err="$2" exitfile="$3" argvlog="$4" baseline="$5" post="$6"

  [[ -s "$out" ]] && return 1
  [[ -s "$err" ]] && return 1
  [[ "$(cat "$exitfile")" != "0" ]] && return 1
  [[ -s "$argvlog" ]] && return 1
  [[ "$baseline" != "$post" ]] && return 1
  return 0
}

# compare_axis <label> <content_a> <content_b>; prints a unified diff on
# mismatch. Returns 1 on mismatch so callers can tally failures.
compare_axis() {
  local label="$1" a="$2" b="$3"

  [[ "$a" == "$b" ]] && return 0
  echo "    diverges: ${label}"
  diff -u <(printf '%s\n' "$a") <(printf '%s\n' "$b") | sed 's/^/      /'
  return 1
}

# prep <home> <proj> <content>; the combined per-side comparison filter:
# collapse this side's own absolute fixture paths to neutral tokens, then
# apply the timestamp/PID normalization. Order matters -- path neutralization
# first, since a fixture path could in principle contain digit runs that the
# timestamp patterns would otherwise be tempted to match.
prep() {
  local home="$1" proj="$2" content="$3"
  printf '%s' "$content" | neutralize_paths "$home" "$proj" | neutralize_path_hash "$proj" | normalize_text
}

# run_corpus_entry <case_file> <target_a> <target_b> <label>
# The two targets are the same launcher for --target self-comparison and
# different ones for --compare; everything downstream is identical either way.
run_corpus_entry() {
  local case_file="$1" target_a="$2" target_b="$3" label="$4"
  local root_a root_b proj_a home_a stub_a proj_b home_b stub_b
  local out_a err_a exit_a argv_a out_b err_b exit_b argv_b
  local baseline_a baseline_b post_a post_b
  local fails=0

  reset_case_defaults
  # shellcheck source=/dev/null
  source "$case_file"

  make_fixture "$project_template"
  root_a="$FIXTURE_ROOT" proj_a="$FIXTURE_PROJ" home_a="$FIXTURE_HOME" stub_a="$FIXTURE_STUB"
  make_fixture "$project_template"
  root_b="$FIXTURE_ROOT" proj_b="$FIXTURE_PROJ" home_b="$FIXTURE_HOME" stub_b="$FIXTURE_STUB"

  baseline_a="$(capture_manifest "$home_a" HOME; capture_manifest "$proj_a" PROJ)"
  baseline_b="$(capture_manifest "$home_b" HOME; capture_manifest "$proj_b" PROJ)"

  out_a="$(mktemp)"; err_a="$(mktemp)"; exit_a="$(mktemp)"; argv_a="$(mktemp)"
  out_b="$(mktemp)"; err_b="$(mktemp)"; exit_b="$(mktemp)"; argv_b="$(mktemp)"

  run_side "$target_a" "$proj_a" "$home_a" "$stub_a" "$out_a" "$err_a" "$exit_a" "$argv_a"
  run_side "$target_b" "$proj_b" "$home_b" "$stub_b" "$out_b" "$err_b" "$exit_b" "$argv_b"

  post_a="$(capture_manifest "$home_a" HOME; capture_manifest "$proj_a" PROJ)"
  post_b="$(capture_manifest "$home_b" HOME; capture_manifest "$proj_b" PROJ)"

  echo "== ${label}: $(basename "$case_file") -- ${CASE_DESCRIPTION} =="

  if side_is_empty "$out_a" "$err_a" "$exit_a" "$argv_a" "$baseline_a" "$post_a" \
    || side_is_empty "$out_b" "$err_b" "$exit_b" "$argv_b" "$baseline_b" "$post_b"; then
    echo "  HARNESS ERROR: produced no observable result at all (empty stdout/stderr, exit 0, no runtime args, unchanged filesystem)"
    echo "                 this corpus entry didn't reach the behavior it claims to exercise"
    total_harness_errors=$((total_harness_errors + 1))
    fails=1
  else
    compare_axis "runtime arguments" \
      "$(prep "$home_a" "$proj_a" "$(cat "$argv_a")")" \
      "$(prep "$home_b" "$proj_b" "$(cat "$argv_b")")" || fails=1
    compare_axis "standard output" \
      "$(prep "$home_a" "$proj_a" "$(cat "$out_a")")" \
      "$(prep "$home_b" "$proj_b" "$(cat "$out_b")")" || fails=1
    compare_axis "standard error" \
      "$(prep "$home_a" "$proj_a" "$(cat "$err_a")")" \
      "$(prep "$home_b" "$proj_b" "$(cat "$err_b")")" || fails=1
    compare_axis "exit status" "$(cat "$exit_a")" "$(cat "$exit_b")" || fails=1
    compare_axis "filesystem manifest" \
      "$(prep "$home_a" "$proj_a" "$post_a")" \
      "$(prep "$home_b" "$proj_b" "$post_b")" || fails=1

    if [[ "$CASE_EXPECT_RUNTIME_ARGS" -eq 1 && ! -s "$argv_a" ]]; then
      echo "  HARNESS ERROR: case declares CASE_EXPECT_RUNTIME_ARGS=1 but no runtime arguments were captured"
      total_harness_errors=$((total_harness_errors + 1))
      fails=1
    fi
  fi

  if [[ $fails -eq 0 ]]; then
    echo "  PASS"
  else
    echo "  FAIL"
    total_failed=$((total_failed + 1))
  fi
  total_entries=$((total_entries + 1))

  rm -rf "$root_a" "$root_b"
  rm -f "$out_a" "$err_a" "$exit_a" "$argv_a" "$out_b" "$err_b" "$exit_b" "$argv_b"
}

shopt -s nullglob
all_case_files=("${corpus_dir}"/*.case)
shopt -u nullglob

if [[ ${#all_case_files[@]} -eq 0 ]]; then
  echo "error: no corpus entries found in ${corpus_dir}" >&2
  exit 2
fi

# --case restricts the corpus by basename glob. Without it, everything runs.
case_files=()
if [[ ${#case_globs[@]} -eq 0 ]]; then
  case_files=("${all_case_files[@]}")
else
  for case_file in "${all_case_files[@]}"; do
    for glob in "${case_globs[@]}"; do
      # shellcheck disable=SC2053
      if [[ "$(basename "$case_file")" == $glob ]]; then
        case_files+=("$case_file")
        break
      fi
    done
  done
  if [[ ${#case_files[@]} -eq 0 ]]; then
    echo "error: no corpus entries matched: ${case_globs[*]}" >&2
    exit 2
  fi
fi

# require_target <name>; both modes resolve targets relative to the repo root.
require_target() {
  if [[ ! -x "${repo_root}/${1}" ]]; then
    echo "error: target not found or not executable: ${repo_root}/${1}" >&2
    exit 2
  fi
}

comparison_count=0

for target in "${targets[@]+"${targets[@]}"}"; do
  require_target "$target"
  comparison_count=$((comparison_count + 1))
  for case_file in "${case_files[@]}"; do
    run_corpus_entry "$case_file" "$target" "$target" "$target"
  done
done

for pair in "${compares[@]+"${compares[@]}"}"; do
  if [[ "$pair" != *:* ]]; then
    echo "error: --compare expects REF:CANDIDATE, got: ${pair}" >&2
    exit 2
  fi
  ref="${pair%%:*}"
  candidate="${pair#*:}"
  require_target "$ref"
  require_target "$candidate"
  comparison_count=$((comparison_count + 1))
  for case_file in "${case_files[@]}"; do
    run_corpus_entry "$case_file" "$ref" "$candidate" "${ref} vs ${candidate}"
  done
done

rm -rf "$project_template"

echo
echo "${total_entries} corpus entr$([[ $total_entries -eq 1 ]] && echo y || echo ies) run across ${comparison_count} comparison(s)."
if [[ $total_failed -gt 0 || $total_harness_errors -gt 0 ]]; then
  echo "${total_failed} failed, ${total_harness_errors} harness error(s)."
  exit 1
fi
echo "All differential harness entries passed."
