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
DEFAULT_DOMAINS='[
  "api.anthropic.com",
  "api.openai.com",
  "chatgpt.com",
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
# The node_modules overlay and --share-skills mounts need no special handling:
# they land inside a project dir and inside ~/.claude/skills respectively, so the
# entries below already cover them.
_home="${HOST_HOME:-$HOME}"
write_paths=(
  /tmp
  "${_home}/.claude"
  "${_home}/.claude-contained"
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
    echo "srt: ignoring malformed ${USER_SETTINGS}" >&2
  fi
  user_json='{}'
fi

# SSH agent forwarding hands the socket in at /ssh-agent; allow it only when the
# launcher actually set up forwarding.
if [ -n "${SSH_AUTH_SOCK:-}" ]; then
  sock_json="[\"${SSH_AUTH_SOCK}\"]"
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
  --argjson socks "$sock_json" '
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
            allowUnixSockets: (($un.allowUnixSockets // []) + $socks | unique)
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
