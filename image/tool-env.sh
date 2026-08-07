#!/usr/bin/env bash
# Resolve the tool environment, then execute a command without evaluating layer content.
# shellcheck disable=SC2016 # HOME and PATH references are deliberately matched as literal text.
set -euo pipefail

fragment_dir=/etc/claude-contained/env.d
if [[ "${1:-}" == "--directory" ]]; then
  fragment_dir="$2"
  shift 2
fi

if [[ "${CLAUDE_CONTAINED_EXPLICIT_ENV_KEYS+set}" == set ]]; then
  initial_keys=$'\n'
  IFS=',' read -r -a explicit_keys <<<"$CLAUDE_CONTAINED_EXPLICIT_ENV_KEYS"
  for key in "${explicit_keys[@]}"; do
    if [[ ! "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      echo "tool-env: invalid explicit environment key: ${key}" >&2
      exit 2
    fi
    initial_keys+="${key}"$'\n'
  done
  unset CLAUDE_CONTAINED_EXPLICIT_ENV_KEYS
else
  # Direct derived-image consumers have no launcher marker, so their image ENV
  # is the explicit startup contract and remains authoritative over fragments.
  initial_keys=$'\n'"$(compgen -e)"$'\n'
fi
initially_set() {
  [[ "$initial_keys" == *$'\n'"$1"$'\n'* ]]
}
if [[ $# -eq 0 ]]; then
  # An empty effective CMD reaches here only from a tooling layer that sets
  # CMD [] or an old base image run by a newer launcher -- the base image's
  # own CMD is now shell-run, so the run path always supplies a non-empty
  # operand. Treat it as "no command was given" rather than a hard failure,
  # consistent with the launcher's own image-default behavior (docs/adr/0009).
  set -- /usr/local/bin/shell-run
fi

prepend_path() {
  local entry="$1"
  case ":${PATH:-}:" in
    *":${entry}:"*) ;;
    *) PATH="${entry}${PATH:+:${PATH}}" ;;
  esac
}

PATH="${PATH:-/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin}"
prepend_path /opt/claude
prepend_path "${HOME:-/home/dev}/.local/bin"
export PATH

reserved_key() {
  case "$1" in
    HOST_*|SRT_*|CLAUDE_CONTAINED_*|LD_*|STAY_ROOT|SSH_AUTH_SOCK|GIT_PROTECT_DIRS|HOME|BASH_ENV|ENV|NODE_OPTIONS)
      return 0
      ;;
  esac
  return 1
}

expand_value() {
  local rest="$1" out='' prefix tail first
  while [[ "$rest" == *'$'* ]]; do
    prefix="${rest%%'$'*}"
    out+="$prefix"
    rest="${rest#*'$'}"
    case "$rest" in
      '{HOME}'*) out+="${HOME:-}"; rest="${rest:6}" ;;
      '{PATH}'*) out+="${PATH:-}"; rest="${rest:6}" ;;
      HOME*)
        tail="${rest:4}"
        first="${tail:0:1}"
        if [[ -z "$first" || ! "$first" =~ [A-Za-z0-9_] ]]; then out+="${HOME:-}"; else out+='$HOME'; fi
        rest="$tail"
        ;;
      PATH*)
        tail="${rest:4}"
        first="${tail:0:1}"
        if [[ -z "$first" || ! "$first" =~ [A-Za-z0-9_] ]]; then out+="${PATH:-}"; else out+='$PATH'; fi
        rest="$tail"
        ;;
      *) out+='$' ;;
    esac
  done
  printf '%s%s' "$out" "$rest"
}

load_fragment() {
  local fragment="$1" raw line key value lineno=0
  while IFS= read -r raw || [[ -n "$raw" ]]; do
    lineno=$((lineno + 1))
    line="${raw%$'\r'}"
    while [[ "$line" == [[:space:]]* ]]; do line="${line:1}"; done
    [[ -z "$line" || "$line" == \#* ]] && continue
    if [[ "$line" != *=* ]]; then
      echo "tool-env: ${fragment}:${lineno}: expected KEY=VALUE" >&2
      return 2
    fi
    key="${line%%=*}"
    value="${line#*=}"
    if [[ ! "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
      echo "tool-env: ${fragment}:${lineno}: invalid environment variable name: ${key}" >&2
      return 2
    fi
    if reserved_key "$key"; then
      echo "tool-env: ${fragment}:${lineno}: ${key} is reserved" >&2
      return 2
    fi
    if [[ "$key" != "PATH" ]] && initially_set "$key"; then
      continue
    fi
    if [[ ${#value} -ge 2 && ( ( "$value" == \"*\" ) || ( "$value" == \'*\' ) ) ]]; then
      value="${value:1:${#value}-2}"
    fi
    value="$(expand_value "$value")"
    export "${key}=${value}"
  done <"$fragment"
}

if [[ -d "$fragment_dir" ]]; then
  for fragment in "$fragment_dir"/*; do
    [[ -f "$fragment" ]] || continue
    load_fragment "$fragment"
  done
fi

exec "$@"
