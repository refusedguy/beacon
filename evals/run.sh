#!/usr/bin/env bash
# Локальный прогон Harbor-подобных eval-кейсов beacon MCP.
# Подробности: evals/README.md
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

GO="${GO:-${GO_BIN:-}}"
if [[ -z "$GO" || ! -x "$GO" ]]; then
  GO="$(command -v go)"
fi
export GO

# Раннер сам создаёт временный XDG_STATE_HOME для дочернего сервера.
unset XDG_STATE_HOME || true

# Зависимости уже лежат в module cache, сеть не нужна.
GOPROXY=off exec "$GO" run -tags=eval ./evals
