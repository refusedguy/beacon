# Правила агента в репозитории beacon

Этот файл описывает текущий цикл работы. Он важнее старых карточек и ADR, где зафиксированы отменённые режимы.

## Доступ к Beacon

MCP-сервер Beacon подключён в `/mnt/projects/beacon-handoff/opencode.json`. Инструменты доступны через инструмент `execute` в Code Mode: сначала `search({ query: "beacon", namespace: "beacon" })`, затем вызовы `tools.beacon.<name>`. Не писать самописный JSON-RPC-клиент.

Семь инструментов: `beacon_watch_pr`, `beacon_wait_result`, `beacon_get_result`, `beacon_list_running`, `beacon_ack`, `beacon_cancel_watch`, `beacon_stats`.

Перед работой прочитать шаблон своего цикла:

- исполнитель и фиксер: `docs/templates/executor-task.md`
- оркестратор: `docs/templates/orchestrator-task.md`

## Слабый ПК и CI-only

Локально запускать только `gofmt -w <изменённые_файлы>`.

Локально НЕ запускать:

- `go test ./...`
- `go build ./...`
- `beacon gates`
- `gh pr checks --watch`, `sleep` и любые циклы ожидания

Все полные проверки прогоняет GitHub Actions CI на PR. Локальный прогон не является гейтом и только тратит ресурсы слабого ПК.

## Ветки, worktree и PR

Прямые коммиты в `main` запрещены. Любая работа сабента идёт через отдельную ветку и worktree, затем через PR.

1. Создать ветку от актуального `origin/main`.
2. Внести изменения только в файлы из поля «Владелец файлов» своей карточки.
3. Выполнить `git push -u origin <ветка>`, затем `gh pr create --base main --head <ветка>`.
4. В отчёте указать номер PR, headSHA от `git rev-parse HEAD` и список файлов.

Мержит только CI вместе с Beacon. Оркестратор мержит вручную только после зелёного CI, когда авто-мерж невозможен по политике Watch.

## Контракт beacon_watch_pr

Исполнитель регистрирует PR сразу после `gh pr create`.

- `repo`: `refusedguy/beacon`.
- `pr`, `headSHA`, `baseRef`: факты сдачи PR.
- role: "executor" или role: "fixer".
- `notify.target`: РЕАЛЬНЫЙ `ses_...` из `_meta["ai.opencode/sessionID"]` текущей сессии или явно переданный `notify.target: "<ses_...>"`.

НИКОГДА не передавать в `notify.target` значение `<cardID>`. Это ломает доставку wake: адрес должен быть идентификатором живой сессии OpenCode.

После регистрации завершить ход ровно фразой `waiting, <watchID>`. Запрещено крутить `gh pr checks --watch` или `sleep` в цикле и ждать вручную. Сон означает завершение хода, а не опрос.

## Авто-мерж по умолчанию

Beacon сам мержит зелёный PR по `verdict=ready` (ADR 0072). Пустая политика Watch означает мерж без участия агента. Исключения задаются политикой Watch: `requireApproval`, `waitFor`, `waitForMain`.

Исполнитель просыпается ТОЛЬКО если мерж невозможен: вердикты `keep_working`, `rebase`, `retry` или ошибка мержа. При `ready` исполнитель не получает команды мерж и не вызывает `gh pr merge`.

## Внешние поломки main

Если проверка красная и проблема на `main`, Beacon сам поставит Watch `blocked` и запишет `BlockerIncident`. Исполнитель не чинит чужую поломку.

Если исполнитель увидел внешнюю поломку локально на `origin/main`, он вызывает `beacon_ack` с `decision="blocked:not_mine"` и непустой причиной. Оркестратор просыпается по этому сигналу и запускает FIX-агента.

## Диагностика вместо гаданий и sleep

Не грепать логи вручную и не ждать в bash. Использовать инструменты:

- `beacon_list_running` для снимка активных Watch;
- `beacon_stats` для счётчиков и диагностики состояния;
- `beacon inspect` для разбора конкретного Watch;
- `beacon logs --tail 50 --errors` для последних ошибок.

## Запреты

- Не закрывать чужие карточки в несвязанных PR.
- Не запускать `batch-apply` без явной команды владельца. Карточки меняются только по такой команде.
- Не делать прямые коммиты в `main`.
- Не вызывать `gh pr checks --watch` и не использовать `sleep` в циклах.
- Не вызывать `beacon gates` локально.

## Архитектурные границы

- `internal/domain` использует только stdlib.
- Infra-пакеты не импортируют соседние infra-, app- и cmd-пакеты.
- `internal/app` не импортирует `internal/domain`.
- В `cmd/beacon` нет `os.ReadFile`, `os.WriteFile`, `net/http`, `os/exec`.

Границы проверяются гейтами в CI, по ADR 0005.

## Project scope

Состояние Beacon привязано к проекту: state лежит в `<XDG_STATE_HOME>/beacon/projects/<name>-<hash>/beacon.db`, когда cwd содержит `.git`. MCP поднимается с `cwd` текущего проекта из `opencode.json`. Явный override: `BEACON_STATE_DIR`.

## Коммиты

Коммиты от `refusedguy <refusedguy@users.noreply.github.com>`, без флага `-c`, без `git add .`. В коммит только файлы из поля «Владелец файлов» карточки.
