#!/usr/bin/env bash
#
# Unit tests for the egress allowlist merge logic: default_allow_hosts_for_tool()
# and merge_allow_hosts(). These are pure functions (no container involved),
# testable by sourcing the launcher with CLAUDE_CONTAINED_LIB_ONLY=1.
#
# The nftables/dnsmasq/SNI-proxy enforcement itself (image/egress-firewall.sh,
# image/sni-proxy.py) was verified end-to-end in a real container during
# development (see the plan/ADR); it isn't re-verified here since it needs an
# actual container runtime, not just the sourced shell functions.
#
# Usage: tests/egress-allowlist.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root_dir="$(dirname "$here")"

suite() {
  set +e
  local fails=0

  _check() { # _check "description" <rc-that-should-be-0>
    if [[ "$2" -eq 0 ]]; then
      echo "  PASS: $1"
    else
      echo "  FAIL: $1"
      fails=$((fails + 1))
    fi
  }

  _eq() { # _eq "description" "actual" "expected"
    if [[ "$2" == "$3" ]]; then
      echo "  PASS: $1"
    else
      echo "  FAIL: $1 (got '$2', want '$3')"
      fails=$((fails + 1))
    fi
  }

  # --- default_allow_hosts_for_tool: known + unknown tool ---
  _eq "claude base list" "$(default_allow_hosts_for_tool claude)" "api.anthropic.com,console.anthropic.com"
  _eq "unknown tool has no base list" "$(default_allow_hosts_for_tool bogus-tool)" ""

  # --- merge_allow_hosts: base list only (no flags, no env) ---
  tool="claude"; no_default_hosts=0; allow_hosts=(); unset CLAUDE_ALLOW_HOSTS
  merge_allow_hosts
  _eq "base list only" "${merged_allow_hosts[*]}" "api.anthropic.com console.anthropic.com"

  # --- --no-default-hosts suppresses the base list ---
  tool="claude"; no_default_hosts=1; allow_hosts=(); unset CLAUDE_ALLOW_HOSTS
  merge_allow_hosts
  _eq "--no-default-hosts with nothing else yields empty" "${merged_allow_hosts[*]}" ""

  # --- explicit --allow-host appends to (doesn't replace) the base list ---
  tool="claude"; no_default_hosts=0; allow_hosts=(github.com); unset CLAUDE_ALLOW_HOSTS
  merge_allow_hosts
  _eq "explicit flag adds to base list" "${merged_allow_hosts[*]}" "api.anthropic.com console.anthropic.com github.com"

  # --- CLAUDE_ALLOW_HOSTS is used only as a default when no flag was given ---
  tool="claude"; no_default_hosts=0; allow_hosts=()
  export CLAUDE_ALLOW_HOSTS="github.com,api.github.com"
  merge_allow_hosts
  _eq "env var used when no --allow-host flag given" "${merged_allow_hosts[*]}" "api.anthropic.com console.anthropic.com github.com api.github.com"

  # --- an explicit --allow-host flag wins over CLAUDE_ALLOW_HOSTS entirely (not merged) ---
  tool="claude"; no_default_hosts=0; allow_hosts=(only-flag.example.com)
  export CLAUDE_ALLOW_HOSTS="should-be-ignored.example.com"
  merge_allow_hosts
  _eq "flag replaces (not merges with) env var" "${merged_allow_hosts[*]}" "api.anthropic.com console.anthropic.com only-flag.example.com"
  unset CLAUDE_ALLOW_HOSTS

  # --- de-duplication across base list + explicit entries ---
  tool="claude"; no_default_hosts=0; allow_hosts=(api.anthropic.com github.com github.com)
  merge_allow_hosts
  _eq "duplicates collapsed, first occurrence order kept" "${merged_allow_hosts[*]}" "api.anthropic.com console.anthropic.com github.com"

  # --- codex base list (different tool) ---
  tool="codex"; no_default_hosts=0; allow_hosts=()
  merge_allow_hosts
  _eq "codex base list" "${merged_allow_hosts[*]}" "api.openai.com auth.openai.com chatgpt.com"

  return "$fails"
}

total_fail=0
for target in claude-contained claude-docked; do
  echo "== ${target} =="
  (
    export CLAUDE_CONTAINED_LIB_ONLY=1
    # shellcheck disable=SC1090
    source "${repo_root_dir}/${target}" . >/dev/null 2>&1
    set +e
    suite "$target"
  )
  total_fail=$((total_fail + $?))
done

echo
if [[ $total_fail -ne 0 ]]; then
  echo "FAILED: ${total_fail} assertion(s) failed."
  exit 1
fi
echo "All egress-allowlist tests passed."
