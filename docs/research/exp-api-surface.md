# Карта experimental HTTP API OpenCode и возможности beacon-адаптера

Дата: 2026-11-21.

Метод. Режим только чтение. Источники: `https://opencode.ai/docs/openapi.json` и `https://opencode.ai/docs/api`, полученные через webfetch, и поиск `rg` по явным каталогам `cmd/beacon/`, `internal/infra/opencode/`, `internal/infra/wake/`, `internal/infra/mcp/`, `docs/adrs/`, `docs/research/`. Код, конфиги и тесты не менялись. `go test`, `go build`, `beacon gates` и инструменты `tools.beacon.*` не запускались. Проверки вживую не было, поэтому все утверждения ниже относятся к спецификации и к исходникам адаптера, а не к поведению запущенного сервера.

## 1. Что называется experimental

Страница API открывается фразой «Experimental HttpApi surface for selected instance routes». Документ описывает 173 операции и 518 схем из OpenAPI 3.1. Есть два разных смысла слова experimental.

Первый смысл относится ко всему HTTP API. Ручки `/api/session/*`, `/api/event`, `/api/mcp` и другие живут в этом же документе и помечены как experimental на уровне продукта.

Второй смысл относится к путям с префиксом `/api/experimental/`. Их описания прямо предупреждают об изменениях без гарантий совместимости. Это `/api/experimental/session/{sessionID}/wait`, `/api/experimental/session/{sessionID}/log`, `/api/experimental/session/{sessionID}/instructions/entries/*`, `/api/experimental/mcp/*`, `/api/experimental/client-mcp*`, `/api/experimental/persistent-pty/*`. Приоритет ниже там, где у операции есть не-experimental сосед.

## 2. Ручки по группам

### 2.1 Отправка сообщений

| operationId, метод и путь | Что делает | Параметры | Зачем beacon |
| --- | --- | --- | --- |
| `session.prompt`, POST `/api/session/{sessionID}/prompt` | Принимает один ввод в сессию и планирует шаг агента, если `resume` не false | Путь `sessionID`; тело: `text` обязательно, `id`, `files`, `agents`, `skills`, `metadata`, `delivery` (`steer` или `queue`), `resume`. Ответ `data: Session.Inbox.User` с `id` | Основной путь записи сейчас. Поле `delivery` даёт управление режимом, `id` даёт ключ для идемпотентности. ADR 0075 фиксирует контракт с `delivery: steer` |
| `session.synthetic`, POST `/api/session/{sessionID}/synthetic` | Принимает синтетический ввод и планирует шаг, если `resume` не false | Путь `sessionID`; тело: `text` обязательно, `id`, `description`, `metadata`, `delivery`, `resume`. Ответ `data: Session.Inbox.Synthetic` | Wake от CI, Beacon и других агентов не должен выглядеть как текст человека. Синтетическое сообщение даёт атрибуцию и сохраняет Audit trail в истории сессии |
| `experimental.session.skill`, POST `/api/experimental/session/{sessionID}/skill` | Добавляет сообщение skill и возобновляет выполнение | Путь `sessionID`; тело задаёт skill | Позволяет поднять навык на сессии без промпта с текстом. Полезно для wake, который несёт только команду активации |

### 2.2 Human in the loop

| operationId, метод и путь | Что делает | Параметры | Зачем beacon |
| --- | --- | --- | --- |
| `session.form.list`, GET `/api/session/{sessionID}/form` | Возвращает ожидающие формы одной сессии | Путь `sessionID` | Точка опроса для human gate конкретного вотча |
| `form.list`, GET `/api/form` | Возвращает ожидающие формы локации | Query `location.directory` | Одна точка опроса на все сессии локации. Заменяет обход сессий при поиске задач оператору |
| `session.form.create`, POST `/api/session/{sessionID}/form` | Создаёт форму для сессии | Путь `sessionID`; тело `Form.CreatePayload`: `title` и `fields` обязательны, плюс `id` и `metadata`. Поля бывают string, number, integer, boolean, multiselect, external | Structured answer вместо свободного текста. Закрытый набор `approve`, `reject`, `rework`, `unblock`, `pause`, `resume`, `cancel` из `cmd/beacon/human.go:34-49` ложится в `multiselect` |
| `session.form.get`, GET `/api/session/{sessionID}/form/{formID}` | Возвращает форму и её состояние `pending`, `answered` или `cancelled` | Пути `sessionID`, `formID` | Проверка состояния перед применением решения. Защищает от повторного ответа |
| `session.form.reply`, POST `/api/session/{sessionID}/form/{formID}/reply` | Отправляет ответ в ожидающую форму | Пути `sessionID`, `formID`; тело `Form.Reply` с полем `answer` | Переносит решение оператора из `beacon human` в сессию без вставки текста в терминал |
| `session.form.cancel`, DELETE `/api/session/{sessionID}/form/{formID}` | Отменяет форму и может передать причину | Пути `sessionID`, `formID`; query `message` | Причина отказа становится частью протокола. Сейчас причина живёт только в журнале Beacon |

Соседняя группа разрешений (`session.permission.reply`, `permission.request.list`) решает похожую задачу для tool calls. Она не разобрана подробно, потому что в задаче перечислены формы, а не разрешения.

### 2.3 Wake и wait

| operationId, метод и путь | Что делает | Параметры | Зачем beacon |
| --- | --- | --- | --- |
| `experimental.session.wait`, POST `/api/experimental/session/{sessionID}/wait` | Ждёт, пока цикл агента станет idle. Ответ 204, ошибка 503 при недоступности | Путь `sessionID` | Даёт детерминированную точку ожидания вместо сна по интервалу. ADR 0078, 0095, 0126, 0159 и 0194 упоминают ручку как доступную через CLI, но в коде её нет |
| `session.inbox.list`, GET `/api/session/{sessionID}/inbox` | Перечисляет недоставленную работу in order enqueue. Типы: user, synthetic, compaction, move | Путь `sessionID` | Показывает, что уже в очереди сессии. Позволяет не дублировать wake и видеть отложенные элементы |
| `session.inbox.update`, PATCH `/api/session/{sessionID}/inbox/{inboxID}` | Меняет режим доставки на `steer` или `queue`. Описание говорит, что steering будит выполнение | Пути `sessionID`, `inboxID`; тело `{delivery}` | Уже используется как promote в `internal/infra/wake/deliver.go:900`. Расширение даёт управление режимом без нового промпта |
| `session.inbox.cancel`, DELETE `/api/session/{sessionID}/inbox/{inboxID}` | Отменяет ещё не доставленный элемент. Недоступные элементы официально дают no-op | Пути `sessionID`, `inboxID` | Позволяет снять устаревший wake без перезапуска сессии. Идемпотентность описана в спецификации |
| `session.interrupt`, POST `/api/session/{sessionID}/interrupt` | Прерывает выполнение сессии | Query `resume` | Уже вызывается через `opencode api` в `internal/infra/wake/deliver.go:888` |

### 2.4 Log follow

| operationId, метод и путь | Что делает | Параметры | Зачем beacon |
| --- | --- | --- | --- |
| `experimental.session.log`, GET `/api/experimental/session/{sessionID}/log` | Читает долговременный журнал событий сессии после эксклюзивной последовательности и продолжает живыми событиями при `follow=true` | Путь `sessionID`; query `after` (курсор), `follow` (`true`/`false`). Ответ `text/event-stream`, кадры `id`, `event`, `data` со схемой `SessionLogItem` | Даёт воспроизводимое чтение после разрыва. `/api/event` начинается с `server.connected` и не проигрывает прошлое, что зафиксировано в ADR 0095. Для вотчей это разница между потерей события и восстановлением |
| `event.subscribe`, GET `/api/event` | Подписка на SSE-события по типам | Query `types` через запятую, в enum более ста типов, включая `session.execution.*`, `session.inbox.*`, `form.*`, `tui.*`, `mcp.status.changed` | Уже используется в `internal/infra/opencode/client.go:418`. Клиент сам переподключается и просит caller перечитать snapshot |

Схема `SessionLogItem` объединяет `Session.Event.Durable` и `EventLog.Synced`. В `Session.Event.Durable` перечислены `session.inbox.enqueued`, `session.instructions.updated`, `session.revert.staged`, `session.execution.succeeded` и другие устойчивые факты. Это подходящий источник для журнала вотча.

### 2.5 Session spawn и fork

| operationId, метод и путь | Что делает | Параметры | Зачем beacon |
| --- | --- | --- | --- |
| `session.create`, POST `/api/session` | Создаёт сессию. `parentID` создаёт дочернюю сессию локации родителя | Тело: `id`, `parentID`, `title`, `agent`, `model`, `location`, `metadata`, `permissions`. Описание говорит, что ребёнок шлёт `<subagent>` report в inbox родителя и будит его, если нет staged revert | Уже используется с `parentID`, `agent`, `title`, `metadata` в `internal/infra/opencode/client.go:345` |
| `session.fork`, POST `/api/session/{sessionID}/fork` | Создаёт сессию из точки истории | Путь `sessionID`; тело `{before?: messageID}` | Позволяет повторить шаг с границы сообщения, сохранив историю родителя. Сейчас повтор упирается в перезапуск или новый промпт |
| `session.update`, PATCH `/api/session/{sessionID}` | Меняет `title`, `metadata`, `permissions`, `archived` | Путь `sessionID`; тело с любым из четырёх полей | Даёт архивацию seссий завершённых вотчей без обращения к store напрямую |
| `session.active`, GET `/api/session/active` | Возвращает foreground drains, которыми владеет процесс. Отсутствие в ответе означает неактивность | Нет | Признак живой сессии вместо догадки по списку сессий. Полезно для watch live и диагностики |

### 2.6 Управление MCP

| operationId, метод и путь | Что делает | Параметры | Зачем beacon |
| --- | --- | --- | --- |
| `mcp.list`, GET `/api/mcp` | Список настроенных серверов и статус соединения | Query `location.directory` | Видеть, подключен ли MCP Beacon к OpenCode, без чтения конфига |
| `experimental.mcp.add`, PUT `/api/experimental/mcp/{server}` | Добавляет или заменяет сервер и сразу подключает | Путь `server`; query `location.directory`; тело `{config}` типа local или remote | Подключать инструменты Beacon к конкретной локации во время работы. Сейчас конфиг меняется на диске и требует перезапуска |
| `experimental.mcp.remove`, DELETE `/api/experimental/mcp/{server}` | Останавливает сервер и убирает его из рантайм-набора до рестарта | Путь `server`; query `location.directory` | Снимать инструменты вотча после завершения работы |
| `experimental.mcp.connect`, POST `/api/experimental/mcp/{server}/connect` | Подключает сервер, временно перекрывая disabled-конфиг | Путь `server`; query `location.directory` | Возвращать сервер без правки конфига |
| `experimental.mcp.disconnect`, POST `/api/experimental/mcp/{server}/disconnect` | Отключает сервер и убирает его инструменты | Путь `server`; query `location.directory` | Ограничивать доступ к инструментам на время шага |
| `mcp.resource.catalog`, GET `/api/mcp/resource` | Каталог ресурсов и шаблонов подключённых серверов | Query `location.directory` | Проверять, какие ресурсы доступны агенту |
| `experimental.mcp.client.token`, POST `/api/experimental/client-mcp/token` | Одноразовый тикет на WebSocket client MCP | Query `name` обязательно, `location.directory`; header `x-opencode-ticket` | Первый шаг к подключению Beacon как MCP-сервера со стороны клиента |
| `experimental.mcp.client`, GET `/api/experimental/client-mcp` | Поднимает WebSocket, на котором клиент размещает MCP-сервер с именем `name`. Инструменты живы, пока открыт сокет | Query `name` обязательно, `ticket`, `location.directory`. Ответ 200 boolean, `x-websocket: true` | Альтернатива собственному MCP-серверу Beacon. Спецификация прямо называет маршрут experimental без гарантий совместимости |

Важное различие. `cmd/beacon/mcp.go` и `internal/infra/mcp/` реализуют собственный MCP-сервер Beacon для агентов. Ручки этого раздела управляют MCP-серверами внутри OpenCode. Это два разных объекта. ADR 0074 и 0091 описывают неудачный спайк с событием `experimental.mcp.event`. Этот факт не доказывает неработоспособность перечисленных ручек, но требует измерения перед выбором.

### 2.7 session.instructions

| operationId, метод и путь | Что делает | Параметры | Зачем beacon |
| --- | --- | --- | --- |
| `experimental.session.instructions.entry.list`, GET `/api/experimental/session/{sessionID}/instructions/entries` | Список записей, которыми управляет API | Путь `sessionID`; ответ `data: InstructionEntry.Info[]` | Сверять контекст вотча с тем, что реально видит модель |
| `experimental.session.instructions.entry.put`, PUT `/api/experimental/session/{sessionID}/instructions/entries/{key}` | Добавляет или заменяет одну запись. Изменение объявляется на следующей границе шага | Пути `sessionID`, `key`; тело `{value}`, ошибка 413 при большом значении | Класть cards, AGENTS.md и enable-условия вотча в долговременный контекст сессии без сообщения в очередь |
| `experimental.session.instructions.entry.remove`, DELETE `/api/experimental/session/{sessionID}/instructions/entries/{key}` | Удаляет запись, объявление на границе шага | Пути `sessionID`, `key` | Снимать устаревший контекст при смене фазы вотча |

ADR 0069 описывает тот же механизм со стороны плагина через `ctx.session.instructions.entry.put`. В коде адаптера HTTP-ручки не используются. Поиск по `internal/infra/mcp/` нашёл только `Instructions` в handshake MCP, а не записи в сессию.

### 2.8 TUI и board

| operationId, метод и путь | Что делает | Параметры | Зачем beacon |
| --- | --- | --- | --- |
| `session.view`, POST `/api/session/{sessionID}/view` | Отмечает наблюдаемый переход в idle как просмотренный | Путь `sessionID`; тело `{idle: number}` | Даёт серверу факт, что агент-наблюдатель обработал idle, и убирает ложные «нужна реакция» |
| `session.message.list`, GET `/api/session/{sessionID}/message` | Постраничный список сообщений | Query `limit`, `order`, `cursor`, `types` | Источник заголовков и статусов для строк board без чтения SQLite Beacon |
| `experimental.persistentPty.read`, GET `/api/experimental/session/{sessionID}/terminal/read` | Читает последние физические строки терминала, не меняя выбор и не беря управление. Возвращает null, если терминала нет | Путь `sessionID`; query `lines` | Структурированная замена `capture-pane -p` из `internal/infra/wake/capture.go:23-37` |
| `experimental.persistentPty.list`, GET `/api/experimental/session/{sessionID}/terminal` | Список терминалов сессии | Путь `sessionID` | Видеть, есть ли живой терминал для восстановления |
| `experimental.persistentPty.create`, POST `/api/experimental/session/{sessionID}/terminal` | Создаёт постоянный терминал сессии | Путь `sessionID`; тело `PersistentPty.CreateInput` | Резервный канал ввода, когда tmux недоступен |
| `experimental.persistentPty.handoff`, POST `/api/experimental/persistent-pty/handoff` | Возвращает метаданные передачи терминала | Нет | Перехват терминала при смерти агента. Рядом есть `shutdown`, `get`, `update`, `remove`, `snapshot`, `connectToken`, `connect` |
| event types `tui.prompt.append`, `tui.command.execute`, `tui.toast.show`, `tui.session.select` | События в `event.subscribe`. Несут текст ввода, команду TUI, текст тоста и целевой `sessionID` | Доступны через query `types` в `/api/event` | Доставка уведомлений и команд в открытый TUI вместо вставки в tmux. `tui.prompt.append` меняет поле ввода, а не сохраняет сообщение, поэтому не заменяет `session.prompt` |

У OpenCode нет ручки board. Board в Beacon строится из локального хранилища карточек в `cmd/beacon/board.go:27-72`. Ручки этого раздела нужны не для замены хранилища, а для обмена данными с открытым TUI.

## 3. Сопоставление с текущим адаптером

| Канал | Где в коде | Какие ручки использует |
| --- | --- | --- |
| Go HTTP-клиент | `internal/infra/opencode/client.go:301` | GET `/api/agent` |
| | `internal/infra/opencode/client.go:315` | GET `/api/session` |
| | `internal/infra/opencode/client.go:345` | POST `/api/session` |
| | `internal/infra/opencode/client.go:373` | POST `/api/session/{id}/prompt` с полем `text` |
| | `internal/infra/opencode/client.go:388` | GET `/api/config` |
| | `internal/infra/opencode/client.go:418` | GET `/api/event?types=...` |
| Health check | `internal/infra/opencode/server.go:33` | GET `/api/config` |
| CLI `opencode api` | `internal/infra/wake/deliver.go:327` и `:868` | POST `/api/session/{id}/prompt` |
| | `internal/infra/wake/deliver.go:888` | POST `/api/session/{id}/interrupt?resume=` |
| | `internal/infra/wake/deliver.go:900` | PATCH `/api/session/{id}/inbox/{inboxID}` |
| CLI по operationId | `cmd/beacon/watch.go:98`, `:213`, `:244`; `cmd/beacon/agents.go:216`, `:303` | `session.list` |
| ACP JSON-RPC | `internal/infra/wake/acp.go:74`, `:808` | `session/prompt`. Рядом `session/new`, `session/load`, `session/cancel`. Это другой протокол, не HTTP |
| Композиция | `cmd/beacon/opencode.go:14-19`, `:118-129` | Только порты и установка клиента. Новых ручек нет |

Что не вызывается нигде в трёх целевых каталогах: `session.synthetic`, `experimental.session.wait`, `experimental.session.log`, все `session.form.*` и `form.list`, `session.fork`, `experimental.session.instructions.entry.*`, управление MCP, `experimental.persistentPty.*`, `session.view`, tui-события.

Текущая human-in-the-loop в Beacon живёт вне HTTP API OpenCode. `cmd/beacon/human.go:28-94` задаёт закрытый набор исходов и пишет решение через проекцию или store. Wake использует tmux `capture-pane` и проверку ввода в `internal/infra/wake/capture.go`, `verify_input.go` и `paste.go`. Формы OpenCode и чтение терминала через persistent PTY дают альтернативу этим двум механизмам.

## 4. Итоговая таблица

Ручка | Сейчас в beacon | Можно добавить | Приоритет и ценность
--- | --- | --- | ---
`session.prompt` | Да, Go-клиент и CLI fallback | `delivery`, `id` для идемпотентности, вложения | Высокая. Уже работает, осталось использовать поля контракта из ADR 0075
`session.synthetic` | Нет | Атрибуция wake и CI-сообщений | Высокая. Убирает маскировку под человека, сохраняет след в истории
`experimental.session.wait` | Нет, упомянуто только в ADR | Блокирующее ожидание idle в вотче | Высокая. Заменяет сон по интервалу, делает цикл детерминированным
`experimental.session.log` | Нет | Чтение с курсора `after` и `follow=true` | Высокая. Восстановление после разрыва SSE, в отличие от `/api/event`
`session.form.*` и `form.list` | Нет | Structured human gate с причиной отказа | Высокая. Типизированный ответ вместо вставки текста в терминал
`session.inbox.list` и `session.inbox.cancel` | Частично, только PATCH promote | Список и отмена отложенных wake | Высокая. Меньше дублей и возможность снять устаревший wake
`session.instructions.entry.*` | Нет по HTTP, есть описание плагина в ADR 0069 | Контекст вотча как долговременные записи | Средняя. Убирает зависимость от плагина и от порядка хуков
`session.fork` | Нет | Повтор с границы сообщения | Средняя. Полезно для retry без потери истории родителя
`session.active` | Нет | Признак живого drains | Средняя. Улучшает watch live и диагностику
`experimental.mcp.*` и `client-mcp*` | Нет. Beacon имеет свой MCP-сервер | Динамическое подключение инструментов к локации | Средняя. Нужно измерение из-за experimental и старого неудачного спайка
`experimental.persistentPty.read` и `terminal/*` | Нет, есть tmux capture | Структурированное чтение экрана и перехват терминала | Средняя. Менее хрупкий ввод, чем `capture-pane`
tui-события через `/api/event` | Нет | Тосты и ввод в открытый TUI | Средняя. Работает только при живом TUI, не заменяет prompt
`session.message.list` | Нет | Строки board из истории сессии | Низкая. Локальный board уже есть в `cmd/beacon/board.go`
`session.view` | Нет | Отметка просмотренного idle | Низкая. Улучшает точность состояния, не меняет протокол вотча
`experimental.session.skill` | Нет | Активация навыка как wake | Низкая. Узкий сценарий
`session.update` | Нет | Архивация завершённых сессий | Низкая. Задача решается локальным store

## 5. Ограничения и порядок внедрения

Спецификация прочитана 2026-11-21. Маршруты с префиксом experimental могут измениться без гарантий совместимости, особенно client MCP и persistent PTY.Перед переносом в код нужно измерить конкретный вызов через `opencode api <operationId>` на живом сервисе.

Порядок по отдаче. Сначала `experimental.session.wait` и `experimental.session.log`, поскольку они снимают главную неопределённость вотча. Затем `session.synthetic` и inbox list/cancel для корректной доставки wake. Затем формы как типизированный human gate. Остальные ручки требуют отдельного измерения и не должны менять текущие порты в `cmd/beacon/opencode.go` без причины.

В отчёте нет проверки вживую, вывода тестов и замеров метрик. Он описывает контракт и точки роста адаптера.
