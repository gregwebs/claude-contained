#!/usr/bin/env bash
#
# Tests for launcher DNS defaulting and overrides.
#
# Apple Containers frequently starts containers with an unreachable vmnet DNS
# gateway, so claude-contained supplies a public resolver by default. Docker
# keeps its runtime default unless CLAUDE_DNS or --dns says otherwise.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(dirname "$here")"

fails=0
_check() { # _check "description" <rc-that-should-be-0>
  if [[ "$2" -eq 0 ]]; then
    echo "  PASS: $1"
  else
    echo "  FAIL: $1"
    fails=$((fails + 1))
  fi
}

line_has() { grep -Fxq -- "$2" <<<"$1"; }
line_count() { grep -Fxc -- "$2" <<<"$1" || true; }

launcher_argv() { # launcher_argv <target> [flags...]
  local target="$1"; shift
  HOME="$home" PATH="${stub_dir}:$PATH" "${repo_root}/${target}" "$@" -N -s -C "$proj" 2>/dev/null
}

stub_dir="$(mktemp -d)"
proj="$(mktemp -d)"
home="$(mktemp -d)"
for rt in container docker; do
  printf '#!/bin/bash\nprintf "%%s\\n" "$@"\n' > "${stub_dir}/${rt}"
  chmod +x "${stub_dir}/${rt}"
done

echo "== claude-contained DNS =="

out="$(
  unset CLAUDE_DNS
  launcher_argv claude-contained
)"
line_has "$out" "--dns" && line_has "$out" "1.1.1.1"
_check "Apple Containers defaults to a stable resolver" $?

out="$(
  export CLAUDE_DNS=system
  launcher_argv claude-contained
)"
[[ "$(line_count "$out" "--dns")" -eq 0 ]]
_check "CLAUDE_DNS=system opts claude-contained back into runtime DNS" $?

out="$(
  export CLAUDE_DNS=9.9.9.9,8.8.8.8
  launcher_argv claude-contained
)"
[[ "$(line_count "$out" "--dns")" -eq 2 ]] && line_has "$out" "9.9.9.9" && line_has "$out" "8.8.8.8"
_check "CLAUDE_DNS list is expanded for claude-contained" $?

out="$(
  unset CLAUDE_DNS
  launcher_argv claude-contained --dns 4.4.4.4
)"
[[ "$(line_count "$out" "--dns")" -eq 1 ]] && line_has "$out" "4.4.4.4" && ! line_has "$out" "1.1.1.1"
_check "explicit --dns overrides claude-contained default" $?

echo "== claude-docked DNS =="

out="$(
  unset CLAUDE_DNS
  launcher_argv claude-docked
)"
[[ "$(line_count "$out" "--dns")" -eq 0 ]]
_check "Docker keeps runtime DNS by default" $?

out="$(
  export CLAUDE_DNS=9.9.9.9,8.8.8.8
  launcher_argv claude-docked
)"
[[ "$(line_count "$out" "--dns")" -eq 2 ]] && line_has "$out" "9.9.9.9" && line_has "$out" "8.8.8.8"
_check "CLAUDE_DNS list is expanded for claude-docked" $?

out="$(
  export CLAUDE_DNS=system
  launcher_argv claude-docked
)"
[[ "$(line_count "$out" "--dns")" -eq 0 ]]
_check "CLAUDE_DNS=system keeps Docker runtime DNS" $?

out="$(
  export CLAUDE_DNS=9.9.9.9
  launcher_argv claude-docked --dns 4.4.4.4
)"
[[ "$(line_count "$out" "--dns")" -eq 1 ]] && line_has "$out" "4.4.4.4" && ! line_has "$out" "9.9.9.9"
_check "explicit --dns overrides CLAUDE_DNS" $?

rm -rf "$stub_dir" "$proj" "$home"

if [[ "$fails" -gt 0 ]]; then
  echo
  echo "${fails} DNS flag test(s) failed."
  exit 1
fi

echo
echo "All DNS flag tests passed."
