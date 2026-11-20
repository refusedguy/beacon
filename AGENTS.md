# beacon: правила агента

Коротко. Этот файл важнее старых карточек и ADR.

## Запреты

| Нельзя | Вместо этого |
| --- | --- |
| Коммитить или пушить в main | Ветка от origin/main, worktree, PR |
| `go test ./...`, `go build ./...`, `beacon gates` локально | GitHub Actions CI на PR; локально только `gofmt -w` на свои .go |
| `gh pr checks --watch`, `sleep` в цикле | `waiting, <watchID>` и сон |
| Трогать чужие OwnerFiles, `docs/tasks/` | Только своё поле «Владелец файлов» |
| `batch-apply` без команды владельца | Ждать команды владельца |
| Закрывать чужие карточки в чужом PR | Не трогать |
| `notify.target: "<cardID>"` | Реальный `ses_...` |

## Диагностика

Не грепать логи и не ждать в bash.

- `beacon_list_running`
- `beacon_stats`
- `beacon inspect`
- `beacon logs --tail 40 --errors`

## Своя ветка и PR

1. Ветка от `origin/main`, worktree.
2. Только свои OwnerFiles.
3. `git diff --stat` перед `gh pr create`.
4. `git push -u origin <ветка>`, `gh pr create --base main --head <ветка>`.
5. Отчёт: PR, headSHA, файлы.

## beacon_watch_pr

- `repo: "refusedguy/beacon"`, `pr`, `headSHA`, role: "executor" или role: "fixer".
- `notify.target`: `ses_...` из `_meta["ai.opencode/sessionID"]` или явный `ses_...`. Никогда `<cardID>`.
- Ход заканчивается фразой `waiting, <watchID>`.
- Оркестратор: role: "orchestrator". Вердикт только из `beacon_get_result`.

## Авто-мерж

Beacon мержит зелёный PR по `verdict=ready` (ADR 0072). Исключения политики: `requireApproval`, `waitFor`, `waitForMain`. Исполнитель просыпается на `keep_working`, `rebase`, `retry` или ошибке мержа. При `ready` не вызывает `gh pr merge`.

## Блокеры main

Красная проверка на `main`: Beacon ставит `blocked`, пишет `BlockerIncident`. Исполнитель видит внешнюю поломку на `origin/main`: `beacon_ack` с `decision="blocked:not_mine"` и причиной. Оркестратор спавнит FIX-агента. main не чиним.

## Слои

- `internal/domain`: только stdlib.
- infra не импортирует соседние infra, app, cmd.
- `internal/app` не импортирует `internal/domain`.
- `cmd/beacon`: без `os.ReadFile`, `os.WriteFile`, `net/http`, `os/exec`.

## После сжатия

Плагин держит ключ `beacon/survival-rules` в instructions. Ключ стабильный, перезаписывается на старте и на хуке `compaction`. Правила выпали из контекста, читай ключ, а не гадай.

## MCP

Code Mode: `search({ query: "beacon", namespace: "beacon" })`, затем `tools.beacon.<name>`. Семь инструментов: `beacon_watch_pr`, `beacon_wait_result`, `beacon_get_result`, `beacon_list_running`, `beacon_ack`, `beacon_cancel_watch`, `beacon_stats`.

Шаблоны: `docs/templates/executor-task.md`, `docs/templates/orchestrator-task.md`.

## Коммиты

`refusedguy <refusedguy@users.noreply.github.com>`, без `-c`, без `git add .`. Только свои файлы.
