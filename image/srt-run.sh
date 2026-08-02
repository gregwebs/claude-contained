#!/bin/bash
set -euo pipefail
# Run a command under the sandbox policy generated for this container.
#
# `container exec` / `docker exec` bypass the entrypoint, so anything started that
# way is unsandboxed unless routed through here:
#   container exec -it -u dev <name> srt-run claude
exec /usr/local/bin/srt --settings "${SRT_SETTINGS_PATH:-/run/srt-settings.json}" -- "$@"
