#!/usr/bin/env bash
# Restore the tool environment before executing a generated Zellij pane command.
set -euo pipefail

restore_pre_zellij_var() {
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

prepend_path_entry() {
  local entry="$1"
  [[ -z "$entry" ]] && return
  case ":${PATH:-}:" in
    *":${entry}:"*) ;;
    *) PATH="${entry}${PATH:+:${PATH}}" ;;
  esac
}

restore_pre_zellij_var XDG_CACHE_HOME
restore_pre_zellij_var XDG_DATA_HOME
restore_pre_zellij_var XDG_RUNTIME_DIR
restore_pre_zellij_var TMPDIR
restore_pre_zellij_var PATH
restore_pre_zellij_var SHELL

if [[ -z "${PATH:-}" ]]; then
  PATH="/usr/local/bin:/usr/bin:/bin"
fi
prepend_path_entry "${JAVA_HOME:-/opt/jbr}/bin"
prepend_path_entry "/home/dev/.sdkman/candidates/jbang/current/bin"
prepend_path_entry "/home/dev/.sdkman/candidates/maven/current/bin"
prepend_path_entry "/opt/claude"
prepend_path_entry "${HOME:-/home/dev}/.local/bin"
export PATH

if [[ -z "${SHELL:-}" ]]; then
  export SHELL=/bin/bash
fi

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
