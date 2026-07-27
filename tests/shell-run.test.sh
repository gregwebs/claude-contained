#!/usr/bin/env bash
#
# Tests for the shell-run helper that gives debug shells a fresh PTY when the
# contained runtime leaves bash without a controlling terminal.
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

work="$(mktemp -d)"
stub_dir="${work}/bin"
log="${work}/script.log"
mkdir -p "$stub_dir"

cat > "${stub_dir}/script" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@" > "${SHELL_RUN_STUB_LOG}"
exit 43
EOF
chmod +x "${stub_dir}/script"

echo "== shell-run =="

PATH="${stub_dir}:$PATH" \
  CLAUDE_CONTAINED_SHELL_RUN_FORCE_SCRIPT=1 \
  SHELL_RUN_STUB_LOG="$log" \
  "${repo_root}/image/shell-run.sh" >/dev/null 2>&1
rc=$?
[[ $rc -eq 43 ]] && paste -sd ' ' "$log" | grep -Fxq -- "-qfec exec /usr/bin/env bash /dev/null"
_check "uses util-linux script for forced PTY shell startup" $?

out="$(PATH="${stub_dir}:$PATH" "${repo_root}/image/shell-run.sh" -c 'printf fallback' 2>/dev/null)"
rc=$?
[[ $rc -eq 0 && "$out" == "fallback" ]]
_check "falls back to bash when stdin/stdout are not TTYs" $?

rm -rf "$work"

if [[ "$fails" -gt 0 ]]; then
  echo
  echo "${fails} shell-run test(s) failed."
  exit 1
fi

echo
echo "All shell-run tests passed."
