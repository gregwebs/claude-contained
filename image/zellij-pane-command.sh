#!/usr/bin/env bash
set -euo pipefail

restore_xdg_var() {
  local name="$1"
  local value_var="CLAUDE_CONTAINED_PRE_ZELLIJ_${name}"
  local set_var="${value_var}_SET"

  if [[ "${!set_var:-0}" == "1" ]]; then
    export "${name}=${!value_var}"
  else
    unset "$name"
  fi

  unset "$value_var" "$set_var"
}

restore_xdg_var XDG_CACHE_HOME
restore_xdg_var XDG_DATA_HOME
restore_xdg_var XDG_RUNTIME_DIR
restore_xdg_var TMPDIR

unset CLAUDE_CONTAINED_ZELLIJ_CONFIG
unset CLAUDE_CONTAINED_ZELLIJ_DATA_DIR
unset CLAUDE_CONTAINED_ZELLIJ_CACHE_DIR
unset CLAUDE_CONTAINED_ZELLIJ_RUNTIME_DIR
unset CLAUDE_CONTAINED_ZELLIJ_SERVER
unset CLAUDE_CONTAINED_ZELLIJ_SOCKET
unset CLAUDE_CONTAINED_ZELLIJ_LAYOUT_DIR
unset CLAUDE_CONTAINED_ZELLIJ_TMP_DIR

if [[ $# -eq 0 ]]; then
  set -- bash
fi

exec "$@"
