#!/usr/bin/env bash
#
# Tests for preserving child command flags when wrapping with srt.
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
mkdir -p "$stub_dir"
cat > "${stub_dir}/srt" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$@"
EOF
chmod +x "${stub_dir}/srt"

echo "== srt-run =="

out="$(PATH="${stub_dir}:$PATH" SRT_SETTINGS_PATH=/tmp/settings.json "${repo_root}/image/srt-run.sh" claude --debug api)"
printf '%s\n' "$out" | paste -sd ' ' - | grep -Fxq -- "--settings /tmp/settings.json -- claude --debug api"
_check "srt-run separates srt options from child flags" $?

echo "== entrypoint =="

grep -Fq 'set -- srt --settings "${SRT_SETTINGS_PATH:-/run/srt-settings.json}" -- "$@"' "${repo_root}/image/entrypoint.sh"
_check "entrypoint separates srt options from child flags" $?

rm -rf "$work"

if [[ "$fails" -gt 0 ]]; then
  echo
  echo "${fails} srt wrapper test(s) failed."
  exit 1
fi

echo
echo "All srt wrapper tests passed."
