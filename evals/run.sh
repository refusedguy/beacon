#!/usr/bin/env bash
# Локальный прогон Harbor-подобных eval-кейсов beacon MCP.
# Подробности: evals/README.md
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"

# Вердикт самого прогона. PASS означает зелёный результат. BROKEN означает,
# что инструмент не завершил прогон: нет Go, не хватает конфигурации
# наблюдателя или suite ушёл ненулевым кодом. Код 2 не сводится к WARN,
# как в контракте rules gate (docs/RULES-CHECK-CONTRACT.md).
verdict() {
  printf 'verdict=%s: %s\n' "$1" "$2"
}

broken() {
  verdict BROKEN "$1"
  exit 2
}

GO="${GO:-${GO_BIN:-}}"
if [[ -z "$GO" || ! -x "$GO" ]]; then
  GO="$(command -v go || true)"
fi
[[ -n "$GO" && -x "$GO" ]] || broken "не найден Go: задайте GO или GO_BIN"
export GO

# Таймауты старта evalserver. Значения совпадают со smoke.DefaultStartTimeout
# (60s) и smoke.DefaultTerminateTimeout (2s) в evals/cmd/smoke.go. Env
# переопределяют их через smoke.Duration; некорректное значение не укорачивает
# лимит. Готовность это MCP-handshake, а не sleep.
export BEACON_EVAL_START_TIMEOUT="${BEACON_EVAL_START_TIMEOUT:-60s}"
export BEACON_EVAL_TERMINATE_TIMEOUT="${BEACON_EVAL_TERMINATE_TIMEOUT:-2s}"

# W01-W13: отдельный набор wake-сценариев T3.221. По умолчанию он выключен,
# потому что наблюдателю нужны внешние артефакты песочниц. Явное включение
# без конфигурации даёт BROKEN, а не тихий зелёный прогон без сценариев.
readonly WAKE_SCENARIOS="W01 W02 W03 W04 W05 W06 W07 W08 W09 W10 W11 W12 W13"
if [[ -n "${BEACON_WAKE_EVAL:-}" ]]; then
  if [[ -z "${BEACON_WAKE_STATE_DIR:-}" || -z "${BEACON_WAKE_WEBHOOKS:-}" ]]; then
    broken "wake-сценарии $WAKE_SCENARIOS: нужны BEACON_WAKE_STATE_DIR и BEACON_WAKE_WEBHOOKS"
  fi
  echo "==> wake eval $WAKE_SCENARIOS"
  export BEACON_WAKE_EVAL=1
else
  echo "==> wake eval $WAKE_SCENARIOS: пропущен (BEACON_WAKE_EVAL не задан)"
fi

# Harbor Step 3.1/3.2: отдельная проверка дрейфа плана и кода.
# Читает только файлы, без сети и тяжёлых зависимостей. Возвращает 1 на
# устаревший план или оставшийся артефакт спайка, поэтому дрейф проваливает
# карточку до PR.
echo "==> plan/code drift check (Harbor Step 3.1/3.2)"
GOPROXY=off "$GO" run -tags=eval ./cmd/evalserver --drift-check \
  || broken "plan/code drift check"

# Раннер сам создаёт временный XDG_STATE_HOME для дочернего сервера.
unset XDG_STATE_HOME || true

# HD-047 (ADR 0301): security severity мапируется в error/warning.
# Чистая проверка шкалы без сети и тяжёлых зависимостей. Своя шкала,
# blocking по умолчанию для medium/low и нестабильный результат проваливают
# прогон до остальных eval.
echo "==> severity mapping check (HD-047, ADR 0301)"
GOPROXY=off "$GO" run -tags=eval ./cmd/evalserver --severity-map-check \
  || broken "severity map check"

# HD-084 (ADR 0310): отчёт смены содержит все категории. Негативный контроль
# на пропущенную категорию проваливает прогон до остальных eval.
echo "==> night-shift report categories check (HD-084, ADR 0310)"
GOPROXY=off "$GO" run -tags=eval ./cmd/evalserver --report-check \
  || broken "night-shift report categories check"

# Зависимости уже лежат в module cache, сеть не нужна.
# Статусы отдельных кейсов печатает suite. Скрипт добавляет один итоговый
# вердикт, чтобы отказ инструмента нельзя было принять за зелёный прогон.
echo "==> MCP eval suite"
set +e
GOPROXY=off "$GO" run -tags=eval .
code=$?
set -e
if (( code != 0 )); then
  broken "MCP eval suite завершился кодом $code"
fi
verdict PASS "evals/run.sh"
