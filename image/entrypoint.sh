#!/bin/bash
# Prepare host parity, networking, and sandboxing before starting the tool as the dev user.
set -e

# JBR as primary Java (with HotswapAgent support)
export JAVA_HOME=/opt/jbr
export PATH="/opt/claude:/home/dev/.sdkman/candidates/maven/current/bin:/home/dev/.sdkman/candidates/jbang/current/bin:$JAVA_HOME/bin:$PATH"

# Add host.local pointing to host machine
# Docker Desktop (macOS/Windows): use host.docker.internal
# Apple Containers / Docker on Linux: use gateway IP
if getent ahostsv4 host.docker.internal >/dev/null 2>&1; then
  HOST_IP=$(getent ahostsv4 host.docker.internal | head -1 | awk '{print $1}')
else
  HOST_IP=$(ip route | grep default | awk '{print $3}')
fi
if [ -n "$HOST_IP" ]; then
  grep -q "host.local" /etc/hosts 2>/dev/null || echo "$HOST_IP host.local" >> /etc/hosts
fi

# Forward host ports to container localhost (for MCPs that expect localhost).
# Must follow the host.local /etc/hosts write above, which the relays resolve.
host-forward

# Path parity setup: match host HOME and UID/GID
if [ -n "${HOST_HOME:-}" ]; then
  mkdir -p "${HOST_HOME}"

  # Match host UID/GID (handle conflicts)
  if [ -n "${HOST_UID:-}" ] && [ -n "${HOST_GID:-}" ]; then
    EXISTING_GROUP=$(getent group "${HOST_GID}" | cut -d: -f1)
    if [ -n "$EXISTING_GROUP" ] && [ "$EXISTING_GROUP" != "dev" ]; then
      groupmod -g $((HOST_GID + 10000)) "$EXISTING_GROUP" 2>/dev/null || true
    fi

    EXISTING_USER=$(getent passwd "${HOST_UID}" | cut -d: -f1)
    if [ -n "$EXISTING_USER" ] && [ "$EXISTING_USER" != "dev" ]; then
      usermod -u $((HOST_UID + 10000)) "$EXISTING_USER" 2>/dev/null || true
    fi

    groupmod -g "${HOST_GID}" dev 2>/dev/null || true
    usermod -u "${HOST_UID}" -g "${HOST_GID}" -d "${HOST_HOME}" dev 2>/dev/null || true
  fi

  chown dev:dev "${HOST_HOME}" 2>/dev/null || true
  chown -R dev:dev "${HOST_HOME}/.claude" 2>/dev/null || true
  chown -R dev:dev /ms-playwright 2>/dev/null || true

  export HOME="${HOST_HOME}"

  # Create container-side symlink to ~/.claude.json in shared directory
  # (Apple Containers can't bind-mount individual files, so we use symlinks)
  SHARED_CLAUDE_JSON="${HOST_HOME}/.claude-contained/.claude.json"
  if [ -e "${SHARED_CLAUDE_JSON}" ] && [ ! -e "${HOST_HOME}/.claude.json" ]; then
    ln -s "${SHARED_CLAUDE_JSON}" "${HOST_HOME}/.claude.json"
    chown -h dev:dev "${HOST_HOME}/.claude.json" 2>/dev/null || true
  fi

  # Copy .gitconfig for git commit identity (read-only, no sync back needed)
  SHARED_GITCONFIG="${HOST_HOME}/.claude-contained/.gitconfig"
  if [ -e "${SHARED_GITCONFIG}" ] && [ ! -e "${HOST_HOME}/.gitconfig" ]; then
    cp "${SHARED_GITCONFIG}" "${HOST_HOME}/.gitconfig"
    chown dev:dev "${HOST_HOME}/.gitconfig" 2>/dev/null || true
  fi

  # Create native-shaped Claude symlink structure when the host has no link yet.
  /usr/local/bin/claude-native-link "${HOST_HOME}" 2>/dev/null || true
  chown -R dev:dev "${HOST_HOME}/.local" 2>/dev/null || true
fi

# Protect .git/config files from modification (prevents AI tools from changing remote URLs)
# Files are made root-owned and read-only so the dev user cannot modify or chmod them
if [ -n "${GIT_PROTECT_DIRS:-}" ]; then
  IFS=':' read -ra _git_dirs <<< "$GIT_PROTECT_DIRS"
  for _dir in "${_git_dirs[@]}"; do
    _git_config="${_dir}/.git/config"
    # Handle worktrees where .git is a file pointing elsewhere
    if [ -f "${_dir}/.git" ] && ! [ -d "${_dir}/.git" ]; then
      _gitdir=$(sed -n 's/^gitdir: //p' "${_dir}/.git")
      # Resolve relative paths
      case "$_gitdir" in
        /*) ;;
        *) _gitdir="${_dir}/${_gitdir}" ;;
      esac
      _git_config="${_gitdir}/config"
    fi
    if [ -f "$_git_config" ]; then
      chown root:root "$_git_config" 2>/dev/null || true
      chmod 444 "$_git_config" 2>/dev/null || true
    fi
  done
fi

if [ "${CLAUDE_CONTAINED_ZELLIJ:-}" = "1" ] && [ -n "${CLAUDE_CONTAINED_ZELLIJ_SESSION:-}" ]; then
  _zellij_uid="${HOST_UID:-$(id -u dev)}"
  _zellij_tmp="/tmp/zellij-${_zellij_uid}"
  _zellij_runtime="/tmp/claude-contained-zellij-runtime"
  mkdir -p \
    "${HOST_HOME}/.claude-contained/zellij/data" \
    "${HOST_HOME}/.claude-contained/zellij/cache/org/Zellij-Contributors/Zellij" \
    "${_zellij_tmp}/zellij-log" \
    "${_zellij_runtime}/zellij/contract_version_1" \
    "${_zellij_runtime}/layouts" 2>/dev/null || true
  touch "${_zellij_tmp}/zellij-log/zellij.log" 2>/dev/null || true
  chown -R dev:dev \
    "${HOST_HOME}/.claude-contained/zellij" \
    "${_zellij_tmp}" \
    "${_zellij_runtime}" 2>/dev/null || true
  chmod 700 \
    "${_zellij_tmp}" \
    "${_zellij_tmp}/zellij-log" \
    "${_zellij_runtime}" \
    "${_zellij_runtime}/zellij" \
    "${_zellij_runtime}/zellij/contract_version_1" \
    "${_zellij_runtime}/layouts" 2>/dev/null || true
  chmod 600 "${_zellij_tmp}/zellij-log/zellij.log" 2>/dev/null || true
fi

# Start virtual framebuffer so Chrome/Chromium can run without a real display
if [ -z "${DISPLAY:-}" ]; then
  export DISPLAY=:99
  Xvfb :99 -screen 0 1280x1024x24 -nolisten tcp &
fi

# Wrap the command in the sandbox (srt), unless disabled with --no-sandbox.
# Generating the policy here, as root, keeps /run/srt-settings.json out of the
# sandboxed process's reach -- an allowlist the agent can rewrite is not a control.
#
# `set --` prepends to "$@" so srt ends up *inside* the gosu below and runs as the
# dev user. It must not run as root.
#
# Note this is the only place the sandbox is applied: `container exec` bypasses the
# entrypoint entirely, so attach paths route through srt-run instead.
if [ "${SRT_DISABLE:-}" != "1" ]; then
  if /usr/local/bin/srt-settings.sh; then
    set -- srt --settings "${SRT_SETTINGS_PATH:-/run/srt-settings.json}" -- "$@"
  else
    echo "entrypoint: failed to generate sandbox policy; refusing to run unsandboxed." >&2
    echo "            re-run with --no-sandbox to bypass deliberately." >&2
    exit 1
  fi
fi

# Drop to dev user (or stay root if STAY_ROOT=1)
if [ "$(id -u)" = "0" ] && [ "${STAY_ROOT:-}" != "1" ]; then
  USER_HOME="${HOME:-/home/dev}"
  exec gosu dev env \
    JAVA_HOME="$JAVA_HOME" \
    PATH="${USER_HOME}/.local/bin:$PATH" \
    HOME="$USER_HOME" \
    DISPLAY="$DISPLAY" \
    "$@"
else
  # Also update PATH for root/non-gosu case
  export PATH="${HOME}/.local/bin:$PATH"
  exec "$@"
fi
