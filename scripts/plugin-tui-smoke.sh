#!/usr/bin/env bash
# Boot the OpenCode TUI with the Beacon plugin under a pseudo-terminal and fail
# on the plugin load errors that unit tests cannot see. CI entry point.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# The JS runner caps the boot at DEFAULT_TIMEOUT_MS. This outer cap keeps the
# whole smoke test inside 30 seconds even if the CLI never yields a prompt.
exec timeout --kill-after=2s 30s \
  node .opencode/plugins/beacon-project/tui_smoke_test.js --live
