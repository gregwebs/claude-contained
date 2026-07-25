#!/usr/bin/env bash
#
# Tests for image/srt-settings.sh, which generates the per-run srt policy.
#
# Two properties matter most here and are easy to regress:
#
#   1. Every writable mount must appear in filesystem.allowWrite. srt denies
#      writes by default, and on Linux it matches paths LITERALLY (no globs), so
#      a missing entry silently breaks the tool rather than failing loudly.
#   2. The generated file must not be writable by the sandboxed user. An
#      allowlist the agent can rewrite is not a control at all.
#
# The script only needs bash and jq, so no container runtime is required. The
# chown to root is expected to fail when not running as root; the script tolerates
# that, and the mode assertion below is the part that must hold either way.
#
# Usage: tests/srt-settings.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"
gen="${repo_root}/image/srt-settings.sh"

fails=0
_check() { # _check "description" <rc-that-should-be-0>
  if [[ "$2" -eq 0 ]]; then
    echo "  PASS: $1"
  else
    echo "  FAIL: $1"
    fails=$((fails + 1))
  fi
}

work="$(mktemp -d)"
home="${work}/home"
out="${work}/settings.json"
mkdir -p "${home}/.claude-contained"

gen_settings() { # gen_settings [env assignments...]
  env HOST_HOME="$home" SRT_SETTINGS_PATH="$out" "$@" bash "$gen"
}

echo "== policy generation =="

# 1. Project directories arrive via GIT_PROTECT_DIRS (colon-separated) and must
#    all become writable, or the agent cannot edit the code it was pointed at.
gen_settings GIT_PROTECT_DIRS="/proj/alpha:/proj/beta" >/dev/null 2>&1
jq -e '.filesystem.allowWrite | index("/proj/alpha") and index("/proj/beta")' "$out" >/dev/null
_check "project dirs from GIT_PROTECT_DIRS are writable" $?

# 2. Tool config dirs are derived from HOST_HOME rather than passed in.
jq -e --arg h "$home" '
  .filesystem.allowWrite as $w
  | ($w | index($h + "/.claude"))
    and ($w | index($h + "/.codex"))
    and ($w | index($h + "/.claude-contained"))
    and ($w | index("/tmp"))
' "$out" >/dev/null
_check "tool config dirs and /tmp are writable" $?

# 3. Required inside a container: bwrap cannot create privileged namespaces there.
jq -e '.enableWeakerNestedSandbox == true' "$out" >/dev/null
_check "enableWeakerNestedSandbox is set" $?

# 4. Published ports (-p), the -H socat forwards and local dev servers all bind
#    inside the container.
jq -e '.network.allowLocalBinding == true' "$out" >/dev/null
_check "allowLocalBinding is on" $?

# 5. Without an allowlist the agent cannot reach its own API, so defaults ship.
jq -e '.network.allowedDomains | index("api.anthropic.com")' "$out" >/dev/null
_check "default domain set includes the Anthropic API" $?

echo "== merge precedence =="

# 6. --allow-host additions are unioned in.
gen_settings GIT_PROTECT_DIRS="/p" SRT_ALLOW_HOSTS="one.example,two.example" >/dev/null 2>&1
jq -e '.network.allowedDomains | index("one.example") and index("two.example")' "$out" >/dev/null
_check "SRT_ALLOW_HOSTS entries are added" $?

# 7. The user file supplies persistent policy and must survive alongside both the
#    defaults and the per-run flag additions.
cat > "${home}/.claude-contained/srt-settings.json" <<'EOF'
{
  "network": {
    "allowedDomains": ["corp.example"],
    "tlsTerminate": { "excludeDomains": ["pinned.example"] }
  },
  "ignoreViolations": { "some-rule": ["path"] }
}
EOF
gen_settings GIT_PROTECT_DIRS="/p" SRT_ALLOW_HOSTS="flag.example" >/dev/null 2>&1
jq -e '
  (.network.allowedDomains | index("corp.example"))
  and (.network.allowedDomains | index("flag.example"))
  and (.network.allowedDomains | index("api.anthropic.com"))
' "$out" >/dev/null
_check "user file, flag, and defaults all merge" $?

# 8. Keys this script knows nothing about must survive, so the user can set srt
#    options without the generator needing to learn each one.
jq -e '
  (.network.tlsTerminate.excludeDomains | index("pinned.example"))
  and (.ignoreViolations["some-rule"] | index("path"))
' "$out" >/dev/null
_check "unknown user keys are carried through" $?

# 9. A broken settings file must not brick every run -- the container would be
#    unusable and the escape hatch hard to discover.
echo 'this is not json {{{' > "${home}/.claude-contained/srt-settings.json"
gen_settings GIT_PROTECT_DIRS="/p" >/dev/null 2>&1
rc=$?
[[ $rc -eq 0 ]] && jq -e '.network.allowedDomains | length > 0' "$out" >/dev/null
_check "malformed user file falls back to defaults instead of failing" $?
rm -f "${home}/.claude-contained/srt-settings.json"

echo "== ssh agent =="

# 10. Only opened when the launcher actually forwarded an agent socket.
gen_settings GIT_PROTECT_DIRS="/p" SSH_AUTH_SOCK=/ssh-agent >/dev/null 2>&1
jq -e '.network.allowUnixSockets | index("/ssh-agent")' "$out" >/dev/null
_check "ssh agent socket allowed when SSH_AUTH_SOCK is set" $?

gen_settings GIT_PROTECT_DIRS="/p" >/dev/null 2>&1
jq -e '.network.allowUnixSockets | length == 0' "$out" >/dev/null
_check "no unix sockets allowed without SSH_AUTH_SOCK" $?

echo "== tamper resistance =="

# 11. The sandboxed process must be able to read its policy and unable to edit it.
[[ "$(stat -c '%a' "$out" 2>/dev/null || stat -f '%Lp' "$out")" == "444" ]]
_check "generated policy is mode 444" $?

rm -rf "$work"

if [[ "$fails" -gt 0 ]]; then
  echo
  echo "${fails} srt-settings test(s) failed."
  exit 1
fi

echo
echo "All srt-settings tests passed."
