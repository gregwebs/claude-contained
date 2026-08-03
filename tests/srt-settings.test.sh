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
  env -u SSH_AUTH_SOCK HOST_HOME="$home" SRT_SETTINGS_PATH="$out" "$@" bash "$gen"
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
    and ($w | index($h + "/.claude-contained/claude"))
    and ($w | index($h + "/.codex"))
    and ($w | index($h + "/.claude-contained"))
    and ($w | index("/tmp"))
' "$out" >/dev/null
_check "tool config dirs, contained Claude profile, and /tmp are writable" $?

jq -e --arg h "$home" '
  .filesystem.allowWrite as $w
  | ($w | index($h + "/.claude-contained"))
    and (($w | index($h + "/.m2")) | not)
    and (($w | index($h + "/.vaadin")) | not)
' "$out" >/dev/null
_check "shared state covers layer caches without Maven or Vaadin paths" $?

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

# 5b. Regression pin: /login broke under the sandbox because the OAuth code
#     exchange goes to platform.claude.com, not api.anthropic.com -- srt's proxy
#     closed the connection, surfacing as "OAuth error: Socket is closed" rather
#     than a clear network error. See image/srt-settings.sh for the citation.
jq -e '.network.allowedDomains | index("platform.claude.com") and index("claude.ai")' "$out" >/dev/null
_check "default domain set includes Claude Code's OAuth hosts" $?

# 6. srt requires these arrays even when they are empty.
jq -e '
  (.network.deniedDomains | type == "array")
  and (.filesystem.denyRead | type == "array")
  and (.filesystem.denyWrite | type == "array")
' "$out" >/dev/null
_check "required deny lists are present" $?

echo "== merge precedence =="

# 7. --allow-host additions are unioned in.
gen_settings GIT_PROTECT_DIRS="/p" SRT_ALLOW_HOSTS="one.example,two.example" >/dev/null 2>&1
jq -e '.network.allowedDomains | index("one.example") and index("two.example")' "$out" >/dev/null
_check "SRT_ALLOW_HOSTS entries are added" $?

# 8. The user file supplies persistent policy and must survive alongside both the
#    defaults and the per-run flag additions.
cat > "${home}/.claude-contained/srt-settings.json" <<'EOF'
{
  "network": {
    "allowedDomains": ["corp.example"],
    "deniedDomains": ["blocked.example"],
    "tlsTerminate": { "excludeDomains": ["pinned.example"] }
  },
  "filesystem": {
    "denyRead": ["/secret/read"],
    "denyWrite": ["/secret/write"]
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

# 9. User deny lists must survive alongside generated allow lists.
jq -e '
  (.network.deniedDomains | index("blocked.example"))
  and (.filesystem.denyRead | index("/secret/read"))
  and (.filesystem.denyWrite | index("/secret/write"))
' "$out" >/dev/null
_check "user deny lists are carried through" $?

# 10. Keys this script knows nothing about must survive, so the user can set srt
#    options without the generator needing to learn each one.
jq -e '
  (.network.tlsTerminate.excludeDomains | index("pinned.example"))
  and (.ignoreViolations["some-rule"] | index("path"))
' "$out" >/dev/null
_check "unknown user keys are carried through" $?

# 11. A broken settings file must fail closed instead of silently dropping a
#    user's tighter persistent policy.
echo 'this is not json {{{' > "${home}/.claude-contained/srt-settings.json"
rm -f "$out"
gen_settings GIT_PROTECT_DIRS="/p" >/dev/null 2>&1
rc=$?
if [[ $rc -ne 0 && ! -e "$out" ]]; then check_rc=0; else check_rc=1; fi
_check "malformed user file fails closed" "$check_rc"
rm -f "${home}/.claude-contained/srt-settings.json"

echo "== ssh agent =="

# 12. Only opened when the launcher actually forwarded an agent socket.
gen_settings GIT_PROTECT_DIRS="/p" SSH_AUTH_SOCK=/ssh-agent >/dev/null 2>&1
jq -e '.network.allowUnixSockets | index("/ssh-agent")' "$out" >/dev/null
_check "ssh agent socket allowed when SSH_AUTH_SOCK is set" $?

gen_settings GIT_PROTECT_DIRS="/p" >/dev/null 2>&1
jq -e '.network.allowUnixSockets | length == 0' "$out" >/dev/null
_check "no unix sockets allowed without SSH_AUTH_SOCK" $?

gen_settings GIT_PROTECT_DIRS="/p" HOST_UID=1234 CLAUDE_CONTAINED_ZELLIJ=1 CLAUDE_CONTAINED_ZELLIJ_SESSION=cc-test >/dev/null 2>&1
jq -e '.network.allowUnixSockets | index("/tmp/claude-contained-zellij-runtime/zellij/contract_version_1/cc-test")' "$out" >/dev/null
_check "Zellij session socket is allowed only for marked Zellij runs" $?
jq -e '.network.allowAllUnixSockets == true' "$out" >/dev/null
_check "Zellij enables Linux Unix-socket support under srt" $?
jq -e --arg h "$home" '
  .filesystem.allowWrite as $w
  | ($w | index($h + "/.claude-contained/zellij/data"))
    and ($w | index($h + "/.claude-contained/zellij/cache/org/Zellij-Contributors/Zellij"))
    and ($w | index("/tmp/claude-contained-zellij-runtime"))
    and ($w | index("/tmp/claude-contained-zellij-runtime/zellij/contract_version_1"))
    and ($w | index("/tmp/claude-contained-zellij-runtime/zellij/contract_version_1/cc-test"))
    and ($w | index("/tmp/claude-contained-zellij-runtime/layouts/cc-test.kdl"))
    and ($w | index("/tmp/zellij-1234/zellij-log/zellij.log"))
' "$out" >/dev/null
_check "Zellij literal runtime, cache, and log paths are writable under srt" $?

echo "== tamper resistance =="

# 13. The sandboxed process must be able to read its policy and unable to edit it.
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
