# Аудит непротиворечивости ADR и кода

Дата: 2026-11-21.
Ветка: main, коммит `d3b1bfb1` (`feat(gate): offline tools/list contract check (RS-MCP-02) (#843)`).
Объём: 15 ADR из задания, индекс `docs/adrs/README.md`, 303 файла в `docs/adrs/`.
Метод: чтение текста ADR, поиск реализаций по символам и путям, сверка с `internal/infra/mcp/server.go`, `internal/infra/mcp/contract_test.go`, `tools/beacon-gates/gate/config/layers.json`, `cmd/beacon/catalog.go` и `git log --all`.
Исключено: каталоги `worktrees/*` и общая проверка здоровья документации. Здесь рассматриваются только противоречия между ADR и кодом или между самими ADR.

Главный вывод: индекс ADR содержит одновременно принятые решения с разными контрактами. Три ADR задают разное число инструментов MCP, а принятый ADR 0060 запрещает свой TUI при наличии кода TUI в main.

## Итоговая таблица, топ-10

| ADR | Предписание | Статус в коде | Противоречие | Приоритет |
|---|---|---|---|---|
| 0263, 0094, 0268 | Контракт MCP из 7, 8 или 9 имён | В main 8 имён: `server.go:106-115`, `contract_test.go:42-49` | `README.md:313` и `README.md:123` помечают оба ADR как accepted. AGENTS.md и код говорят про восемь. ADR 0268 про девять живёт только в ветках `fix/*` | P0 |
| 0060 | Свой TUI и веб-интерфейс не создаются, `attach` уходит в обычный TUI OpenCode | `internal/tui/app.go`, `client.go`, `socket.go` есть в main. Ребро `cmd` к `internal/tui` запрещено в `layers.json:17-22`. Dispatch `board` отсутствует | WIRING-493 и WIRING-915 прямо требуют снять ограничения ADR 0060 и 0094. Восемь ADR про TUI со статусом proposed duplicated друг друга | P0 |
| 0026, 0059 | Wake возвращает `delivery`, heartbeat.release только при смене состояния или открытом гейте | `tool_wake.go` содержит `Delivery*`, `NewHeartbeatGuard`, `ToRepairableErrorShape`. `AttachWake` вызывается только в тестах | `NewServer` не вызывает `AttachWake`, а ADR 0263 запрещает регистрацию. Живая проверка из ADR 0026 недостижима на main | P1 |
| 0086, 0089, 0091, 0094, 0095 | Каждый номер закреплён за одним решением | Индекс содержит по две accepted-строки на каждый номер | Ссылка «ADR 0094» неоднозначна: это и `steer`/`queue`, и восемь инструментов. Аналогично 0086, 0089, 0091, 0095 | P1 |
| 0213 | Вопрос это версионированная строка с operation id и явным reply path | `operationId` встречается только в wake (`tool_wake.go:342`). Кода `question_changed` нет | `docs/evals/w21-human-question-revision.md` описывает проверку, но реализации в main нет | P1 |
| 0214 | Seeded regression: пара healthy/seeded, покрытие одними impl-тестами не считается | Нет fault controller, seed-механизма и каталога `test/system/scenarios` | Карточка T3.342 остаётся `todo` с блокером. Принятый ADR не имеет реализации | P1 |
| 0016 | Один бинарник `beacon` с подкомандами build, test, gates, list, doctor, what | Dispatch содержит `test` (`main.go:242`), но `catalog.go` его не перечисляет | Агент читает каталог через `beacon list --format json` и не видит `test`, хотя ADR называет каталог источником команд | P2 |
| 0009 | Единый error shape из шести полей, `Result[T]` не добавляется | `ToErrorShape` возвращает шесть полей, `statusByCode` совпадает с таблицей ADR, `Result[T]` отсутствует | `RepairableErrorShape` в `tool_wake.go:392` расширяет конверт до девяти полей. Второй конверт в том же пакете | P2 |
| 0005, 0022 | capabilities-гейт и проверка только в CI | Гейт `capabilities` зарегистрирован (`tools/beacon-gates/main.go:25`), CI лежит в `.github/workflows` | Противоречий не найдено. ADR 0059 явно сохраняет границу ADR 0022 | Нет |
| Ссылки ADR | Пути в ADR существуют | 18 ADR содержат ссылки на отсутствующие файлы | 0030, 0035, 0039, 0040, 0041, 0045, 0057, 0060, 0062, 0063, 0070, 0072 | P2 |

## Детали по ключевым ADR

### 0005, архитектурные гейты

Предписывает слои с зависимостями внутрь, два гейта импортов через `go list` и AST, capabilities-гейт и посаженные нарушения. Реализовано в `tools/beacon-gates`: `layers.go`, `capabilities/`, `config/layers.json`, `config/permanent.json`. Противоречий с кодом нет.

### 0009, обработка ошибок

Предписывает `(value, error)`, общий `AppError`, преобразование в единый shape на границе MCP и шесть полей ответа. `ToErrorShape` в `schema.go:393` соблюдает контракт. `StatusForCode` даёт 400, 404, 409, 429, 410, 503 по таблице ADR. Напряжение возникает из-за `RepairableErrorShape`: wake добавляет `retryable`, `recoveryHint` и `partial`. Это расширение оговорено в комментарии, но создаёт второй формат ошибки внутри `internal/infra/mcp`.

### 0016, билд система

Предписывает stdlib-бинарник `beacon-gates` и команды beacon, включая `test` и машиночитаемый каталог. Гейты и `beacon build` работают. Команда `test` есть в dispatch, но отсутствует в `catalog.go`. Для ADR 0016 это расхождение: каталог не полон.

### 0022, проверка только в CI

Компиляция, тесты и гейты не запускаются локально. AGENTS.md повторяет правило. `cmd/beacon/checkall.go` реализует `beacon ci check`, который читает открытые PR через GitHub API, а не запускает локальную сборку. ADR 0059 прямо сохраняет эту границу. Противоречий нет.

### 0026, честная доставка follow-up

Требует обязательное поле `delivery` со значениями `steer`, `queue`, `unsupported` и живой прогон с агентом, вернувшим `queued`. Код реализует доставку в `tool_wake.go`, включая fallback и heartbeat guard. Но `AttachWake` не подключён к `NewServer`, поэтому инструмент недоступен через MCP. ADR 0263 запрещает его регистрацию до отдельного решения.

### 0059, шесть фич из AQ-волны

Предписывает блокирующее ожидание, guarded heartbeat, отдельный вердикт «зелёный, но заблокирован», `/fix-checks` на том же worktree, FIFO-очередь в одном файле и verified completion. Следы есть: `IssueTaskHandle` в `domain/identity.go`, `EventAccept` и `acceptWatch` в `domain/transition.go`, `NewHeartbeatGuard` в `tool_wake.go`, `taskstore/store.go`. Heartbeat живёт внутри нерегистрируемого wake, поэтому своей точки входа на main не имеет.

### 0060, граница CLI-адаптера и живой контур

Принятый ADR прямо пишет: «Свой TUI или веб-интерфейс не создаётся». Clone `beacon watch --live`, `spawn`, `deliver`, `attach` реализованы в `cmd/beacon`. При этом в main есть `internal/tui/app.go`, `client.go`, `socket.go` и тесты к ним. WIRING-493 говорит, что слушатель TUI не подключён, а снятие ограничений ADR 0060 и 0094 требует решения владельца. WIRING-915 добавляет вторую ветку: локальный board на Bubble Tea, команда не зарегистрирована, ребро `cmd` к `internal/tui` в матрице запрещено. Закрывающий коммит WIRING-915 (`f5b1d7d4`) не является предком main. Восемь ADR про TUI имеют статус proposed и повторяют два текста: 0092, 0123, 0156, 0191 и 0105, 0136, 0169, 0204.

### 0086, ACP и реестры

Два файла с номером 0086. `mcp-beacon-wake.md`: один MCP-сервер, тонкие адаптеры доставки, Beacon не вызывает API OpenCode и не становится вторым мостом. `project-scoped-agent-registry.md`: авторский `.beacon/agents.yaml` и выведенное состояние рядом с `beacon.db`. Код re registry живёт в `internal/infra/registry/`, CLI-часть в `cmd/beacon/registry_*.go`. Три реестра из первого ADR в явном виде не найдены. Второй ADR задаёт `capability index`, он реализован в `index.go` и `index_check.go`.

### 0089, capability index

Предписывает производный индекс из roster executors и двустороннюю проверку. Реализовано в `internal/infra/registry/index.go` и `index_check.go`. Одинаковый номер у ADR `spoolsource-mcppullsource.md`, который описывает два других источника указателей. Противоречие только в нумерации.

### 0091, проверенный останов

Предписывает precondition по watchID, revision, sessionID и fencing token, отказ `selection_changed`, неопределённый исход `stop_uncertain`. Реализовано в `cmd/beacon/registry_stop.go`. Одинаковый номер у ADR `mcp.md` про отсутствие гарантии пробуждения в MCP.

### 0094, контракт инструментов и delivery

`seven-tools.md` называется «Восемь инструментов ядра», закрепляет восемь имён и отдельное решение для девятого. `delivery-steer-queue.md` задаёт режимы приёма ввода. В main восемь имён, контрактный тест сравнивает отсортированный список. ADR 0263 противоречит восьмёрке. ADR 0268 в ветках добавляет девятое имя `beacon_task_claim` и поднимает бюджет с 16 KiB до 20 KiB.

### 0095, stale и CLI opencode api

`restart-reconcile.md` предписывает статус `stale` при расхождении базы и живого сервера, запрет wake по stale, идемпотентную команду `beacon registry sync` без таймера на Watch. Реализовано в `cmd/beacon/registry_sync.go`, включая `classifyStale`. Второй ADR с тем же номером описывает синтаксис `opencode api`. ADR 0060 уточняет, что обёртка ограничена и не годится для SSE.

### 0213, вопрос как ревизионная строка

Предписывает версионированную строку, operation id, reply path, отказ при изменении вопроса и идемпотентный повтор. В коде есть `operationId` для wake, но нет `question_changed` и reply path. Eval `w21-human-question-revision.md` описывает ожидаемое поведение без реализации.

### 0214, seeded regression

Предписывает пару сценариев для общих путей, причём покрытие одними impl-тестами не считается. В main нет fault controller, seed-механизма и каталога сценариев. Карточка T3.342 зафиксировала блокер 2026-11-21. ADR принят без реализации.

### 0263, граница MCP

Утверждает ровно семь имён и объясняет, что `AttachWake` не вызывается, а `beacon_claim` не входит в реестр. Часть про невызов `AttachWake` совпадает с кодом. Часть про семь имён не совпадает: `ToolNames` содержит восемь. ADR загружен коммитом `1ddf475e`, а ADR 0094 приведён к восьми инструментам коммитом `8a2fac98`. Оба датированы 2026-11-20.

### 0268, девятый инструмент

Файла нет в main. ADR существует в коммите `1d10c03f` и в ветках `fix/*`. Он расширяет контракт до девяти имён и меняет бюджет `tools/list`. Слияние любой из этих веток сделает main несовместимым с ADR 0094, ADR 0263 и AGENTS.md.

## Устаревшие ссылки на удалённые файлы и пакеты

Проверка извлекает пути в обратных кавычках из всех ADR и сверяет их с деревом main. Записи с подстановочными знаками и имена MCP-методов `tools/list`, `tools/call` отброшены.

| ADR | Отсутствующие пути |
|---|---|
| 0030 | `internal/infra/mcp/tool_observe.go` |
| 0035 | `.opencode/agents/night-shift-actor.md` |
| 0039 | `internal/infra/ci/bitbucket.go`, `github.go`, `gitlab.go`, `status.go`, `internal/infra/ports/registry.go` |
| 0040 | `internal/infra/github/events.go` |
| 0041 | `tools/call` |
| 0045 | `cmd/beacon/why.go` |
| 0057 | `internal/infra/gates/gates.go`, `internal/infra/store/leases.go`, `threads.go` |
| 0060 | `cmd/beacon/opencode_bridge.go`, `cmd/beacon/opencode_bridge_test.go` |
| 0062 | `internal/infra/store/tool_ledger.go` |
| 0063 | `internal/app/eval/runner.go`, `internal/infra/mcp/tool_acp_wait.go`, `internal/infra/store/batch_queue.go` |
| 0070 | `internal/infra/wake/deliverer.go`, `deliverer_test.go`, `opencode.go`, `opencode_test.go` |
| 0072 | `internal/domain/mergepolicy.go`, `internal/infra/merge/executor.go`, `registry.go` и тесты |

Некоторые пакеты заменены другими именами. Так, ADR 0060 обещал `opencode_bridge.go`, а в main лежит `opencode.go`. ADR 0070 обещал `wake/deliverer.go`, а в main есть `wake/deliver.go`. Читатель ADR по этим путям не найдёт код.

## Что сделать владельцу

1. Выбрать один контракт MCP и привести к нему ADR 0094, ADR 0263, AGENTS.md и `contract_test.go`. Для девяти имён нужен перенос ADR 0268 в main с пересмотром обоих принятых ADR.
2. Разрешить или запретить свой TUI. При разрешении нужно снять запрет из ADR 0060, закрыть дубли proposed ADR про TUI и подключить WIRING-493 и WIRING-915 к main. При запрете нужно удалить `internal/tui` и закрыть обе wiring-карточки как dropped.
3. Развести дубли номеров ADR, начиная с 0086, 0089, 0091, 0094, 0095. Пока номера дублируются, любая ссылка на ADR требует уточнения имени файла.
4. Решить судьбу wake. Либо зарегистрировать инструмент и выполнить живой тест ADR 0026, либо перенести ADR 0026 и ADR 0059 в состояние с явной оговоркой, что доставка живёт вне MCP.
5. Обновить путь `test` в `cmd/beacon/catalog.go` и ссылки на удалённые файлы в двенадцати ADR из таблицы выше.
