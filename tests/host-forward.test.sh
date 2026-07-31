#!/usr/bin/env bash
#
# Tests for image/host-forward.sh, which relays host ports into the container's
# localhost for MCP clients that insist on 127.0.0.1.
#
# The property that matters is embarrassingly small and was broken in production
# for months: socat takes **two** address arguments, and a quoting change once
# collapsed them into one. socat then exits "exactly 2 addresses required" and
# forwards nothing, silently -- the relays are backgrounded, so nobody sees the
# error. `-H` appeared to work and did not, on both container runtimes.
#
# Every assertion here therefore inspects socat's *argument vector*, not just
# whether the script ran. Shell linters will keep suggesting the collapsed form;
# this test is the reason to refuse.
#
# The script only needs bash and a socat on PATH, so no container runtime is
# required.
#
# Usage: tests/host-forward.test.sh
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"
fwd="${repo_root}/image/host-forward.sh"

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
stub="${work}/bin"
log="${work}/socat.log"
mkdir -p "$stub"

# The stub records one line per invocation: the argument count, then each argument
# separated by a space. A collapsed "addr1 addr2" single argument shows up as
# count 1, which is exactly the regression under test.
cat > "${stub}/socat" <<'STUB'
#!/bin/bash
printf '%s|%s\n' "$#" "$*" >> "$SOCAT_LOG"
exit 0
STUB
chmod +x "${stub}/socat"

relay_count() { wc -l < "$log" | tr -d ' '; }

# forward <HOST_FORWARD_PORTS> <expected-relay-count>
#
# The relays are backgrounded on purpose -- the entrypoint must not block on them
# -- so host-forward.sh can exit before the stubs have written their log lines.
# Poll for the expected count instead of assuming it has landed, or every
# assertion below races the stub.
forward() {
  : > "$log"
  PATH="${stub}:${PATH}" SOCAT_LOG="$log" HOST_FORWARD_PORTS="$1" bash "$fwd"
  local waited=0
  while [[ "$(relay_count)" -lt "$2" ]]; do
    [[ "$waited" -ge 100 ]] && return 1
    waited=$((waited + 1))
    sleep 0.02
  done
  return 0
}

# every_relay_got_two_addresses fails on an empty log too: "no lines" must never
# pass a check about what the lines contain.
every_relay_got_two_addresses() {
  [[ -s "$log" ]] || return 1
  ! grep -qv '^2|' "$log"
}

echo "== socat argument vector =="

# 1. The headline property. Two arguments, never one.
forward "3845" 1
_check "one mapping starts one relay" $?

every_relay_got_two_addresses
_check "socat receives exactly two address arguments" $?

# 2. A bare port listens and connects on the same number.
grep -qxF '2|TCP-LISTEN:3845,fork,reuseaddr TCP:host.local:3845' "$log"
_check "a bare port maps to the same host port" $?

# 3. LOCAL:HOST splits, so a container port can differ from the host's.
forward "3845:9000" 1
grep -qxF '2|TCP-LISTEN:3845,fork,reuseaddr TCP:host.local:9000' "$log"
_check "LOCAL:HOST maps the two ports independently" $?

# 4. A comma-separated list starts one relay per mapping.
forward "3845,8080:9090" 2
_check "a comma-separated list starts one relay per mapping" $?

every_relay_got_two_addresses
_check "each relay in a list gets two addresses" $?

grep -qxF '2|TCP-LISTEN:3845,fork,reuseaddr TCP:host.local:3845' "$log" \
  && grep -qxF '2|TCP-LISTEN:8080,fork,reuseaddr TCP:host.local:9090' "$log"
_check "both mappings in a list are forwarded correctly" $?

# 5. The listener keeps fork and reuseaddr: without fork it serves one connection
#    and exits, which looks like a flaky MCP rather than a broken forward.
forward "3845" 1
grep -q 'TCP-LISTEN:3845,fork,reuseaddr' "$log"
_check "the listener keeps fork,reuseaddr" $?

echo
echo "== no forwards requested =="

# 6. Unset or empty means do nothing at all, not start a degenerate relay.
: > "$log"
PATH="${stub}:${PATH}" SOCAT_LOG="$log" bash "$fwd"
[[ ! -s "$log" ]]
_check "no HOST_FORWARD_PORTS starts no relays" $?

: > "$log"
PATH="${stub}:${PATH}" SOCAT_LOG="$log" HOST_FORWARD_PORTS="" bash "$fwd"
[[ ! -s "$log" ]]
_check "an empty HOST_FORWARD_PORTS starts no relays" $?

rm -rf "$work"

if [[ "$fails" -gt 0 ]]; then
  echo
  echo "${fails} host-forward test(s) failed."
  exit 1
fi

echo
echo "All host-forward tests passed."
