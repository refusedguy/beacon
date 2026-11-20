# Правила агента в репозитории beacon

## Beacon MCP обязателен для цикла

MCP-сервер Beacon подключён в `/mnt/projects/beacon-handoff/opencode.json`. Инструменты доступны через инструмент `execute` (Code Mode): сначала `search({ query: "beacon", namespace: "beacon" })`, затем вызовы `tools.beacon.beacon_watch_pr({...})` и остальные шесть инструментов. Не писать самописный JSON-RPC-клиент.

До начала работы прочитать шаблон своего цикла:

- исполнитель: `docs/templates/executor-task.md`
- оркестратор: `docs/templates/orchestrator-task.md`

Исполнитель после открытия PR:

1. Берёт `headSHA` командой `git rev-parse HEAD` в своём worktree.
2. Вызывает `beacon_watch_pr` с `repo: "refusedguy/beacon"`, `pr`, `headSHA`, `role: "executor"`, `notify: { adapter: "opencode", target: "<cardID>" }`.
3. Завершает ход ровно фразой `waiting, <watchID>`. Не опрашивает `gh pr checks` в цикле и не ждёт вручную.

Оркестратор подписывается на тот же PR с `role: "orchestrator"`. Вердикт читается только через `beacon_get_result`. `<subagent>`-отчёт со словом `waiting` вердиктом не является. Мерж только после зелёных проверок и читаного `verdict=ready`.

## Самоиспользование инструментов

- Карточки создаются только через `go run ./cmd/batch-apply --dir <json> --check`, затем тем же вызовом без `--check`. Ручной `cat` для карточек не используется.
- Оркестратор регистрирует Watch через `tools.beacon.beacon_watch_pr` и читает вердикт через `beacon_get_result`. `gh pr checks` вручную не вызывается.
- Wake доставляется через opencode API `POST /api/session/{id}/prompt` с `delivery=steer`. В тексте настоящие `watchID` и `obligationID`, а не заглушки.
- Форматирование и проверки выполняет CI, локальный `gofmt` не запускается.

## Пересборка MCP-бинарника

Если `beacon_watch_pr` отвечает `PERSIST_FAILED: watch_pr: store is nil`, бинарник собран до коммита с подключённым store. Пересобрать из корня репо:

```
go build -o .opencode/bin/beacon ./cmd/beacon
```

## Коммиты и локальные проверки

Коммиты от `refusedguy <refusedguy@users.noreply.github.com>`, без флага `-c`, без `git add .`. В коммит только файлы из поля «Владелец файлов» карточки. Локальные проверки не являются гейтом: `gofmt`, `go test` и `go build` выполняет CI.

## Project scope и вердикты

Состояние Beacon привязано к проекту: state лежит в `<XDG_STATE_HOME>/beacon/projects/<name>-<hash>/beacon.db`, когда cwd содержит `.git`. MCP поднимается с `cwd` текущего проекта из `opencode.json`. Переход в другую папку проекта даёт другой state. Явный override: `BEACON_STATE_DIR`.

Локальный MCP поднимает CI-мост: он читает проверки PR через `gh` и переводит Watch в `ready` (все зелёные) или `retry` (FAILURE/CANCELLED). Оркестратор мержит только после `beacon_get_result` с `verdict=ready`; ручной `gh pr checks` в цикле запрещён.
