# Направление 1: разрыв между автоспавном в коде и его подключением

Дата снимка: 2026-11-22. Дерево: `origin/main` на `408a21951b82950f5b7884190614a6441bc3caad`. Все ссылки на код даны по этому дереву.

## Что уже есть и что не входит в бэклог

Проверка перед карточками показала, что примитивы Direction 1 уже влиты. Новые карточки их не повторяют.

- `AUTOSP-01` влит PR #1448, `merge_policy_hold` живёт в `internal/infra/cibridge/verdict_applier.go:187`, тест `TestMergePolicyHoldsReadyVerdict` в `internal/infra/cibridge/bridge_test.go:1828`. Проверка `RequireApproval`, `WaitFor`, `WaitForMain` стоит в `verdict_applier.go:165-171`. Остаток описан в finding 7.
- `AUTOSP-02` влит: `PromptOptions`, `StartPromptOptions`, `PromptWithOptions` в `internal/infra/opencode/client.go:397`, `:413`, `:425`. Остаток в finding 3.
- `AUTOSP-03` влит: реестр ACP-раннеров содержит `gemini` в `internal/infra/wake/acp.go:430`.
- `AUTOSP-07` влит: `daemonWakeGate` владеет `SlotLedger` в `cmd/beacon/daemon.go:226`,Issue в `:256`,Burn в `:289`.
- `AUTOSP-08` влит: `fetchOriginMain` в `cmd/beacon/agents.go:968` вызывается перед `git worktree add` в `:1017`. Остаток в finding 6.
- `AUTOSP-09` влит: `LinkCardWatch` в `internal/infra/store/card_links.go:118`. Остаток в finding 4.
- `AUTOSP-10` влит: eval `decideAutoSpawn` в `evals/main.go:3235`. Остаток в finding 1.
- `AUTOSP-11` влит: `CheckLaunchFootprint` в `internal/infra/server/metrics.go:443`. Остаток в finding 4.
- `audit-01-autospawn.md`, ADR 0306 и ADR 0307 уже зафиксировали решение. Новый бэклог не меняет эти выводы, он закрывает только разрыв между готовым кодом и запуском.

Главный вывод: обработчик завершения волны есть, раннер есть, гейты есть. Нет ни одного production-вызова, который связал бы их в цепочку. Сигнал доставляется оркестратору текстом, и следующий воркер запускается только после рассуждения LLM или человека.

## Таблица находок

| Приоритет | Проблема | Место в коде | Целевое решение | Эффект |
| --- | --- | --- | --- | --- |
| P0 | Решение об автозапуске живёт только в eval. Production-цикл его не вызывает. | `evals/main.go:3235 decideAutoSpawn`, `cmd/beacon/mcp.go:1977 ReconcileLoop`, `cmd/beacon/mcp.go:1957 bridge.Refresh` | Перенести решение в `internal/app/orchestrator/decision.go` как порт и вызвать его в `ReconcilePass` после `Refresh`. Добавить флаг `--auto-spawn` в `cmd/beacon/up.go:149`. | Снимает ручной пинк оркестратора. Экономит сотни токенов reasoning на каждую волну и убирает задержку между терминальным вердиктом и стартом воркера. |
| P0 | Терминальный сигнал доставляется как текст оркестратору, а не как команда спавна. Без привязанного оркестратора цепочка обрывается. | `internal/infra/cibridge/verdict_applier.go:614 terminalNotice`, `:616 nextAction`, `:628 notifyOrchestratorTerminal`, `:636-639` | В `ReconcilePass` читать терминальный вердикт из Watch и инициировать спавн напрямую. Доставка текстового уведомления остаётся для наблюдателя, но перестаёт быть единственным путём. | Надёжность: закрытый Watch всегда порождает следующую задачу. RAM не меняется, токены оркестратора не расходуются на разбор envelope. |
| P0 | Спавн требует `BEACON_PARENT_SESSION` в окружении и не работает из демона. Порт спавна не умеет отправлять промпт с `delivery` и `resume`. | `cmd/beacon/spawn.go:127 spawnParentEnv`, `:176-180`, `cmd/beacon/opencode.go:24 opencodePort`, `:26-27`, `internal/infra/opencode/client.go:425 PromptWithOptions` | Добавить в `cmd/beacon/spawn.go` явный параметр parentID вместо обязательной env-переменной. Расширить порт методом с `PromptOptions` и использовать `StartPromptOptions` для первого промпта. | Убирает отказ спавна из демона. `delivery=steer` с `resume=true` продолжает существующую сессию вместо запуска нового контекста. Экономия токенов и меньше дублей сессий. |
| P1 | Гейты запуска есть, но ни один из них не вызывается перед стартом раннера. | `internal/app/taskcli/service.go:507 AdmitLaunch`, `internal/infra/github/points.go:441 PreflightGuard.Check`, `internal/infra/server/metrics.go:443 CheckLaunchFootprint` | Собрать один launch-гейт в `internal/app/orchestrator` и вызывать его до создания воркера. Порядок: квота, свободные слоты, RSS, непересечение OwnerFiles. | RAM: отказ при RSS более 100 MB и 15 активных Watch. Надёжность: гонки за квоту и файлы между воркерами исключены. |
| P1 | Выбор карточки и связь с Watch доступны только через MCP-инструмент и ручной CLI. Демон не может вызвать их как Go-порт. | `internal/infra/mcp/tool_task_claim.go:34 TaskClaimToolName`, `:111 Claim`, `:136 FileTaskClaimStore`, `cmd/beacon/task.go:660 PlanLaunch` | Открыть Go-порт поверх `FileTaskClaimStore` и `PlanLaunch`, выбирать следующую todo-карточку по индексу и графу без циклов, писать `LinkCardWatch` до старта раннера. | Атомарный claim убирает двойную выдачу одной карточки. Связь cardID↔watchID переживает смену agentID и `ses_` идентификатора. |
| P1 | Фикс политики мержа влит, но новый путь спавна не проверяет удержание политики. Зелёный PR под `waitFor` может получить воркера. | `internal/infra/cibridge/verdict_applier.go:187 mergePolicyHold`, `:217 publishMergeWait`, PR #1448 | Перед спавном читать verdict Watch. При `wait_dependency:*` слот не открывать, инцидент писать в observe. | Надёжность: политика действует и на мерж, и на запуск следующей волны. Токены не тратятся на воркера, который не может влиться. |
| P2 | Worktree создаётся с fetch только в пути `agents bind`. Путь спавна через ACP работает в текущем `cwd` и не обновляет `origin/main`. | `cmd/beacon/agents.go:968 fetchOriginMain`, `:987 bindRepoWorktree`, `:1017`, `cmd/beacon/spawn.go:503 runSpawnACP`, `:571 NewSession` | Вызывать `bindRepoWorktree` до запуска ACP-раннера. При неудачном fetch старт отменяется. | Надёжность по HD-011: воркер всегда берёт свежий `origin/main`. Меньше ребейзов и меньше токенов на разрешение конфликтов. |
| P2 | Eval проверяет локальную модель решения, а не production-вызов. Регрессия в подключении не ловится. | `evals/main.go:3272 evalAutoSpawnNoManualPing`, `:3290-3296` | Добавить eval на production-порт: терминальный вердикт, свободный слот, todo-карточка, зелёный main дают ровно один spawn и одну связь. Повтор сигнала и занятый слот дают ноль. | Регрессия в подключении ловится в CI. Стоимость: один eval без сети. |

## Проверка внешних фактов

Источник: живой OpenCode v2.3.2. Версия из `$HOME/.local/state/opencode/service.json`, поле `version`. OpenAPI получен с `GET http://127.0.0.1:49374/openapi.json` с Basic-авторизацией `opencode:<password>` из того же файла. Спецификация OpenAPI 3.1.0, 458269 байт.

- `prompt_async` в спецификации отсутствует. Число вхождений `prompt_async` и `promptasync` равно нулю. Операция `session.prompt` (`POST /api/session/{sessionID}/prompt`) использует поля `id`, `text`, `files`, `agents`, `skills`, `metadata`, `delivery`, `resume`. Поля `async` нет.
- Описание `session.prompt`: «Durably admit one session input and schedule agent-loop execution unless resume is false». Асинхронность уже встроена в метод. Отдельный `prompt_async` не нужен.
- `POST /api/session` принимает `parentID`, `agent`, `model`, `location`, `metadata`, `permissions`. Описание говорит, что дочерняя сессия после каждого завершённого раунда отправляет `<subagent>` в inbox родителя и будит его. Это второй возможный путь доставки сигнала без ручного пинка.
- Есть `POST /api/experimental/session/{sessionID}/wait` с описанием «Wait for a session agent loop to become idle». Можно использовать как дешёвый барьер в eval.
- `opencode run --help` в 2.3.2 не показывает флага `--attach`. Доступны `--file, -f`, `--session, -s`, `--fork`, `--worktree, -w`, `--format json`, `--archived`, `--auto`. Формулировка ТЗ про `opencode run --attach` не соответствует живой версии. В карточках используется `--session`, `--worktree` и `--format json`.

## Порядок волн

Волна P0 создаёт production-решение и спавн-порт. Волна P1 добавляет гейты, выбор карточки и проверку политики. Волна P2 переносит worktree-логику в путь спавна и обновляет eval. OwnerFiles внутри волны не пересекаются. Файлы из `cmd/` не импортируют `internal/tui`.
