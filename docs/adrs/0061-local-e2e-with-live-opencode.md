# ADR 0061. Расширение e2e-тестов с реальным opencode для локальной проверки

Статус: accepted

## Контекст

Сейчас в `tests/e2e/e2e_test.go` пять записей в `e2eScenarios`: `repo_plugin_skeleton`, `mcp_go_sdk_CommandTransport`, `mcp_inspector_optional`, `opencode_wake_with_reference_plugin` и `negative_control_stub_handler_must_fail`. Отдельная функция `TestFrozenCrashTestScenarios` сверяет хеш набора. Замороженное значение `ec28944e71a04357e310438abcb4544034734a9ef3c424644da811d2b31563d8` записано в константе `frozenScenarioHash` и в `tests/e2e/README.md`. В CI джоба `e2e` выполняет `bash tests/e2e/run.sh` с `continue-on-error: true`, поэтому красный прогон не блокирует merge.

Владелец не принимает зелёный CI как доказательство. Ему нужен локальный прогон на своей машине с живым opencode, который закрывает весь цикл, а не только протокольный round trip. Текущий набор этого не даёт: `mcp_go_sdk_CommandTransport` проверяет `initialize`, `tools/list` и один `tools/call` на заглушках, `opencode_wake_with_reference_plugin` использует эталонный плагин из `fixtures`, а `docs/e2e/README.md` прямо перечисляет непроверенное: поведение обработчиков, демона опроса CI и репозиторийский адаптер.

Ограничение инструментов уже зафиксировано в ADR 0003 и `docs/e2e/README.md`: общение с MCP идёт через официальный go-sdk с `CommandTransport`, с живым сервером через `opencode api`, составитель протокола это MCP Inspector CLI. Свой JSON-RPC-клиент, `curl` и прямые stdio-кадры в прогоне не используются.

ADR 0060 на момент записи отсутствует в `docs/adrs/`. Флагов `--format json` и `--live` в коде нет. Поэтому сценарий 6 фиксируется как условный и входит в набор только после принятия ADR 0060.

ADR 0022 продолжает действовать для сборки, обычных тестов и гейтов. Для e2e вводится явное исключение: локальный прогон владельца считается приёмкой, а зелёный CI сам по себе приёмкой не считается. Менеджер код не пишет. Он готовит ветку и PR, читает вывод прогона и не подменяет его обещанием агента. Агент, пишущий код, обязан приложить к PR команду и фактический вывод, но не имеет права называть прогон пройденным без запуска.

## Проверенные ограничения opencode v2.3.0

Менеджер проверил команды 20 ноября 2026 на живой среде: opencode v2.3.0, сервис на `127.0.0.1:49374`. Результаты зафиксированы как факт и не зависят от того, как эти команды описаны в старых документах.

| Команда | Фактический результат |
| --- | --- |
| `opencode api app.agents` | `Operation not found: app.agents` |
| `opencode api 'GET /api/event'` | `Operation not found: GET /api/event` |
| `opencode api session.list` | Работает. JSON с полями `id`, `parentID`, `agent`, `model`, `title`, `time.busy`, `time.idle`, `time.outcome`, `location.directory` |
| `opencode agent list` | Подкоманды `agent` в CLI нет, команда печатает help |

В `docs/e2e/README.md:45` раньше было записано, что `opencode api GET /api/event` возвращает 0 байт. Проверка 20 ноября даёт `Operation not found`. Оба результата ведут к одному выводу: этот вызов непригоден как источник фактов и в новые сценарии не включается.

Следствия для проекта:

- Не проектировать e2e и CLI на `opencode api app.agents` и на `opencode api 'GET /api/event'`. Не использовать `opencode agent list`.
- `beacon watch` уже вызывает рабочий `opencode api session.list` в `cmd/beacon/watch.go:190`. Этот путь сохраняется.
- `beacon agents` сейчас вызывает `opencode api app.agents` в `cmd/beacon/agents.go:195`, то есть нерабочий путь. Сценарий 6 требует перевода команды на `session.list`, где поле `agent` присутствует в проверенном ответе, либо на прямой HTTP-запрос к сервису.
- Прямой HTTP к service.json с basic auth по `url` и паролю из registration-файла допускается как источник фактов для event-потока и списка агентов. Он идёт через существующий слой запуска внешних процессов, а не через новый протокольный клиент. В `cmd/beacon/main.go:42` `runner` это `func(dir, name string, args, env []string) runResult`, а `realRunner` в `:117-118` запускает дочерний процесс через `exec.Command`. HTTP-методов у этого `runner` нет, поэтому добавление HTTP-адаптера требует отдельного решения владельца. До него e2e берёт факт пробуждения из подписки плагина внутри живого opencode и из файлов состояний.

## Решение

Набор e2e расширяется семью решениями. Каждое из них содержит цель, негативный контроль, разрешённые инструменты и владельцев файлов. Негативный контроль обязателен: если подмена реального процесса, обработчика или вердикта оставляет прогон зелёным, сценарий не считается e2e.

Имена новых сценариев в `e2eScenarios`: `live_opencode_full_mcp_cycle`, `verdict_ready_gate_requires_shipfacts`, `wake_delivery_honest_and_no_double_prompt`, `needs_input_continue_of_same_watch`, `crash_recovery_durable_store_no_old_pid`, `cli_manager_smoke_against_opencode_service` после принятия ADR 0060. Сценарий 7 оформляется отдельной тестовой функцией `TestTwoPhaseLocalCIGate`, а не записью в `e2eScenarios`, чтобы не вызвать рекурсивный запуск `tests/e2e/run.sh` изнутри самого прогона.

Изменение набора требует одного коммита, который обновляет `e2eScenarios`, `frozenScenarioHash`, таблицу сценариев в `tests/e2e/README.md` и `docs/e2e/README.md`. Этот ADR является пересмотром решения по T3.89. Новое значение хеша записывается после фактического изменения списка и фиксируется в обоих файлах. Тихая правка только списка остаётся красной.

### 1. Полный MCP-цикл на живом opencode

`live_opencode_full_mcp_cycle` прогоняет `initialize`, `tools/list` с ровно семью инструментами из `mcp.ToolNames`, `beacon_watch_pr`, `beacon_wait_result`, `beacon_get_result`, `beacon_ack` и финальный отчёт агента по `mcp.CycleInstructions`. Восьмого инструмента `summary` нет: отчёт это строка завершения обхода, а не вызов MCP. `watchID`, ревизия и вердикт берутся из ответов реальных обработчиков поверх go-sdk `CommandTransport`, а не из `ok: true` стартовой заглушки. Процесс `opencode serve` поднимается отдельно, плагин грузится из `.opencode/plugins` и подтверждается через проверенный текущим e2e вызов `/api/plugin`.

Негативный контроль. Подмена реального обработчика заглушкой, возвращающей `ok: true` без записи в store, обязана уронить сценарий на шаге, где читается `watchID` или вердикт. Замена плагина на `fixtures/beacon-bridge-stub.js` обязана уронить шаг пробуждения. Если цикл доходит до отчёта после любой из этих подмен, сценарий не работает.

Инструменты. go-sdk `CommandTransport` для всех вызовов MCP, `opencode api` для `/api/session` и `/api/plugin`, файлы состояния как внешнее доказательство. MCP Inspector CLI применяется только в опциональном сценарии. Непроверенные операции `app.agents` и `GET /api/event` не используются.

Владельцы файлов. `tests/e2e/e2e_test.go`, `tests/e2e/README.md`, `tests/e2e/fixtures/beacon-bridge.js`, `cmd/beacon/mcp.go`, `internal/infra/mcp/server.go`, `internal/infra/mcp/tool_watch_pr.go`, `internal/infra/mcp/tool_wait_result.go`, `internal/infra/mcp/tool_get_result.go`, `internal/infra/mcp/tool_ack.go`.

### 2. Гейт вердикта ready

`verdict_ready_gate_requires_shipfacts` подтверждает, что `beacon_get_result` возвращает `verdict=ready` только при заполненных ShipFacts. Негативный контроль это регрессия T3.72 и T3.78: снимок с зелёными обязательными проверками, но без ShipFacts, обязан дать вердикт не `ready`. Проверка идёт через реальный `beacon_get_result` и реальный путь `domain.Evaluate`, а не через сравнение строк в тесте. Позитивный снимок с `Build`, `Audit` и `TDD` даёт `ready`.

Негативный контроль отдельно проверяет, что `Evaluate` вызывает `ShipAllowed`. Если убрать это условие, снимок без ShipFacts начнёт возвращать `ready` и сценарий станет красным. eval 09 в `evals/main.go` проверяет то же правило на уровне домена, e2e проверяет его на уровне ответа MCP-сервера.

Инструменты. go-sdk `CommandTransport` для `beacon_get_result`, `opencode api session.list` для факта живой сессии. Вердикт не подставляется в ответ тестом.

Владельцы файлов. `internal/domain/verdict.go`, `internal/infra/github/points.go`, `internal/infra/mcp/tool_get_result.go`, `evals/main.go`, `tests/e2e/e2e_test.go`.

### 3. Честная доставка wake и отсутствие дубля промпта

`wake_delivery_honest_and_no_double_prompt` различает `steer`, `queue` и `unsupported` по фактам, которые пишет реальный плагин внутри живого opencode. Сценарий требует `prompt.ok` с нужным `delivery`, запись `ctx-event` с `session.execution.started` для целевой сессии и квитанцию в `receipts`. Для `unsupported` ожидается отказ без ложного `prompt.ok`. Повтор записи того же указателя в `events.jsonl` не создаёт вторую доставку: в журнале плагина остаётся один `operationId`, а в `events.jsonl` одно событие исполнения с тем же `sessionID`. Это проверка T3.124 на реальном процессе.

Негативный контроль. Плагин, который всегда печатает `delivery=steer` независимо от фактического результата, обязан провалить сценарий. Двойная запись указателя, после которой появляется вторая доставка или второй `session.execution.started`, обязана провалить сценарий. Заглушка без вызова `session.prompt` обязана провалить сценарий.

Инструменты. `opencode api session.list`, `/api/session` и `/api/plugin` для фактов сервера, подписка `ctx.event.subscribe` внутри реально загруженного плагина, файлы `events.jsonl`, `plugin-events.jsonl` и `receipts.jsonl` как доказательства. Вызов `opencode api 'GET /api/event'` запрещён проверкой 20 ноября 2026. Факт пробуждения подтверждается подпиской плагина и квитанцией, а не выдуманным кадром SSE.

Владельцы файлов. `internal/infra/mcp/tool_wake.go`, `internal/infra/mcp/tool_wake_test.go`, `tests/e2e/fixtures/beacon-bridge.js`, `tests/e2e/e2e_test.go`, `docs/e2e/README.md`.

### 4. needs_input и продолжение в той же Watch

`needs_input_continue_of_same_watch` паркует задачу через `needs_input` и возвращает ответ человека в ту же Watch через `ack` с `continue_of`. `beacon_wait_result` после ответа продолжает ожидание той же задачи и не создаёт новую. Сценарий сверяет неизменный `watchID`, ожидаемую ревизию и отсутствие второй записи в `beacon_list_running`. Пробуждение агента идёт через живой opencode.

Негативный контроль. Путь, который после ответа создаёт новую Watch или теряет `continue_of`, обязан упасть. Сценарий обязан упасть и в том случае, если `wait_result` вернул результат по другому `watchID`. Ограничение числа раундов из T3.21 проверяется отдельной проверкой в том же сценарии.

Инструменты. go-sdk `CommandTransport` для `ack`, `wait_result` и `list_running`, `opencode api session.list` для факта сессии, `events.jsonl` для факта доставки.

Владельцы файлов. `internal/domain/transition.go`, `internal/infra/mcp/tool_wait_result.go`, `internal/infra/mcp/tool_ack.go`, `tests/e2e/e2e_test.go`.

### 5. Восстановление после краша

`crash_recovery_durable_store_no_old_pid` убивает процесс opencode во время `beacon_wait_result`, перезапускает его и читает результат из постоянного хранилища. Сценарий опирается на T3.44, T3.53 и T3.65: живость определяется по артефактам и PID без `ps`, старый lease после краша недействителен, сообщение из durable inbox возвращается после рестарта по `PEER_NAME`.

Негативный контроль. Результат, привязанный к старому PID, не читается. Старый lease не продолжает задачу. Если `get_result` после рестарта вернул результат по мёртвому PID или по недействительному lease, сценарий красный.

Инструменты. go-sdk `CommandTransport` для `wait_result` и `get_result`, `opencode api session.list` для проверки живого сервера после рестарта, файлы состояния в изолированном `XDG_STATE_HOME`. Свой протокольный клиент не пишется.

Владельцы файлов. `internal/infra/store/db.go`, `internal/infra/mcp/tool_list_running.go`, `internal/infra/mcp/tool_claim.go`, `tests/e2e/e2e_test.go`.

### 6. Smoke CLI-менеджера против живого сервиса

`cli_manager_smoke_against_opencode_service` включается в набор в том коммите, где принимается ADR 0060. Сценарий запускает реальный `opencode service`, выполняет `beacon agents --format json` и `beacon watch --live` и проверяет JOIN-таблицу по `sessionID` из `_meta`. Колонка `VERDICT` берётся из решения Beacon, а не из состояния `idle` OpenCode. На момент записи `cmd/beacon/agents.go` и `cmd/beacon/watch.go` уже есть в main, но `agents` вызывает нерабочий `app.agents`, а флагов `--format json` и `--live` ещё нет. Поэтому сценарий требует перевода `beacon agents` на рабочий путь из раздела «Проверенные ограничения» и принимается вместе с ADR 0060.

Негативный контроль. Если `beacon agents` снова вызовет `app.agents`, сценарий обязан упасть с `Operation not found`, а не выдать зелёный результат. Если подставить сессию в `idle` при вердикте Beacon `watching` или `ready`, CLI обязан показать вердикт Beacon. Если убрать строку из registration-файла, команда обязана завершиться с кодом 2 и напечатать команду запуска сервиса. Расхождение между строкой агента и задачей не должно молча пропадать из таблицы.

Инструменты. Сам бинарник `beacon` с указанными флагами, рабочий вызов `opencode api session.list`, `opencode service`, registration-файл `$XDG_STATE_HOME/opencode/service.json`. `app.agents`, `GET /api/event` и `opencode agent list` не используются. Новый Go-HTTP-клиент не добавляется без отдельного решения владельца, граница ADR 0006 и ADR 0024 не меняется.

Владельцы файлов. `cmd/beacon/agents.go`, `cmd/beacon/watch.go`, `tests/e2e/e2e_test.go`, ADR 0060 после появления.

### 7. Двухфазная проверка локально и в CI

`TestTwoPhaseLocalCIGate` фиксирует приёмку в две фазы. Локальная фаза обязательна: владелец запускает `bash evals/run.sh` и `bash tests/e2e/run.sh` на своей машине с живым opencode. Результат приёмки это `9 passed, 0 failed` в eval и PASS по каждому активному e2e-сценарию. `mcp_inspector_optional` при невключённом `BEACON_E2E_INSPECTOR` печатается как SKIP и в число PASS не входит. CI-фаза повторяет те же команды отдельной джобой и служит сигналом расхождения, а не заменой локального прогона.

Негативный контроль. Удаление или переименование сценария без обновления `frozenScenarioHash` делает `TestFrozenCrashTestScenarios` красным. Уборка ShipFacts из eval 09 делает eval красным. Зелёная джоба `e2e` с `continue-on-error: true` при красном локальном прогоне приёмкой не считается.

Инструменты. `bash evals/run.sh`, `bash tests/e2e/run.sh`, константа `frozenScenarioHash`, джоба `e2e` в `.github/workflows/ci.yml`. Функция не запускает `tests/e2e/run.sh` изнутри набора, чтобы не получить рекурсию.

Владельцы файлов. `evals/main.go`, `evals/run.sh`, `tests/e2e/e2e_test.go`, `tests/e2e/README.md`, `.github/workflows/ci.yml`.

## Последствия

- Активный набор `e2eScenarios` после реализации содержит десять сценариев: пять существующих и пять новых. После принятия ADR 0060 он содержит одиннадцать. `TestFrozenCrashTestScenarios` и `TestTwoPhaseLocalCIGate` остаются отдельными тестовыми функциями.
- Значение `frozenScenarioHash` меняется в том же коммите, что и список сценариев. Новое значение записывается в `tests/e2e/e2e_test.go` и `tests/e2e/README.md`. Пересмотр решения зафиксирован этим ADR.
- Приёмка это локальный прогон владельца плюс eval 9/9. ADR 0022 не отменяется для сборки, обычных тестов и гейтов менеджера.
- В каждом PR на реализацию сценария агент приводит команду прогона и вывод с именами сценариев. Отсутствие негативного контроля или замена реального процесса заглушкой блокирует PR.
- `docs/e2e/README.md` получает строки о новых источниках фактов, о запрете `app.agents` и `GET /api/event` через `opencode api` и о том, что зелёный CI не является приёмкой.
- Описание `beacon agents` в ADR 0029 и карточка T3.11 называют нерабочий вызов `app.agents`. Этот факт зафиксирован здесь. ADR 0029 не переписывается, но конкретная команда в реализации заменяется на проверенную.
- ADR 0003, 0022, 0024, 0026, 0029, 0031, 0042 и карточки T3.21, T3.44, T3.53, T3.65, T3.72, T3.78, T3.89, T3.124 не переписываются. Новые сценарии проверяют их поведение на реальных процессах.

## Отклонённое

INTERNAL-stub вместо живого opencode. Заглушка внутри процесса теста, фейковый хост в `tests/adapter/steer-prompt.test.js` и подмена `$ctx.session.prompt` массивом вызовов подтверждают только работу самого теста. Сценарии этого ADR требуют отдельный процесс `opencode serve`, реально загруженный плагин и внешние файлы доказательств.

Самописный JSON-RPC-клиент. Прямые кадры по stdio, `curl`, `fetch` и собственный HTTP-клиент на Go повторяют работу go-sdk и `opencode api` и не дают нового доказательства. Разрешены go-sdk `CommandTransport`, MCP Inspector CLI, проверенные вызовы `opencode api` и прямой HTTP к service.json через существующий слой запуска внешних процессов. Чтение `events.jsonl`, квитанций и файлов состояния этим запретом не считается, потому что не является протокольным клиентом.

Мокнутые вердикты. Подстановка `ready` в ответ `get_result`, хардкод строки вердикта в тесте и замена `domain.Evaluate` таблицей ожидаемых значений делают гейт вердикта бессмысленным. Вердикт обязан вычисляться реальным кодом и приходить через реальный инструмент.

Нерабочие команды opencode. `opencode api app.agents`, `opencode api 'GET /api/event'` и `opencode agent list` отклонены проверкой 20 ноября 2026. Сценарии и CLI не проектируются под них, даже если в старых документах эти вызовы указаны.

Зелёный CI как единственное доказательство. Джоба `e2e` работает с `continue-on-error: true`, поэтому её зелёный статус не говорит о локальном прогоне с живым opencode. Приёмка без локального запуска владельца недействительна.

Сценарии без негативного контроля. Прогон, который остаётся зелёным после подмены плагина, обработчика или вердикта, не расширяет уверенность и в набор не принимается.
