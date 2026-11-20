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

Code Mode: `search({ query: "beacon", namespace: "beacon" })`, затем `tools.beacon.<name>`. Восемь инструментов: `beacon_watch_pr`, `beacon_wait_result`, `beacon_get_result`, `beacon_list_running`, `beacon_ack`, `beacon_cancel_watch`, `beacon_stats`, `beacon_defer_wiring`.

`beacon_defer_wiring` вызывай сразу, как только понял, что часть фичи осталась неподключённой (OwnerFiles другого агента). Он возвращает готовую карточку `WIRING-<PR>`, которую надо добавить в свой PR. Не откладывай wiring на «потом»: без карточки работа теряется.

Шаблоны: `docs/templates/executor-task.md`, `docs/templates/orchestrator-task.md`.

## Оркестратору: заполнение слотов

Каждое `<beacon-event>` с `nextAction: report_to_orchestrator` (`wave item done`) означает один свободный слот. В том же ходе, без пинка владельца, запусти одного нового сабента. Один сигнал равен одному spawn. Не отвечай «тихо продолжаю» и не оставляй слот пустым.

Исключение: если main красный по required-проверке, новых сабентов не спавни. Дождись FIX-агента и зелёного main. Информационные проверки (`required: false`) main не ломают.

Источник задач по приоритету: сначала `beacon deferred --format json` (отложенное wiring), затем `docs/ROADMAP_STATUS.md` KEEP_TODO и аудиты. Перед запуском проверь непересечение OwnerFiles через `beacon task check-launch` / `PlanLaunch`.

НЕ открывай карточки со статусом `dropped`. Их подтверждение фиксирует, что дизайн брошен или дублирует существующий код. Архивные S1-S7 карточки в大部分 dropped по ADR 0086/0094: серверные проекты, реестры, сессии, адаптеры и recovery уже сделаны в store/MCP или запрещены вторым протоколом. Агент обязан проверить premise перед кодом. Если функциональность уже есть в `internal/infra/store/*`, `internal/infra/mcp/*`, `internal/infra/registry/*` или `internal/domain/*`, НЕ пиши дубль, вернись с обоснованием (как агенты S3.001/S3.002/S3.003/S3.008/S4.001). Карточку `dropped` в работу не брать.

Тихий режим: на терминальный сигнал делай действие и не печатай в чат статусную строку «PR X влит». Пиши в чат только новую информацию: решение владельца, измерение, блокер, результат выборки.

Информационные проверки с `required: false` или `continue-on-error: true` не являются блокерами. По ним не вызывай `beacon_ack decision="blocked:not_mine"` и не спавни FIX-агента.

## Прозу пиши по unslop

Все тексты, которые ты пишешь (ответы, docs, README, PR body, commit messages, комментарии), пропускай через правила скилла `unslop`: без em dashes, простые слова, активный залог, одна мысль на предложение, без заполняющих фраз и без «rule of three». Загрузи скилл `unslop` перед написанием любой прозы.

## Коммиты

`refusedguy <refusedguy@users.noreply.github.com>`, без `-c`, без `git add .`. Только свои файлы.
