#!/bin/bash
# Forward host ports into the container's localhost, for MCP servers and other
# clients that insist on 127.0.0.1.
#
# Reads HOST_FORWARD_PORTS, a comma-separated list of LOCAL[:HOST] mappings, and
# starts one backgrounded socat relay per mapping from the container's localhost
# to host.local (which the entrypoint has already written into /etc/hosts).
#
# This lives in its own script rather than inline in entrypoint.sh so it can be
# tested on the host without running the entrypoint, which needs root to chown and
# usermod. tests/host-forward.test.sh is that test, and it exists because this
# forwarding was silently broken for months: a quoting change collapsed socat's two
# address arguments into one, which socat rejects outright. Keep the two addresses
# as two separate arguments.
set -e

[ -n "${HOST_FORWARD_PORTS:-}" ] || exit 0

IFS=',' read -ra PORTS <<< "$HOST_FORWARD_PORTS"
for mapping in "${PORTS[@]}"; do
  if [[ "$mapping" == *:* ]]; then
    local_port="${mapping%%:*}"
    host_port="${mapping##*:}"
  else
    local_port="$mapping"
    host_port="$mapping"
  fi
  socat "TCP-LISTEN:${local_port},fork,reuseaddr" "TCP:host.local:${host_port}" &
done
