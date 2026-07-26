#!/usr/bin/env bash
set -euo pipefail

session="${1:-}"
if [[ -z "$session" ]]; then
  echo "zellij-attach: missing session name" >&2
  exit 2
fi

config_file="/etc/claude-contained/zellij/config.kdl"
zellij_root="${HOME}/.claude-contained/zellij"
data_dir="${zellij_root}/data"
cache_dir="${zellij_root}/cache"
runtime_dir="/tmp/claude-contained-zellij-runtime"
tmp_dir="/tmp/zellij-$(id -u)"
log_dir="${tmp_dir}/zellij-log"
socket_dir="${runtime_dir}/zellij/contract_version_1"
session_socket="${socket_dir}/${session}"

export CLAUDE_CONTAINED_ZELLIJ_CONFIG="$config_file"
export CLAUDE_CONTAINED_ZELLIJ_DATA_DIR="$data_dir"
export CLAUDE_CONTAINED_ZELLIJ_CACHE_DIR="$cache_dir"
export CLAUDE_CONTAINED_ZELLIJ_RUNTIME_DIR="$runtime_dir"
export CLAUDE_CONTAINED_ZELLIJ_SOCKET="$session_socket"
export CLAUDE_CONTAINED_ZELLIJ_TMP_DIR="$tmp_dir"
export XDG_DATA_HOME="$data_dir"
export XDG_CACHE_HOME="$cache_dir"
export XDG_RUNTIME_DIR="$runtime_dir"
export TMPDIR="/tmp"

mkdir -p \
  "$data_dir" \
  "$cache_dir" \
  "${cache_dir}/org/Zellij-Contributors/Zellij" \
  "$runtime_dir" \
  "$socket_dir" \
  "$log_dir"
chmod 700 "$runtime_dir" "${runtime_dir}/zellij" "$socket_dir" "$tmp_dir" "$log_dir" 2>/dev/null || true

zellij_cmd=(zellij --config "$config_file" --data-dir "$data_dir")

zellij_session_is_live() {
  local line first

  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    first="${line%% *}"
    if [[ "$first" == "$session" && "$line" != *"(EXITED"* ]]; then
      return 0
    fi
  done < <("${zellij_cmd[@]}" list-sessions --no-formatting 2>/dev/null || true)
  return 1
}

if ! zellij_session_is_live; then
  echo "zellij-attach: no live Zellij session named ${session}" >&2
  exit 1
fi

exec "${zellij_cmd[@]}" attach "$session"
