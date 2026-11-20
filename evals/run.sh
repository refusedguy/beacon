#!/usr/bin/env bash
# Локальный прогон Harbor-подобных eval-кейсов beacon MCP.
# Подробности: evals/README.md
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

GO="${GO:-${GO_BIN:-}}"
if [[ -z "$GO" || ! -x "$GO" ]]; then
  GO="$(command -v go)"
fi
export GO

# Harbor Step 3.1/3.2: отдельная проверка дрейфа плана и кода.
# Читает только файлы, без сети и тяжёлых зависимостей. Возвращает 1 на
# устаревший план или оставшийся артефакт спайка, поэтому дрейф проваливает
# карточку до PR.
echo "==> plan/code drift check (Harbor Step 3.1/3.2)"
GOPROXY=off "$GO" run -tags=eval ./cmd/evalserver --drift-check

# Раннер сам создаёт временный XDG_STATE_HOME для дочернего сервера.
unset XDG_STATE_HOME || true

# Зависимости уже лежат в module cache, сеть не нужна.
echo "==> MCP eval suite"
GOPROXY=off exec "$GO" run -tags=eval .
