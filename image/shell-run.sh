#!/usr/bin/env bash
set -euo pipefail

# Some container/sandbox combinations leave an interactive bash without a
# controlling terminal. Starting bash under util-linux script gives it a fresh
# child PTY, which avoids repeated job-control warnings in debug shells.
if [[ $# -eq 0 ]] \
  && { [[ "${CLAUDE_CONTAINED_SHELL_RUN_FORCE_SCRIPT:-}" == "1" ]] || [[ -t 0 && -t 1 ]]; } \
  && command -v script >/dev/null 2>&1; then
  exec script -qfec 'exec /usr/bin/env bash' /dev/null
fi

exec /usr/bin/env bash "$@"
