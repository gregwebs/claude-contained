#!/usr/bin/env bash
# Create the native-shaped Claude launcher link expected by Claude Code.
set -euo pipefail

home="${1:-${HOST_HOME:-${HOME:-/home/dev}}}"
claude_bin="${CLAUDE_CONTAINED_CLAUDE_BIN:-/opt/claude/claude}"
bin_dir="${home}/.local/bin"
versions_dir="${home}/.local/share/claude/versions"
bin_link="${bin_dir}/claude"

mkdir -p "$bin_dir"

if [[ -e "$bin_link" || -L "$bin_link" ]]; then
  exit 0
fi

version=""
if [[ -x "$claude_bin" ]]; then
  version="$("$claude_bin" --version 2>/dev/null | awk 'NR == 1 {print $1}')"
fi

if [[ -n "$version" && "$version" =~ ^[0-9]+[.][0-9]+[.][0-9]+ ]]; then
  mkdir -p "$versions_dir"
  ln -sf "$claude_bin" "${versions_dir}/${version}"
  ln -sf "${versions_dir}/${version}" "$bin_link"
else
  ln -sf "$claude_bin" "$bin_link"
fi
