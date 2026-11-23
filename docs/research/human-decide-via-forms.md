# Human-decide через экспериментальный Forms API OpenCode

Дата: 2026-11-21. Формат: read-only исследование. Код, тесты и гейты не запускались.

Источники контракта: [API docs](https://opencode.ai/docs/api) и [полный openapi.json](https://opencode.ai/docs/openapi.json), спецификация `openapi: 3.1.0`, `info.version: 0.0.1`, заголовок `opencode HttpApi`, описание `Experimental HttpApi surface for selected instance routes`. Документация инструмента `question`: [Tools](https://opencode.ai/docs/tools/). Исходники Beacon: `beacon-impl` на момент исследования.

Контракт перепроверен по локальному снимку `/tmp/opencode/api-probe-2520653/openapi.json` от 2026-11-20. Все шесть form-роутов, схемы `Form.*` и события совпадают с web-спецификацией. Старый локальный checkout `/home/nbook/opencode` версии 1.18.22 и его `packages/sdk/openapi.json` форм не содержат. Установленный бинарник `/home/nbook/.bun/bin/opencode` сообщает версию 2.3.2, но его живой API в этом исследовании не проверялся.

## 1. Контракт form-ручек

Все роуты объявлены в `paths`. Отдельные роуты сессии лежат в теге `session` и помечены как experimental session routes. Общий список pending форм лежит в теге `form`.

| Ручка | operationId | Вход | Успех | Ошибки |
| --- | --- | --- | --- | --- |
| `GET /api/session/{sessionID}/form` | `session.form.list` | `sessionID` path, string | `200 {"data": Form.Info[]}` | 400, 401, 404 (SessionNotFound \| LocationNotFound) |
| `POST /api/session/{sessionID}/form` | `session.form.create` | body `Form.CreatePayload` | `200 {"data": Form.Info}` | 400, 401, 404, 409 ConflictError |
| `GET /api/session/{sessionID}/form/{formID}` | `session.form.get` | `sessionID` path, `formID` path `Form.ID` | `200 {"data": Form.Detail}` | 400, 401, 404 (SessionNotFound \| FormNotFound \| LocationNotFound) |
| `DELETE /api/session/{sessionID}/form/{formID}` | `session.form.cancel` | `sessionID`, `formID`, query `message?: string` | `204` без тела | 400, 401, 404, 409 FormAlreadySettledError |
| `POST /api/session/{sessionID}/form/{formID}/reply` | `session.form.reply` | `sessionID`, `formID`, body `Form.Reply` | `204` без тела | 400 (FormInvalidAnswerError \| InvalidRequestError), 401, 404, 409 FormAlreadySettledError |
| `GET /api/form` | `form.list` | query `location.directory?: string \| null`, style deepObject | `200 {"location": Location.PublicRef, "data": Form.Info[]}` | 400, 401, 404 LocationNotFound |

Описания из спецификации: list session forms возвращает pending формы сессии; create создаёт форму сессии; get отдаёт форму вместе с текущим состоянием; cancel отменяет ожидающую форму и может передать причину через `message`; reply отправляет ответ на ожидающую форму; `GET /api/form` перечисляет pending формы локации.

### Схемы

`Form.CreatePayload`: `title: string` и `fields: Form.Fields` обязательны, `id?: Form.ID | null`, `metadata?: Form.Metadata`. `Form.Fields` это массив минимум из одного `Form.Field`.

`Form.Field` это union шести типов: `string`, `number`, `integer`, `boolean`, `multiselect`, `external`. Общие поля каждого варианта: `key` обязателен, `type` обязателен, дальше `title?`, `description?`, `required?`, `hidden?`, `when?: Form.When[]`.

- `string`: `format?` из `email | uri | date | date-time`, `minLength?`, `maxLength?`, `pattern?`, `placeholder?`, `default?`, `options?: Form.Option[]`, `custom?`.
- `number` и `integer`: `minimum?`, `maximum?`, `default?`. Числа кодируются как number или как строки `Infinity`, `-Infinity`, `NaN`.
- `boolean`: `default?: boolean`.
- `multiselect`: `options: Form.Option[]` обязателен, плюс `minItems?`, `maxItems?`, `custom?`, `default?: string[]`.
- `external`: `url: string` обязателен, плюс `title?`, `description?`.

`Form.When`: `key`, `op` из `eq | neq`, `value`. Это условное отображение поля по ответу на другое поле.

`Form.Option`: `value` и `label` обязательны, `description?`.

`Form.Info`: `id`, `sessionID`, `title`, `fields`, `metadata?`. `Form.Detail` добавляет `state`.

`Form.State` это union трёх состояний: `{"status":"pending"}`, `{"status":"answered","answer":Form.Answer}`, `{"status":"cancelled","message?"}`. Поле `message` в cancelled не обязательно.

`Form.Answer` это объект, ключи которого совпадают с `key` полей формы, значения имеют тип `Form.Value`: string, number, boolean, `string[]`, плюс строковые коды `Infinity`, `-Infinity`, `NaN`. `Form.Reply` это `{"answer": Form.Answer}`. `Form.ID` это строка по шаблону `^frm_`. `Form.Metadata` это произвольный object без описанных полей.

События: `form.created` несёт `data.form: Form.Info`, `form.replied` несёт `data: {id, sessionID, answer}`, `form.cancelled` несёт только `data: {id, sessionID}`. Причину отмены из `message` событие не несёт, её надо читать через `GET .../form/{formID}`. Все три события есть и в `@opencode/event`, и в V2Event. Описание `POST /api/location/reload` прямо говорит, что pending permissions и forms при перезагрузке локации отменяются.

### Что форма не делает

`POST .../form` создаёт объект и, судя по контракту, не останавливает выполнение сессии. Документация инструмента `question` говорит обратное: `question` приостанавливает выполнение, показывает вопросы клиенту, клиент должен поддерживать интерактивную форму, а отказ клиента отменяет вопрос. То есть форму создаёт сам агент через встроенный `question`, а человек отвечает через reply. Ответ возвращается агенту как результат вызова инструмента. Отдельный вызов HTTP create не приостанавливает агента и не меняет состояние Watch в Beacon.

## 2. Текущий human-decide в Beacon

### Как задаётся вопрос

В `beacon_wake` есть флаги `hasPendingQuestion` и `pendingApproval` (`internal/infra/mcp/tool_wake.go:241-248`, `474-476`, `1147-1150`). При любом из них wake возвращается до попытки доставки (`tool_wake.go:773`). Это прерывание ожидания из ADR 0028, а не сам вопрос.

`beacon_wait_result` на статусе `needs_input` паркуется и возвращает `waitStopReason=input-required`, не помечая обязательство прочитанным (`internal/infra/mcp/tool_wait_result.go:1247-1256`). Продолжение приходит через `ack(continueOf)` на том же Watch.

В CLI вопрос задаётся как свободный текст одной строкой: `beacon task needs-input ... --question "..."`. Команда пишет поле `Вопрос:` в карточку и выставляет `Состояние: needs_input` (`cmd/beacon/task.go:490-551`). Структурированного вопроса, вариантов ответа и обязательных полей нет.

Вопрос дочернего агента проецируется в родительский чат через `ParentNotice` и `AskChildQuestion` (`internal/infra/mcp/server.go:325-368`, `internal/app/session/delegation.go:209-244`). Отдельный MCP-инструмент для этого не добавляется.

Внимание человека привлекают тремя путями. Первый: observe-события `escalate_human`, `executor_orphaned`, `blocker_opened` уходят в Telegram и ntfy через `NotificationSink` (`internal/infra/wake/notify.go:12-35`, `cmd/beacon/up.go:115-131`). Второй: колонка `ATTENTION` в `beacon watch` и `beacon agents` строится из `attention-requests.json` (`cmd/beacon/watch.go:329-404`, `cmd/beacon/agents.go:599-621`). Третий: `PendingApproval` в серверном сторе, который читает `ApprovalAuthenticator` (`internal/infra/mcp/meta.go:87-155`).

### Как приходит ответ

Закрытый набор исходов `humanOutcome`: approve, reject, rework, unblock, pause, resume, cancel (`cmd/beacon/human.go:28-49`). `parseHumanResponse` отклоняет неизвестный verb и пустую цель (`human.go:81-94`). Ответ это verb, target и произвольная заметка `--note`.

`beacon human` сначала идёт в HTTP через `httpapi.NewProjectionClient.Decide` и `POST /api/v1/human/decide` (`internal/infra/httpapi/server.go:693`), при отсутствии сервера падает в локальный SQLite порт (`cmd/beacon/human.go:281-313`). Telegram-бот и channel ingress идут в тот же `upDecisionPort` (`cmd/beacon/up.go:281-337`). Слой cmd не открывает сокеты сам.

`humanDecisionSink.Apply` пишет событие `human_decide` в журнал Watch и использует его seq как `DecisionID` (`cmd/beacon/mcp.go:754-777`). Дальше решение превращается в `AckInput.Decision` и `DecisionID` (`cmd/beacon/human.go:183-213`).

В `beacon_ack` решение обрабатывается так. `Decision` это закрытый набор `approve` и `reject`, плюс отдельное семейство `blocked:not_mine` и `blocked:resolved`, которые не возобновляют агента (`internal/infra/mcp/tool_ack.go:58-106`). `DecisionRegistry` хранит одно решение на Watch: первое побеждает, повтор с тем же значением идемпотентен, другое значение отклоняется (`tool_ack.go:200-254`). `applyDecision` требует статус `needs_input` и один раз применяет `domain.ContinueEvent` через `resumeWatch` (`tool_ack.go:628-658`, `847-868`). `blocked:not_mine` берёт sessionID из `_meta` запроса, собирает failed checks и ставит блок (`tool_ack.go:660-742`). `blocked:resolved` снимает блок (`tool_ack.go:744-814`). Путь `continueOf` для rework и resume расходует один из пяти раундов (`tool_ack.go:340-381`, `879-906`).

Аутентификация решения отделена от данных клиента. `ApprovalAuthenticator` берёт sessionID из `_meta["ai.opencode/sessionID"]`, проверяет `decisionID` по серверному списку `PendingApprovals` и игнорирует присланный снимок (`internal/infra/mcp/meta.go:87-155`).

### Связанные ADR

ADR 0058 п4 фиксирует «уведомление с approve или reject и возобновление остановленного агента» и прямо отделяет решение от самого вопроса ADR 0028. ADR 0058 п6 добавляет маршрутизацию внимания. ADR 0031 п6 задаёт закрытые исходы human gate, ADR 0041 добавляет unblock, pause и cancel. ADR 0028 описывает флаги вопроса и прерывание `wait_result`. ADR 0091 задаёт проверенный останов с expected watchID, revision, sessionID и fencing token. ADR 0006 и 0024 ограничивают адаптер OpenCode и один мост на протокол, ADR 0029 закрепляет модель CLI-менеджера поверх уже запущенного сервиса OpenCode.

## 3. Сравнение и применимость

### Что даёт Forms API

Структурированный вопрос. Поля с типами, обязательностью, описаниями, вариантами `options`, регулярками и ограничениями длины или диапазона. Условные поля через `when` позволяют менять форму по предыдущему ответу. `metadata` позволяет положить в форму opaque-идентификаторы Beacon.

Ответ и отмена как отдельные операции. `reply` имеет один шаг settled: повторный ответ получает 409. `cancel` принимает причину и оставляет её в состоянии `cancelled`. Причина может отсутствовать. Состояние формы читается через `get`.

Обнаружение ожидающих вопросов. `GET /api/form` отдаёт все pending формы локации в одном ответе вместе с `Location.PublicRef`. `GET /api/session/{id}/form` сужает список до сессии. События `form.created`, `form.replied`, `form.cancelled` дают тот же поток через SSE.

### Чего Forms API не даёт

Форма не меняет Watch, не ставит claimedAt, не возобновляет агента, не хранит failed checks, не знает про obligation, wake ladder и лимит в пять раундов продолжений. `form.reply` закрывает форму в OpenCode, но оставляет Beacon Watch в `needs_input`. Всё это по-прежнему делает `beacon_ack`.

`form.reply` не несёт `_meta` Beacon с sessionID. Сейчас аутентификация решения опирается на этот sessionID и на server-side `PendingApprovals`. При переходе на форму эту проверку надо восстанавливать на границе Beacon: читать форму через `get`, брать из `metadata` ожидаемый decisionID и ожидаемого ревьюера, сверять их с pending list и только потомзывать `Decide`.

Форма привязана к sessionID. `beacon up` работает без OpenCode-сессии, это прямо сказано в комментарии к `runUp` (`cmd/beacon/up.go:117-119`). Чтобы создать форму, нужен sessionID воркера из Watch subscriber или уведомления, и сессия должна существовать на сервере. Иначе create вернёт 404.

### Можно ли перевести human-decide на форму

Да, но как дополнительный канал ввода, а не как замену механизма решения. Рабочая схема следующая.

1. Воркер задаёт вопрос встроенным инструментом `question`. OpenCode создаёт форму. Beacon видит её через `form.created` или через `GET /api/form` и связывает `formID` с Watch и obligation через `metadata` или через свою таблицу соответствий.
2. Менеджер видит форму в клиенте OpenCode и отвечает через `form.reply`. Ответ возвращается воркеру как результат `question`.
3. Параллельно Beacon читает `form.replied`, проверяет answer и decisionID по pending list и вызывает существующий путь `beacon_ack` с `decision=approve` или `reject`. Watch возобновляется по ADR 0058.

Альтернативная схема, где Beacon сам вызывает `POST .../form`, хуже. Форма создастся, но агент не остановится, потому что create не приостанавливает выполнение. Придётся оставить `wait_result` парковку по-прежнему и дублировать вопрос в два места. Тогда форма становится только UI-обвязкой над текущим вопросом, и выигрыш сводится к валидации ответа.

Вариант «агент получает вопрос через форму и отвечает через reply» в буквальной формулировке не соответствует ролям API. В OpenCode вопрос через форму видит человек, а reply отправляет человек. Агент получает уже ответ. Для Beacon это значит, что форма может заменить канал «вопрос человеку и ответ от человека», но не может заменить `beacon_ack` как переход Watch.

Что можно взять без отказа от текущего контракта: сделать тип формы с полем `decision` типа string и options `approve`, `reject`, `blocked:not_mine`, `blocked:resolved`, `rework`, `resume`, `cancel`, плюс поле `note` типа string. В `metadata` положить `watchID`, `obligationID`, `revision`, `decisionID`, `expectedReviewerSessionID`. Строковые options лучше числовых: `Form.Value` кодирует особые числа как строки `Infinity` и `NaN`, что усложняет разбор на Go.

## 4. Риски и совместимость

Golden контракт MCP. В `internal/infra/mcp/contract_test.go` зафиксированы ровно девять инструментов, размер `tools/list` 18212 байт и SHA-256 `ba0a9f42031cab8ff4733f89a8f6b3d1424be695367cbb6f305a3a0b60865f99`. Любой десятый инструмент или изменение описания `beacon_ack` ломает побайтовую проверку. Form-мост надо делать через HTTP-адаптер в `internal/infra/opencode`, не через новый MCP-инструмент. Eval-кейсы `02_catalog`, `r10-seven-tools` и `e02-seven-tool-contract` тоже считают число инструментов.

Evals. `e05-wait-pending-question` требует `hasPendingQuestion=true` и видимость вопроса в родительском чате. `w21-human-question-revision` требует отклонять ответ со старой revision как `question_changed` и отправлять reply в asking seat один раз. `r03-plain-vs-blocked-ack` требует, чтобы `blocked:not_mine` ставил блок, а plain ack не менял verdict. Мост обязан сохранить эти наблюдаемые поведения или их надо явно переписать в тех же кейсах. Поведение `question_changed` сегодняшний ack path не покрывает, это отдельный риск привязки формы к revision.

ADR. Изменение семантики approve, reject или resume задевает ADR 0058 п4 и ADR 0031 п6. Добавление новых исходов задевает ADR 0041. Снятие проверки sessionID из `_meta` задевает ADR 0034 п2 и модель `ApprovalAuthenticator`. Это требует нового ADR или явной правки существующих, а не тихой замены кода. ADR 0006, 0024 и 0029 задают границу адаптера и модель запуска сервиса, новый HTTP-клиент в `internal/infra/opencode` в эту границу вписывается.

Стабильность API. Спецификация экспериментальная, версия `0.0.1`, form-роуты помечены как experimental. Контракт надо зафиксировать в тестах адаптера и оставить текущий human-decide работающим как fallback. Удаление старого пути создаст зависимость от нестабильного API.

Жизненный цикл. `location.reload` отменяет pending формы. `form.cancelled` не несёт `message`, причину надо дочитывать через `get`. Повторный reply даёт 409, но это защита формы, а не Watch. `DecisionRegistry` и `BlockedRegistry` остаются нужны для идемпотентности на уровне Watch.

Безопасность. Reply не проходит через `ApprovalAuthenticator`. Если не сверять `decisionID` и ожидаемого ревьюера по серверному pending list, любой клиент с доступом к OpenCode сможет закрыть форму и тем самым возобновить чужой Watch. `Form.Metadata` это произвольный JSON, его нельзя считать доверенным источником прав.

Совместимость версий OpenCode. Формы появились в HttpApi 0.0.1, а старый SDK 1.18.22 их не знает. Установленный бинарник 2.3.2 не проверялся на живой вызов. Нужна проверка возможностей через `GET /api/info` или пробный `GET /api/form`, а при 404 или 400 откат на текущий CLI и Telegram путь.

## 5. Итоговая таблица

| Сценарий | Сейчас | Через Forms API | Выигрыш | Риск |
| --- | --- | --- | --- | --- |
| Воркер задаёт вопрос | Флаг `hasPendingQuestion` в wake, парковка `wait_result`, свободный текст в карточке | Встроенный `question` создаёт форму с полями и валидацией | Типы, варианты, обязательность, условные поля | Форма не останавливает агента, нужен встроенный `question` или сохранение парковки |
| Человек видит вопрос | Уведомление Telegram или ntfy, колонка ATTENTION, `attention-requests.json`, pending list | `GET /api/form`, `GET .../form`, события `form.created` | Один источник вопроса, состояние pending или answered или cancelled | Экспериментальный API, нужен sessionID и живая сессия |
| Approve или reject | `beacon human approve/reject`, `/approve` в боте, HTTP decide, затем ack и resume | `POST .../reply` с полем `decision`, затем мост вызывает тот же ack | Структурированный ответ, 409 на повтор формы | Reply не возобновляет Watch, нужна проверка decisionID и sessionID заново |
| Блок main | `decision="blocked:not_mine"`, sessionID из `_meta`, failed checks из Watch | Ответ `blocked:not_mine` в форме, мост вызывает тот же ack | Причина в поле формы и в `message` cancel | `form.cancelled` не несёт `message`, нужно дочитывать `get`; ADR 0058 и eval r03 |
| Rework, resume со старой revision | `continueOf`, пять раундов, сверка revision в ack | Форма хранит revision в `metadata`, reply сверяется с pending list | Revision можно показать отдельным полем | `w21` требует `question_changed`; текущий ack path этого не делает |
| Отмена вопроса | `cancel` закрывает Watch, `pause` только journal | `DELETE .../form` с `message` | Явная причина отмены и состояние cancelled | Нужно решить, отменяет ли это Watch или только форму |
| Повторный или задержанный ответ | DecisionRegistry, first wins, conflict на другое решение, проверка revision | 409 FormAlreadySettled плюс тот же DecisionRegistry | Два уровня идемпотентности | Нельзя полагаться только на 409, Watch всё равно может возобновиться дважды |
| Авторизация решения | `_meta` sessionID плюс pending list, снимок клиента игнорируется | Reply не несёт `_meta`, мост должен сам сверить metadata с pending list | Единый идентификатор формы | Высокий риск подмены ревьюера без строгой проверки |
| Golden контракт MCP | Девять инструментов, 18212 байт, SHA зафиксирован | Доступ через HTTP-адаптер, число инструментов не меняется | Контракт инструментов остаётся побайтово тем же | Новый MCP-инструмент сразу ломает контракт и evals 02, r10, e02 |
| Перезагрузка локации или рестарт | Watch и блоки живут в SQLite | Pending формы отменяются локацией | Видно, что вопрос больше не активен | Нужно восстановить вопрос в Watch или явно закрыть его |

Короткий вывод. Forms API стоит использовать для структурирования и доставки вопроса и ответа, а не как замену `beacon_ack` и Watch-переходов. Минимальный безопасный шаг это добавить HTTP-адаптер к form-роутам и мост от `form.replied` к уже существующему `Decide`, сохранив CLI, Telegram и ack контракт как работающий путь. Полный отказ от текущего механизма требует отдельного ADR, пересмотра golden контракта и обновления evals `e05`, `w21`, `02_catalog`.
