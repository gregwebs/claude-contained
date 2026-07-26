#!/bin/bash
# Generate the sandbox-runtime (srt) settings file for this container run.
#
# Runs as root from entrypoint.sh, before the drop to the dev user. The result is
# written root-owned and read-only so the sandboxed process cannot rewrite its own
# policy -- an allowlist the agent can edit is not a control at all.
#
# Filesystem paths must be generated at runtime rather than baked into the image:
# the mounted directories vary per invocation, and on Linux srt matches paths
# literally (no globs), so every path has to be spelled out.
set -euo pipefail

OUT="${SRT_SETTINGS_PATH:-/run/srt-settings.json}"
USER_SETTINGS="${HOST_HOME:-$HOME}/.claude-contained/srt-settings.json"

# Domains the AI CLIs need to function at all. A starting point, not an
# authoritative list -- tune with `srt --debug` and extend via the user settings
# file or --allow-host.
#
# Claude Code's OAuth login exchanges the pasted code with platform.claude.com,
# not api.anthropic.com -- omitting it doesn't error cleanly, srt's proxy just
# closes the connection, which Claude Code reports as "OAuth error: Socket is
# closed." The other CLIs' auth/token endpoints are included for the same reason.
# See https://code.claude.com/docs/en/network-config ("Network access requirements").
#
# Deliberately EXCLUDED, do not add back:
#   - storage.googleapis.com: the docs list it (plugin metadata, artifact upload
#     fallback), but it's the shared namespace for every GCS bucket on the
#     internet. Allowing it lets anything in the sandbox exfiltrate to an
#     attacker-owned bucket, which defeats the allowlist.
#   - the two Datadog telemetry intake hosts: optional traffic, fails silently
#     without them.
DEFAULT_DOMAINS='[
  "claude.ai",
  "claude.com",
  "platform.claude.com",
  "downloads.claude.ai",
  "mcp-proxy.anthropic.com",
  "code.claude.com",
  "api.anthropic.com",
  "api.openai.com",
  "auth.openai.com",
  "chatgpt.com",
  "accounts.google.com",
  "oauth2.googleapis.com",
  "generativelanguage.googleapis.com",
  "cloudcode-pa.googleapis.com",
  "api.githubcopilot.com",
  "api.mistral.ai",
  "github.com",
  "*.github.com",
  "*.githubusercontent.com",
  "registry.npmjs.org",
  "pypi.org",
  "files.pythonhosted.org"
]'

# ---- Writable paths ---------------------------------------------------------
# Tool config dirs live at fixed locations under HOME, so they are derivable.
# Project directories arrive via GIT_PROTECT_DIRS (colon-separated), which the
# launchers already populate with the working dir, every extra dir, and the
# worktree's main repo.
#
# The contained Claude profile is mounted at ~/.claude and is also visible under
# ~/.claude-contained/claude through the shared state directory. Keep both paths
# writable so either route works under srt's literal path matching.
#
# The node_modules overlay and shared skill mounts need no special handling:
# they land inside a project dir and inside ~/.claude/skills respectively, so the
# entries below already cover them.
_home="${HOST_HOME:-$HOME}"
write_paths=(
  /tmp
  "${_home}/.claude"
  "${_home}/.claude-contained"
  "${_home}/.claude-contained/claude"
  "${_home}/.codex"
  "${_home}/.copilot"
  "${_home}/.gemini"
  "${_home}/.vibe"
  "${_home}/.m2"
  "${_home}/.vaadin"
  "${_home}/.agents"
  "${_home}/.local"
  "${_home}/.cache"
  "${_home}/.npm"
)

if [ -n "${GIT_PROTECT_DIRS:-}" ]; then
  IFS=':' read -ra _dirs <<< "$GIT_PROTECT_DIRS"
  for _d in "${_dirs[@]}"; do
    [ -n "$_d" ] && write_paths+=("$_d")
  done
fi

if [ "${CLAUDE_CONTAINED_ZELLIJ:-}" = "1" ] && [ -n "${CLAUDE_CONTAINED_ZELLIJ_SESSION:-}" ]; then
  _zellij_uid="${HOST_UID:-$(id -u)}"
  write_paths+=(
    "${_home}/.claude-contained/zellij"
    "${_home}/.claude-contained/zellij/data"
    "${_home}/.claude-contained/zellij/cache"
    "${_home}/.claude-contained/zellij/cache/org"
    "${_home}/.claude-contained/zellij/cache/org/Zellij-Contributors"
    "${_home}/.claude-contained/zellij/cache/org/Zellij-Contributors/Zellij"
    "/tmp/claude-contained-zellij-runtime"
    "/tmp/claude-contained-zellij-runtime/zellij"
    "/tmp/claude-contained-zellij-runtime/zellij/contract_version_1"
    "/tmp/claude-contained-zellij-runtime/zellij/contract_version_1/${CLAUDE_CONTAINED_ZELLIJ_SESSION}"
    "/tmp/claude-contained-zellij-runtime/layouts"
    "/tmp/claude-contained-zellij-runtime/layouts/${CLAUDE_CONTAINED_ZELLIJ_SESSION}.kdl"
    "/tmp/zellij-${_zellij_uid}"
    "/tmp/zellij-${_zellij_uid}/zellij-log"
    "/tmp/zellij-${_zellij_uid}/zellij-log/zellij.log"
  )
fi

# Read-only extras appear in GIT_PROTECT_DIRS too. Listing them here is harmless:
# the bind mount is read-only, and the kernel wins over srt's policy.

writes_json="$(printf '%s\n' "${write_paths[@]}" | jq -R . | jq -sc 'unique')"

# ---- Extra domains from --allow-host ----------------------------------------
if [ -n "${SRT_ALLOW_HOSTS:-}" ]; then
  extra_json="$(printf '%s' "$SRT_ALLOW_HOSTS" \
    | jq -Rc 'split(",") | map(select(length > 0))')"
else
  extra_json='[]'
fi

# ---- User-supplied policy ---------------------------------------------------
# The user's file owns network policy. Anything it sets outside `filesystem`
# is carried through, so options like tlsTerminate or allowUnixSockets can be set
# there without this script needing to know about them.
if [ -f "$USER_SETTINGS" ] && jq -e . "$USER_SETTINGS" >/dev/null 2>&1; then
  user_json="$(cat "$USER_SETTINGS")"
else
  if [ -f "$USER_SETTINGS" ]; then
    echo "srt: malformed ${USER_SETTINGS}; refusing to generate sandbox policy" >&2
    exit 1
  fi
  user_json='{}'
fi

socket_paths=()
allow_all_unix_sockets=false

# SSH agent forwarding hands the socket in at /ssh-agent; allow it only when the
# launcher actually set up forwarding.
if [ -n "${SSH_AUTH_SOCK:-}" ]; then
  socket_paths+=("${SSH_AUTH_SOCK}")
fi

# Zellij uses a per-session Unix socket for client/server communication. Keeping
# XDG_RUNTIME_DIR under /tmp makes the socket tree container-local.
if [ "${CLAUDE_CONTAINED_ZELLIJ:-}" = "1" ] && [ -n "${CLAUDE_CONTAINED_ZELLIJ_SESSION:-}" ]; then
  socket_paths+=("/tmp/claude-contained-zellij-runtime/zellij/contract_version_1/${CLAUDE_CONTAINED_ZELLIJ_SESSION}")
  # srt's path-specific allowUnixSockets is macOS-only; Linux seccomp cannot
  # filter Unix sockets by path, so a Zellij run needs the coarse Unix-socket
  # switch while the VM/container remains the outer boundary.
  allow_all_unix_sockets=true
fi

if [ ${#socket_paths[@]} -gt 0 ]; then
  sock_json="$(printf '%s\n' "${socket_paths[@]}" | jq -R . | jq -sc 'unique')"
else
  sock_json='[]'
fi

# ---- Merge ------------------------------------------------------------------
# Precedence: built-in defaults + user file + --allow-host, unioned.
# allowLocalBinding is on because published ports (-p), the -H socat forwards and
# local dev servers all bind inside the container.
jq -n \
  --argjson defaults "$DEFAULT_DOMAINS" \
  --argjson extra "$extra_json" \
  --argjson writes "$writes_json" \
  --argjson user "$user_json" \
  --argjson socks "$sock_json" \
  --argjson allow_all_unix_sockets "$allow_all_unix_sockets" '
  ($user.network // {}) as $un
  | ($user.filesystem // {}) as $uf
  | $user
  + {
      network: (
        $un
        + {
            allowedDomains: (($un.allowedDomains // []) + $defaults + $extra | unique),
            deniedDomains: ($un.deniedDomains // []),
            allowLocalBinding: ($un.allowLocalBinding // true),
            allowUnixSockets: (($un.allowUnixSockets // []) + $socks | unique),
            allowAllUnixSockets: (($un.allowAllUnixSockets // false) or $allow_all_unix_sockets)
          }
      ),
      filesystem: (
        $uf
        + {
            denyRead: ($uf.denyRead // []),
            allowWrite: $writes,
            denyWrite: ($uf.denyWrite // [])
          }
      ),
      enableWeakerNestedSandbox: true
    }
  ' > "${OUT}.tmp"

# Root-owned and world-readable but not writable: the sandboxed process must be
# able to read its policy and unable to change it.
chown root:root "${OUT}.tmp" 2>/dev/null || true
chmod 444 "${OUT}.tmp"
mv -f "${OUT}.tmp" "$OUT"
