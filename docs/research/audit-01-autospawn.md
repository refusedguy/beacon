# Аудит 01. Полный автоспавн сабагентов на уровне Beacon и OpenCode

Дата: 2026-11-22. Режим: чтение кода и живого API, запись только двух файлов этого аудита в своём worktree. Ветка `fix/autospawn-audit-dfe7` от `origin/main` 80d3b0ce. Код не менялся. `go test`, `go build`, `beacon gates` и инструменты `tools.beacon.*` не запускались.

## Метод и baseline

Baseline снят поиском по явным каталогам и живым контрактом API, без нагрузочного прогона.

- `rg -n "auto-spawn|autoSpawn|AutoSpawn|autospawn" cmd internal` дал 0 совпадений. Команда автоспавна в daemon не существует.
- `rg -n "\.Burn\(" cmd internal --glob '!**/*_test.go'` дал 0 совпадений. `SlotLedger.Burn` не вызван ни одним рабочим кодом.
- `rg -n -i "gemini" internal/infra/wake cmd/beacon` дал 0 совпадений. Раннер Gemini в реестре ACP отсутствует.
- Живой OpenAPI снят командой `opencode api GET /openapi.json` через запись адреса и пароля из `$XDG_STATE_HOME/opencode/service.json`. Спецификация `openapi: 3.1.0`, сервер версии 2.3.2. Слово `prompt_async` встречается 0 раз, поля `delivery` и `resume` присутствуют.
- `DefaultSlotsTotal` равен 4 по `internal/domain/orchestrator.go:17`. Период ReconcileLoop по умолчанию 30 секунд зафиксирован в `GLOSSARY.md:52`.

Оценки RAM и токенов в таблице помечены как оценки. Замер нагрузки не снимался, поэтому числа задают порядок величины и служат опорой для выбора, а не отчётом об измерении.

## Цепочка, подтверждённая по коду

1. Задача выдаётся инструментом `beacon_task_claim`. Имя зафиксировано в `internal/infra/mcp/tool_task_claim.go:34`, константа `TaskClaimToolName`. Обработчик `TaskClaimHandler.Handle` лежит в `:101`, метод `Claim` в `:111`. Рабочая реализация `FileTaskClaimStore` определена в `:125`, метод `(*FileTaskClaimStore).Claim` в `:160`. Блокировка `syscall.Flock` стоит в `:189`. Выбор карточки идёт в `:215-228` по первому `todo` в списке, отсортированном по ID в `:259`. Дополнительно проверяется только `handoffOf(c).Validate()` в `:220`. Порт подключён в `cmd/beacon/mcp.go:739`, поле `TaskClaimStore`. Графа, индекса, квоты, лимита слотов и проверки пересечения OwnerFiles внутри claim нет. Привязки watchID к cardID на этом шаге нет.

2. Проверка запуска живёт в CLI. Команда `beacon task check-launch` разбирается в `cmd/beacon/task.go:67-68`, реализация `runTaskCheckLaunch` в `:648`. Сервис `CheckLaunch` определён в `internal/app/taskcli/service.go:351`, сравнение границ вызывает `IntersectingFiles` в `:354`, функцию `patternsConflict` в `:342`. Метод `(*Service).PlanLaunch` читает карточки и пропускает статусы `done` и `dropped` в `:392`, затем вызывает `CheckLaunch` в `:397`. Проверка требует ручного списка ID и не вызывается ни из spawn, ни из daemon.

3. Слоты считаются чистым механизмом. Сигнал `SignalReportToOrchestrator` объявлен в `internal/app/orchestrator/decision.go:515`. Типы `SlotArtifact`, `SlotToken` и `SlotLedger` лежат в `:517`, `:530` и `:591`. Методы `Issue`, `Burn` и `IsLive` находятся в `:604`, `:629` и `:650`. Ключ токена выводится из пары watchID и revision в `:543`. В daemon регистр подключён через `daemonWakeGate` в `cmd/beacon/daemon.go:140-147`, слот открывается в `open` на `:158-160`, условие проверяется в `:167-175`. Длительная защита живёт в `claimConsumed` на `:152-154`. Вызовов `Burn` вне тестов нет, поэтому токен после удачной доставки никогда не гаснет.

4. Запуск агента живёт в `cmd/beacon/spawn.go`. Порт `sessionSpawner` описан в `:31-34`, переменная `spawnSessionSpawner` в `:38`. Метод `(*CLI).runSpawn` начинается в `:143`. Переключение runtime идёт в `:165-173`, ветка `acp` вызывает `runSpawnACP` из `:425`. Обычная ветка требует переменную `BEACON_PARENT_SESSION` в `:175-179`, создаёт сессию через `CreateSession` в `:193`, шлёт промпт в `:204` и записывает реестр в `:209`. ACP-ветка захватывает durable run через `ClaimACPRun` в `:435`, создаёт или загружает сессию в `:477-501`, шлёт промпт в `:516`. Реестр раннеров `acpRuntimes` перечисляет opencode, claude, codex и pi в `internal/infra/wake/acp.go:425-430`. Gemini в нём нет. HTTP-клиент создаёт сессию телом из `parentID`, `agent`, `title` и `metadata` в `internal/infra/opencode/client.go:342-365`. Метод `Prompt` шлёт только поле `text` в `:370-381`.

5. Сигнал завершения волны выдаётся в `internal/infra/cibridge/verdict_applier.go`. Функция `terminalNotice` формирует текст в `:472-477`, включая строку `nextAction: report_to_orchestrator`. Функция `notifyOrchestratorTerminal` описана в `:486`. Адрес берётся из привязанной сессии оркестратора в `:490-497`, дедупликация идёт по обязательству в `:502-507`, доставка в `:522-532`. Потребителем сигнала сейчас является человек или LLM-оркестратор, который читает wake и вручную спавнит нового агента. Код, который превращает этот текст в запуск, отсутствует.

6. Фоновый цикл не содержит запуска агентов. `ReconcilePass` определён в `cmd/beacon/mcp.go:1914`, мост настраивается в `:1921-1933`, вызов `bridge.Refresh` стоит в `:1934`. `ReconcileLoop` начинается в `:1954`, один проход делается в `:1960`, интервал выбирается в `:1963-1967`, запуск под блокировкой poller идёт в `runReconciler` на `:1978`. Команда `beacon up` описана в `cmd/beacon/up.go:138-142`, её флаги перечислены в `:152-154`, флага `--auto-spawn` нет. Порты демона в `cmd/beacon/daemon.go:500-529` содержат CheckAll, Subscribe, Orchestrate и Summary, но не содержат Spawn. Регистрация Watch демону запрещена в `:484-487`.

## Таблица находок

| Проблема | Текущие file:line и символ | Целевое решение | Эффект: RAM, токены, надёжность | Карточка |
| --- | --- | --- | --- | --- |
| Auto-мерж пропускает условия политики. На `VerdictReady` проверяется только `MergePolicy.AutoMerge` | `internal/infra/cibridge/verdict_applier.go:106-109` `applyVerdict`; `internal/domain/types.go:244-249` `MergePolicy`; `internal/app/orchestrator/orchestrator.go:146-181` `canAutoMerge` | Проверять `RequireApproval`, `WaitFor` и `WaitForMain` до вызова `applyMerge`, держать PR на `wait` через резолвер, не полагаться на отдельный вызов `orchestrator.Run` | Токены: не тратится ход агента на преждевременно залитый PR. Надёжность: устранён обход политики обязательного одобрения | AUTOSP-01 |
| Промпт созданной сессии не возобновляет её и не управляет доставкой | `internal/infra/opencode/client.go:370-381` `(*Client).Prompt`; живой OpenAPI `POST /api/session/{sessionID}/prompt` с полями `id`, `delivery`, `resume` | Добавить в body `id` с префиксом `msg_`, `delivery:"steer"` и `resume:true`. Транспорт `opencode api` из `internal/infra/wake/deliver.go` уже шлёт resume | Токены: сессия продолжает ход вместо полного перестарта контекста. Надёжность: wake не теряется в закончившей сессии | AUTOSP-02 |
| Нет раннера Gemini, унификация ACP не полна | `internal/infra/wake/acp.go:425-430` `acpRuntimes`, `:477` `NewWorkerRuntime`, `:756` `DeliverWake` | Добавить запись Gemini с собственной командой запуска и оставить override через `BEACON_ACP_COMMAND` и `BEACON_ACP_ARGS` из `cmd/beacon/spawn.go:306-330` | RAM: один раннер на слот, лимит 4 по `DefaultSlotsTotal`. Надёжность: единый путь запуска для пяти агентов вместо отдельного адаптера | AUTOSP-03 |
| Цикл завершения не порождает следующий spawn | `internal/infra/cibridge/verdict_applier.go:472-477` `terminalNotice`; `cmd/beacon/mcp.go:1954` `ReconcileLoop`; `cmd/beacon/daemon.go:500-529` `DaemonPorts` | Добавить порт `AutoSpawner` в цикл и флаг `--auto-spawn` в `beacon up`. Выбор карточки через индекс и граф без циклов, запись инцидента при нарушении | Токены: экономится один полный ход LLM-оркестратора на каждую карточку. RAM: на один агент меньше в рабочем цикле, оценка. Надёжность: слот заполняется в том же ходе | AUTOSP-04 |
| Claim выбирает первую карточку по алфавиту и не знает зависимостей | `internal/infra/mcp/tool_task_claim.go:215-228` цикл выбора, `:220` `handoffOf`, `:259` `cards` | Добавить выбор по приоритету фазы и зависимостям графа, исключить `dropped`, отклонить кандидата с неразрешёнными `depends-on` | Токены: агент не получает задачу с неготовым переходом. Надёжность: цикл отвергается на записи по `AGENTS.md:51` | AUTOSP-05 |
| Launch-гейт живёт только в ручной CLI-команде | `internal/app/taskcli/service.go:351` `CheckLaunch`, `:368` `PlanLaunch`; `cmd/beacon/task.go:648` `runTaskCheckLaunch` | Вызывать сервис из автоспавнера. Добавить проверку пересечения с активными `in_progress`, квоту `PreflightGuard` и лимит слотов `CanAcceptTask` | Токены: не запускается агент при исчерпанной квоте. Надёжность: гейт перестаёт зависеть от человека | AUTOSP-06 |
| Токен слота не гаснет в рабочем коде | `internal/app/orchestrator/decision.go:629` `Burn`; `cmd/beacon/daemon.go:158-160` `open`; 0 вызовов `Burn` вне тестов | Подключить `ArtifactVerifier` к `Burn` в daemon. Артефактом служит PR или PLAN.md, результат пишется в observe | RAM: слоты не протекают, лимит 4 остаётся реальным. Надёжность: повторный сигнал не открывает второй spawn | AUTOSP-07 |
| Go-путь создания worktree не соблюдает HD-011 | `cmd/beacon/agents.go:964` `worktreeAddArgs`, `:1003` вызов `git worktree add`; `docs/tasks/HD-011.md:1-13`; `.opencode/plugins/beacon-bridge.js` `checkFetchBeforeWorktree` | Вызывать `git fetch origin main` до `worktree add` в `bindRepoWorktree`, использовать существующее правило `FETCH_BEFORE_WORKTREE_RULE`, не писать новый проверяльщик | Надёжность: ветка не срезается со stale ref. Токены: меньше неудачных rebase у каждого нового агента | AUTOSP-08 |
| Связь cardID и watchID выводится из agentID | `cmd/beacon/mcp.go:537` `claimedCardForNotify`, `:593-602` `db.Link`; `internal/infra/store/card_links.go:72-109` `Link` | Передавать cardID явно из ответа claim в отдельном поле вызова, сохранить `AgentCardLookup` как запасной путь. Атомарность уже есть в `Link` | Надёжность: связь не рвётся, когда runtime выдаёт sessionID, отличный от agentID | AUTOSP-09 |
| Нет eval на цикл без ручного пинка | `evals/main.go:45,73` реестр eval; `docs/research/wake-via-prompt-api.md:106-134` план проверки wake | Добавить сценарий с терминальным вердиктом и проверкой, что новый spawn появился без сообщения владельца | Токены: измеряется число ходов оркестратора на волну вместо оценки. Надёжность: регресс ловится в CI | AUTOSP-10 |
| Нет ограничения по памяти перед spawn | `internal/infra/server/metrics.go:406` `FootprintCheck`, `:401` `rss_bytes` | Вызывать `FootprintCheck` в автоспавнере до запуска раннера, при превышении порога удерживать слот и записать событие в observe | RAM: предотвращается рост числа раннеров сверх footprint. Надёжность: daemon не умирает от нехватки памяти | AUTOSP-11 |
| Решение и источники чисел не зафиксированы | `docs/research/audit-01-autospawn.md` этот файл; `docs/adrs/README.md` индекс ADR | Добавить ADR 0306 и 0307 пакетом, зафиксировать владельцев чисел в `facts` | Надёжность: последующий агент видит решение и источник, а не пересказ | AUTOSP-12 |

## Живой контракт OpenCode

Проверено через `opencode api GET /openapi.json` с записью адреса и пароля из `$XDG_STATE_HOME/opencode/service.json`. Версия сервера 2.3.2.

- `POST /api/session`, операция `session.create`, принимает `id`, `parentID`, `agent`, `title`, `model`, `location`, `metadata` и `permissions`. Это совпадает с телом `CreateSession` из `internal/infra/opencode/client.go:343-353`. Отдельный headless-порт писать не нужно.
- `POST /api/session/{sessionID}/prompt`, операция `session.prompt`, принимает `id`, `text`, `files`, `agents`, `skills`, `metadata`, `delivery` и `resume`. Обязателен только `text`.
- Поля `prompt_async` в живой спецификации нет. Вариант с `prompt_async` из задания не подтвердился. Нужен обычный `prompt` с `delivery:"steer"` и `resume:true`.
- Флага `--attach` у `opencode run` версии 2.3.2 нет. Есть `--session`, `-s`, `--continue`, `--fork`, `--worktree`, `--standalone` и `--server`. Вариант эфемерного `opencode run --attach` тоже не подтвердился. Для daemon пригодится `opencode run --session <id>` или HTTP-пара create и prompt.
- ACP-путь уже реализован в `internal/infra/wake/acp.go`. Раннеры opencode, claude, codex и pi перечислены в `:425-430`. После добавления Gemini он станет пятым.

## Что уже есть и не надо писать заново

- `newReadOnlyOpencodePort` в `cmd/beacon/opencode.go:151` читает только registration и не ставит fallback-стартер. Вызов в `cmd/beacon/mcp.go:1708` уже использует его.
- `FileTaskClaimStore` в `internal/infra/mcp/tool_task_claim.go:125` и `FileTaskCloseStore` в `internal/infra/mcp/tool_task_close.go:141` дают атомарный claim и close под файловым замком.
- `card_links` в `internal/infra/store/card_links.go:20-25` с методом `Link` в `:72` уже даёт одну строку на карточку через `INSERT OR IGNORE` и одного writer. Привязка в `cmd/beacon/mcp.go:593-602` готова к явному cardID.
- Формат ATX-R01 поддерживается регулярным выражением `letterDashIDRE` в `internal/infra/taskstore/store.go:100`.
- `WorkerRuntime`, `NewWorkerRuntime` и `DeliverWake` находятся в `internal/infra/wake/acp.go:400`, `:477` и `:756`. Это общий путь для всех ACP-раннеров.
- Локальная доска живёт в `internal/tui/app.go:12`, тип `App`, и `internal/tui/board.go:18`, тип `Card`. Сетей и БД там нет.
- `FootprintCheck` в `internal/infra/server/metrics.go:406` уже публикует `rss_bytes`.
- `PreflightGuard` в `internal/infra/github/points.go:433`, методы `Check` и `Send` в `:441` и `:458`, читают живую квоту и отказывают при `quota_exhausted` и `quota_unknown`.
- Правило HD-011 уже реализовано в `.opencode/plugins/beacon-bridge.js` функцией `checkFetchBeforeWorktree`. В Go-пути `cmd/beacon/agents.go:1003` оно не подключено. Это wiring, а не новая проверка.

## Пакет для batch-apply

Файл `docs/research/audit-01-batch.json` содержит 12 карточек, 2 ADR, 1 eval и 12 фактов. ID карточек идут от AUTOSP-01 до AUTOSP-12. owner_files داخل волны не пересекаются. Каждый путь существует на `origin/main` 80d3b0ce. Типы ограничены множеством task, doc и eval, фазы P0, P1 и P2, статусы только todo. Слои соблюдены: файлы `cmd` остаются в `cmd`, файлы `internal` остаются в `internal`, импорт `cmd` в `internal/tui` не вводится.
