#!/usr/bin/env bash
# Одна команда для локального и CI-прогона. См. docs/e2e/README.md.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."
GO="${GO_BIN:-/tmp/opencode/go126/bin/go}"
if [[ ! -x "$GO" ]]; then
  GO="$(command -v go)"
fi
exec "$GO" test -tags=e2e ./tests/e2e -count=1 -v "$@"
