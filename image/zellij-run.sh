#!/usr/bin/env bash
set -euo pipefail

session="${1:-}"
if [[ -z "$session" ]]; then
  echo "zellij-run: missing session name" >&2
  exit 2
fi
if [[ "$session" == -* || ! "$session" =~ ^[A-Za-z0-9_.-]+$ ]]; then
  echo "zellij-run: invalid session name: $session" >&2
  exit 2
fi
shift
if [[ "${1:-}" == "--" ]]; then
  shift
fi
if [[ $# -eq 0 ]]; then
  set -- bash
fi

remember_pre_zellij_var() {
  local name="$1"
  local value_var="CLAUDE_CONTAINED_PRE_ZELLIJ_${name}"
  local set_var="${value_var}_SET"

  if [[ "${!name+x}" == "x" ]]; then
    export "${set_var}=1"
    export "${value_var}=${!name}"
  else
    export "${set_var}=0"
    unset "$value_var"
  fi
}

kdl_quote() {
  local s="$1"
  s="${s//\\/\\\\}"
  s="${s//\"/\\\"}"
  s="${s//$'\n'/\\n}"
  s="${s//$'\r'/\\r}"
  s="${s//$'\t'/\\t}"
  printf '"%s"' "$s"
}

if [[ -z "${SHELL:-}" ]]; then
  export SHELL=/bin/bash
fi

remember_pre_zellij_var XDG_CACHE_HOME
remember_pre_zellij_var XDG_DATA_HOME
remember_pre_zellij_var XDG_RUNTIME_DIR
remember_pre_zellij_var TMPDIR
remember_pre_zellij_var PATH
remember_pre_zellij_var SHELL

config_file="/etc/claude-contained/zellij/config.kdl"
zellij_root="${HOME}/.claude-contained/zellij"
data_dir="${zellij_root}/data"
cache_dir="${zellij_root}/cache"
runtime_dir="/tmp/claude-contained-zellij-runtime"
tmp_dir="/tmp/zellij-$(id -u)"
log_dir="${tmp_dir}/zellij-log"
socket_dir="${runtime_dir}/zellij/contract_version_1"
session_socket="${socket_dir}/${session}"
layout_dir="${runtime_dir}/layouts"
layout_name="$session"
layout_file="${layout_dir}/${layout_name}.kdl"

export CLAUDE_CONTAINED_ZELLIJ_CONFIG="$config_file"
export CLAUDE_CONTAINED_ZELLIJ_DATA_DIR="$data_dir"
export CLAUDE_CONTAINED_ZELLIJ_CACHE_DIR="$cache_dir"
export CLAUDE_CONTAINED_ZELLIJ_RUNTIME_DIR="$runtime_dir"
export CLAUDE_CONTAINED_ZELLIJ_SOCKET="$session_socket"
export CLAUDE_CONTAINED_ZELLIJ_LAYOUT_DIR="$layout_dir"
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
  "$layout_dir" \
  "$log_dir"
chmod 700 "$runtime_dir" "${runtime_dir}/zellij" "$socket_dir" "$layout_dir" "$tmp_dir" "$log_dir" 2>/dev/null || true

tmp_layout="${layout_file}.$$"
{
  printf 'layout {\n'
  printf '    pane command="/usr/local/bin/zellij-pane-command" {\n'
  printf '        args'
  for arg in "$@"; do
    printf ' '
    kdl_quote "$arg"
  done
  printf '\n'
  printf '    }\n'
  printf '}\n'
} > "$tmp_layout"
mv -f "$tmp_layout" "$layout_file"

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

forget_saved_session_state() {
  rm -rf -- \
    "${cache_dir}/zellij/contract_version_1/session_info/${session}" \
    "${data_dir}/zellij/contract_version_1/session_info/${session}"
}

if zellij_session_is_live; then
  echo "zellij-run: session ${session} is already live" >&2
  exit 1
fi

forget_saved_session_state

set +e
"${zellij_cmd[@]}" attach --forget --create "$session" options --default-layout "$layout_name"
zellij_status=$?
set -e

while zellij_session_is_live; do
  sleep "${CLAUDE_CONTAINED_ZELLIJ_WAIT_SECONDS:-2}"
done

exit "$zellij_status"
