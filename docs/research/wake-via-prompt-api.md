# Wake через `opencode api POST .../prompt` без порта и EnsureServer

Дата: 2026-11-21. Режим только чтение. Код, тесты и конфиги не менялись. `go test`, `go build`, `beacon gates` и `tools.beacon.*` не запускались. Поиск шёл через `rg` с явными каталогами `cmd/`, `internal/`, `docs/`, а также по локальным исходникам OpenCode в `/home/nbook/opencode/packages`. Первичные сведения об API получены со страницы `https://opencode.ai/docs/api/` и через Context7.

## 1. Текущий поток wake

Цепочка от старта процесса до доставки указателя агенту.

1. `cmd/beacon/main.go:121` безусловно вызывает `wireOpenCodePorts()`. Функция в `main.go:131-137` вызывает `newOpencodePort()` до разбора любой подкоманды.
2. `cmd/beacon/opencode.go:118-129`: `newOpencodePort()` вызывает `resolveOpencodeConfig()` (строка 119), строит `opencode.New(cfg, opencodeHTTPDoer)` (123), затем ставит глобальные порты `SetSessionCreator` (127) и `SetServerEnsurer(serverEnsurerFunc(opencode.EnsureServer))` (128). После этого `installedServerEnsurer` не nil.
3. `resolveOpencodeConfig()` в `opencode.go:102-107` выбирает `installedServerEnsurer.EnsureServer(ctx)`, если порт уже установлен, иначе `opencode.ReadConfig()`. `ReadConfig()` в `internal/infra/opencode/client.go:57-81` читает только `$XDG_STATE_HOME/opencode/service.json`.
4. Фоновый реконсилятор доходит до реестра wake четырьмя путями: `startHTTPAPI` через `Sink` в `cmd/beacon/mcp.go:906`, `makeEscalator` в `:1586`, повторная отправка ladder pointer в `:1664`, `WithWaker` в `ReconcilePass` в `:1732`. Все четыре вызывают `reconcileWaker(cfg.db)`.
5. `reconcileWaker()` в `mcp.go:1547-1556` вызывает `opencodeWakePorts()` (1549). Тот в `mcp.go:1516-1542` вызывает `newOpencodePort()` (1517). Это второй вызов в процессе, и ensurer уже установлен, поэтому управление уходит в `EnsureServer`.
6. `EnsureServer()` в `internal/infra/opencode/server.go:390-429` под мьютексом сначала переиспользует свой живой сервер из `opencode-serve.json` (394-396), потом `service.json` (397-399), и только при отсутствии обоих выделяет свободный порт, спавнит `opencode serve` с Pdeathsig, ждёт health и записывает состояние (401-428). Это место, где wake-цепочка может поднять новый процесс.
7. `opencodeWakePorts()` приводим клиент к `*opencode.Client` (1521) и строит два замыкания. `prompt` вызывает `client.Prompt(ctx, sessionID, text)` (1525-1527), при этом аргумент `delivery` из `PromptFunc` отбрасывается. `client.Prompt` в `client.go:373-384` шлёт body только с полем `text`. `active` вызывает `client.Sessions(ctx)` (1528-1540), то есть `GET /api/session` с TTL-кэшем 2 секунды.
8. `reconcileWaker()` регистрирует `wake.OpenCodeDeliverer{PromptFunc, SessionActiveFunc, Obligations: db}` для адаптера `opencode`.
9. `OpenCodeDeliverer.Deliver()` в `internal/infra/wake/deliver.go:211-314` проверяет `ses_`-префикс (212), владельца обязательства (219), in-memory дедуп по `(watchID, obligationID, revision)` (222-232), аренду цели (237-245) и длительный дедуп через `Obligations` (249-259). Через `SessionActiveFunc` выбирает `queue` для занятой сессии либо `steer` с `resume=true` для свободной (261-278). Собирает текст `Envelope(p)` (280, реализация в 397-409), при наличии `DraftAware` уходит в черновик (287-296).
10. Дальше два транспорта. Если `PromptFunc` задан, идёт вызов в 297-309. Успех ведёт в `markDelivered` (301), `ErrNoPromptPort` ведёт в CLI (302-303), 404 превращается в `ErrSessionDead` (304-305). Если порта нет, сразу вызывается `deliverCLI` (310-313).
11. `deliverCLI()` в `deliver.go:316-343` собирает body `{"text":..., "delivery":..., "resume":true}` (319-322), вызывает `opencode api POST /api/session/<id>/prompt --data <json>` через `Runner` или `exec.CommandContext` с таймаутом 10 секунд (327-335), проверяет 404 (337-339) и подтверждение `verifyAck` (342, реализация в 517-536).
12. `markDelivered()` в `deliver.go:358-367` пишет ключ в `seen` и отметку в SQLite через `MarkObligationDelivered`.

Есть второй поставщик доставки. Демон собирает `T210WakePointer` и вызывает `wake.T210Deliver` (`internal/app/orchestrator/daemon.go:362`). Тот через адаптер вызывает `T202API.Prompt` (`deliver.go:856-874`), который тоже построен на `opencode api`. Общая дедупликация поставщиков живёт через `SharedDeliveryDedup` (`deliver.go:110-175`). Этот путь не вызывает `newOpencodePort` и не достигает `EnsureServer`.

## 2. Где нужен порт и где вызывается EnsureServer

Порт нужен только двум замыканиям в `opencodeWakePorts`: отправке текста и проверке активности сессии. CLI-путь `deliverCLI` не требует ни клиента, ни конфига, ни порта. `opencode api` сам находит фоновый сервис по registration-файлу и авторизуется, что зафиксировано в `docs/adrs/0095-cli-opencode-api.md:11` и на странице CLI docs OpenCode.

`EnsureServer` достигается не из wake-транспорта, а из способа построения порта. Первый вызов `newOpencodePort()` в `main.go:132` читает `service.json` и ставит ensurer. Каждый следующий вызов, включая `mcp.go:1517`, идёт через `resolveOpencodeConfig()` в `EnsureServer`. Если к этому моменту живого сервера Beacon и `service.json` нет, `EnsureServer` спавнит процесс. Это окно описано в `docs/research/mcp-q1-startup.md:51-59` и `docs/research/process-leak-mechanism.md:27`.

Ещё два нетестовых вызова `newOpencodePort()` лежат в `cmd/beacon/main.go:132` и `cmd/beacon/registry_sync.go:295`. Они обслуживают spawn, watch, deliver и registry sync. Убирать их ради wake нельзя.

## 3. Чистая доставка через `opencode api`

### 3.1. Цепочка

1. `reconcileWaker(db)` создаёт `wake.NewRegistry()` и регистрирует `&wake.OpenCodeDeliverer{Obligations: db}` для `wake.AdapterOpenCode`. Поля `PromptFunc` и `SessionActiveFunc` остаются nil.
2. Все четыре вызова `reconcileWaker` в `mcp.go` получают один и тот же реестр без HTTP-клиента. Ни один из них не вызывает `newOpencodePort` и не может дойти до `EnsureServer`.
3. `OpenCodeDeliverer.Deliver()` проходит те же проверки, дедуп и аренду, что и сейчас (211-259).
4. Поскольку портов нет, `Deliver` идёт в транспорт `opencode api` (310). Транспорт собирает id, body и вызывает CLI.
5. CLI возвращает подтверждение `data.id`. `verifyAck` проверяет его форму. `markDelivered` записывает доставку в `seen` и SQLite.

В данной схеме `PromptFunc` и `SessionActiveFunc` не нужны совсем. `ErrNoPromptPort` из `deliver.go:54-56` теряет смысл в mcp-пути: его возвращал бы внедрённый порт, а внедрять нечего. Константу можно оставить для других вызывающих или удалить вместе с веткой, как отмечено в `docs/research/opencode-adapter-audit.md:184`.

Оговорка про процессы. `opencode api` может поднять общий фоновый сервис, если здорового сервиса нет. Это процесс OpenCode, который регистрируется в том же `service.json`, а не спавн Beacon через `freePort()` и `opencode-serve.json`. Если цель владельца звучит как «вообще никаких новых процессов», этот факт нужно учесть отдельно. Устранение `EnsureServer` из wake убирает только управление процессом со стороны Beacon.

### 3.2. Что передавать

Подтверждённая форма v2 из локального источника `packages/protocol/src/groups/session.ts:205-223` и фикстуры `packages/codemode/test/fixtures/opencode-v2-openapi.json:1147-1288`:

```
POST /api/session/<ses_...>/prompt
{
  "id": "msg_<детерминированный хеш>",
  "prompt": { "text": "<beacon-event>...словарь идентификаторов...</beacon-event>" },
  "delivery": "steer",
  "resume": true
}
```

Поля по назначению.

`id` это ключ идемпотентности. Схема требует префикс `msg_` (`^msg_`). Источник в `packages/core/src/session/session.ts`, найденный через Context7, использует `input.id ?? SessionMessage.ID.create()`. Спецификация `specs/v2/session.md` говорит, что повтор того же id в той же сессии и того же типа идемпотентен: первый приём побеждает, а повторные payload, metadata и delivery игнорируются. Повтор с другим идентификатором сессии или другого типа даёт 409. Поэтому id должен выводиться детерминированно из `p.ObligationID`, который уже равен `domain.ObligationKey(watchID, revision, subscriberID)` и проверен в `Deliver` на строке 219. Подходящая формула: `"msg_" + первые 32 hex-символа sha256(obligationID)`. Повтор той же обязанности даёт тот же id, разные обязанности дают разные id. Импорты `crypto/sha256` и `encoding/hex` уже есть в `deliver.go:7,9`.

`prompt.text` это результат `Envelope(p)` из `deliver.go:397-409`. Envelope несёт только `id`, `watchID`, `obligationID`, `revision` и `verdict`. Никакого тела проверки или заголовка PR.

`delivery` равен `steer`. Wake должен продолжить выполнение, а не припарковать ввод. Режим `queue` нужен для ручного ввода в занятую сессию, не для указателя обязательства. Текущий nil-портовый путь ставит `steer` в `deliver.go:261`, но оставляет `resume=false`, что не запускает сессию, закончившую ход. Транспорт должен слать `resume:true` всегда. Это же делает `T202API.Prompt` в `deliver.go:863`.

Проверка формы тела обязательна перед правкой. В локальном дереве есть и старая форма из `packages/sdk/openapi.json:6286-6352`: там body содержит `parts` массив и `messageID`, а код 200 возвращает `{info, parts}`. Текущий код Beacon шлёт плоский `text` и ждёт `data.id`, то есть не совпадает ни с одной из двух форм в дереве исходников. Карточка `docs/tasks/T3.200.md:12-14` фиксировала плоский `text` и 200 с `data.id` для более ранней версии. Установленный бинарник сообщает `opencode v2.3.2`. Владелец должен снять актуальную схему командой `opencode api GET /openapi.json` и сверить поля. Команда read-only. Хардкодить порт или адрес нельзя.

Если сервер отвечает по старой форме, body принимает вид `{"messageID":"msg_...","parts":[{"type":"text","text":"..."}]}`. Ключ идемпотентности и требование стабильного id остаются теми же, меняется только обёртка текста.

### 3.3. Обработка ошибок

`opencode api` печатает тело ответа в stdout при успехе и возвращает ненулевой код при HTTP-ошибке. Нужна классификация по combined output, а не только по stdout.

| Результат CLI | Действие |
| --- | --- |
| Код 0 и `{"data":{"id":"msg_..."}}` | Доставка принята. Вызвать `verifyAck`, затем `markDelivered`. |
| 409 ConflictError | Тот же id уже принят. Это идемпотентный повтор, вторая доставка не нужна. Отметить обязательство доставленным. `T202ClassifyHTTP` уже трактует 409 как `duplicate-id` (`deliver.go:798-807`). |
| 404 SessionNotFoundError | `ErrSessionDead`. Не помечать доставленным. |
| 400 InvalidRequestError | Ошибка формы запроса или версии API. Не ретраить, не помечать доставленным, поднять ошибку. |
| 401 UnauthorizedError | Расхождение пароля или конфига сервиса. Не ретраить, не помечать доставленным. |
| Таймаут 10 с, connection refused, 5xx | Ошибка транспорта. Не помечать доставленным, оставить обязательство доступным для повтора. |
| Код 0, пустое или неверное тело | `verifyAck` возвращает ошибку (`deliver.go:520-536`). Не помечать доставленным. |

В текущем коде есть пробел. `deliverCLI` вызывает `exec.Cmd.Output()` в `deliver.go:334`, который возвращает только stdout. При ненулевом коде `opencode api` пишет HTTP-ошибку в stderr, доступный как `*exec.ExitError.Stderr`, но `sessionNotFound(err, out)` в `deliver.go:337,381-390` смотрит только `out` и `err.Error()`, а `ExitError.Error()` даёт строку `exit status 1`. Поэтому 404 и 409 из CLI сейчас не распознаются. В новой транспортной функции следует брать `CombinedOutput()` или явно читать `ExitError.Stderr` до классификации. Функция `sessionNotFound` остаётся для статусного порта и для `PromptFunc`, если тот ещё используется вне mcp.

### 3.4. Как подтвердить доставку

Основное подтверждение это `data.id` в ответе POST. `verifyAck` (`deliver.go:517-536`) и `T202messageID` (`deliver.go:908-926`) уже требуют непустое `data.id` и не считают пустой ответ успехом. Карточка T3.200 называет это признаком принятия и требует, чтобы повтор с тем же id возвращал первоначальный приём.

Более сильная проверка доступна через `GET /api/experimental/session/<id>/log?after=<seq>&follow=false`. OpenAPI называет его `experimental.session.log`, описание: «Experimental durable session event log». Ответ это SSE поток объектов с полями `id`, `event`, `data`, где `data` это `SessionLogItem`. Параметр `after` задаёт эксклюзивную позицию в агрегированной последовательности, `follow=false` завершает чтение. В потоке можно найти событие приёма с тем же `msg_...`. Альтернатива это `GET /api/session/<id>/inbox`, который показывает durable-очередь, включая synthetic и пользовательские элементы.

Эти два чтения стоит держать вне горячего пути доставки. Они добавляют второй вызов и разбор SSE. Для диагностики и eval-сценариев достаточно существующей подписки `GET /api/event` с событиями `session.inbox.enqueued` и `session.execution.started`, как записано в `docs/tasks/T3.200.md:13` и `docs/tasks/T3.215.md:12`. Отдельно стоит учесть negative control из `docs/tasks/T0.3-wake-spike.md:20`: synthetic с `resume:false` добавляет текст в историю, но не будит сессию. Для wake нужен именно `prompt` с `resume:true`. Маршрут `/synthetic` пригодится только если владелец сознательно захочет внедрить текст без запуска агента.

## 4. Nil-порты и различение состояний

Да, дизайн предполагает nil в обоих портах `wake.OpenCodeDeliverer`. Это не компромисс, а рабочий сигнал. `Deliver` в `deliver.go:297-313` именно так выбирает CLI: нет `PromptFunc`, значит сразу `deliverCLI`. Текущий `opencodeWakePorts` тоже возвращает `nil, nil` при отсутствии registration (`mcp.go:1517-1520`). Новая правка делает это состояние единственным и убирает саму попытку построить клиента.

Нельзя подменять nil замыканиями, которые возвращают `ErrNoPromptPort` или `(false, true, nil)`. Документ `docs/research/mcp-lazy-port-design.md:47` показывает ловушку: как только `SessionActiveFunc` существует, `Deliver` начинает судить о живости сессии по ошибке статуса (`deliver.go:263-271`) и может не дойти до CLI. Ленивая инициализация с `sync.Once` также сохраняет риск спавна при первой доставке, если внутри всё равно вызывается `newOpencodePort`.

Различать «нет конфига» и «сервер мёртв» локально в новой схеме не нужно и нечем. В wake-пути больше нет `opencode.ReadConfig()` и нет `*opencode.Client`, значит Beacon не держит состояние конфига. `opencode api` выполняет discovery и возвращает один наблюдаемый результат. Честная классификация строится по результату CLI, как в таблице выше: ошибка discovery это «сервис недоступен», 404 это «сессия мертва», таймаут это «транспорт», 409 это «уже принято». Если владельцу нужно именно локальное различение для диагностики, можно добавить read-only вызов `opencode.ReadConfig()` вне пути доставки. Он не ставит ensurer и спавн не вызывает, но на результат доставки влиять не должен.

Следствие nil-портов: `Deliver` выбирает `delivery=steer` и `resume=false` в `deliver.go:261`. Транспорт должен переопределить `resume` на true, иначе пробуждение сессии не произойдёт. Проще всего убрать параметр `resume bool` из `deliverCLI` и всегда писать `true`, поскольку функция обслуживает только wake-указатели.

## 5. Минимальный patch-план

| Файл:строки | Правка |
| --- | --- |
| `cmd/beacon/mcp.go:1516-1542` | Удалить `opencodeWakePorts()` целиком. Вне mcp функция не вызывается. |
| `cmd/beacon/mcp.go:1547-1556` | `reconcileWaker`: зарегистрировать `&wake.OpenCodeDeliverer{Obligations: db}` без портов. Оставить `wake.NewRegistry()` и `wake.AdapterOpenCode`. |
| `internal/infra/wake/deliver.go:222`, `:310` | Вычислить `messageID := wakeMessageID(p.ObligationID)` рядом с `key` и передать его в `deliverCLI`. |
| `internal/infra/wake/deliver.go:316-343` | Переписать `deliverCLI`: новая сигнатура с messageID, body по актуальной форме API, `resume:true`, `CombinedOutput`, классификация 409/404/400/401/таймаута, затем `verifyAck`. |
| `internal/infra/wake/deliver.go` рядом с `:379-390` | Добавить классификатор вывода CLI. `sessionNotFound` оставить для статусного порта. |
| `internal/infra/wake/deliver.go` рядом с `:517-536` | Добавить `wakeMessageID(obligationID string) string` на базе sha256 и hex с префиксом `msg_`. |
| `internal/infra/wake/deliver.go:31-36`, `:54-56` | Уточнить комментарии: `ErrNoPromptPort` относится только к внедрённым портам, mcp их не ставит. |
| `internal/infra/wake/deliver_ports_test.go:140-173`, `:209-238`, `:177-207` | Обновить разбор body и добавить случаи 409 и 404 из combined output. Тесты не запускались в этом режиме. |
| `internal/infra/wake/deliver_test.go:38,70,86,115,347` | Проверить runner-заглушки на новую форму body. Форма ack (`w2bAck`) меняться не должна. |

`cmd/beacon/opencode.go`, `cmd/beacon/main.go`, `cmd/beacon/registry_sync.go` и `internal/infra/opencode/server.go` не трогаются. Spawn, watch, deliver и registry sync сохраняют прежний порт и прежний `EnsureServer`.

Порядок шагов. Сначала подтвердить форму body через `opencode api GET /openapi.json`. Затем убрать порты из mcp. Затем переписать транспорт и классификатор. Каждый шаг оставляет рабочий CLI-фолбэк, поэтому промежуточные состояния проверяемы.

## 6. Проверка текстом и логикой без тестов

1. `rg -n 'newOpencodePort|EnsureServer|opencodeWakePorts|reconcileWaker' cmd/beacon`. Ожидание: в `mcp.go` нет `newOpencodePort`, `opencodeWakePorts` отсутствует, `reconcileWaker` не строит клиент. Вызовы `EnsureServer` остаются только в адаптере `serverEnsurerFunc` и в `internal/infra/opencode/server.go`.
2. `rg -n 'EnsureServer|newOpencodePort' internal/infra/wake internal/app/orchestrator`. Ожидание: пусто. Демон использует `T202API`, а не HTTP-клиент.
3. Прочитать `mcp.go:1547-1556` вручную. Единственное поле регистрации это `Obligations: db`.
4. Проследить четыре вызова `reconcileWaker` из `mcp.go:906,1586,1664,1732`. Все они приходят к одному deliverer с nil-портами.
5. Прочитать переписанный `deliverCLI`. Подтвердить наличие `id` с префиксом `msg_`, `prompt.text`, `delivery:"steer"`, `resume:true`, `CombinedOutput` и вызов `verifyAck`.
6. `rg -n 'sha256|msg_' internal/infra/wake/deliver.go`. Хелпер на месте, префикс согласован со схемой `^msg_`.
7. Проверить, что в новой wake-цепочке нет `opencode.ReadConfig()`, `opencode.New()` и вызовов `exec` кроме самого `opencode api`.
8. Мысленно прогнать три случая: нет registration, живой сервис и мёртвая сессия. В первых двух CLI сам найдёт сервис и вернёт `data.id` или ошибку discovery. В третьем придёт 404, который станет `ErrSessionDead` и не вызовет `markDelivered`.
9. Проверка в живом окружении, которую делает владелец: снять `/openapi.json`, отправить тестовый `<beacon-event>` через `opencode api POST`, прочитать `data.id`, затем прочитать `GET /api/experimental/session/<id>/log?follow=false` и найти событие приёма. Число процессов `opencode serve` до и после вызовов `tools.beacon.*` не должно расти по вине Beacon.

## Источники

Локальные файлы:

- `cmd/beacon/mcp.go:906,1516-1556,1586,1664,1732`
- `cmd/beacon/opencode.go:102-129`
- `cmd/beacon/main.go:121,131-137`
- `cmd/beacon/registry_sync.go:295`
- `internal/infra/opencode/client.go:47-81,107-133,373-384`
- `internal/infra/opencode/server.go:390-429`
- `internal/infra/wake/deliver.go:31-60,74-98,110-175,211-343,358-409,517-536,798-807,842-926,1048-1115`
- `internal/infra/wake/registry.go:16-145`
- `internal/infra/wake/deliver_ports_test.go:140-238`
- `docs/adrs/0095-cli-opencode-api.md:11`, `docs/adrs/0194-cli-opencode-api.md:11`
- `docs/tasks/T3.200.md:12-14`, `docs/tasks/T3.202.md:14`, `docs/tasks/T3.215.md:12`, `docs/tasks/T0.3-wake-spike.md:20`
- `docs/research/opencode-experimental-wake-api.md:29-36,85-91,117-139`
- `docs/research/mcp-lazy-port-design.md:41-47,90-125`
- `docs/research/mcp-q1-startup.md:51-59`
- `docs/research/opencode-adapter-audit.md:175-194`
- `docs/research/process-leak-mechanism.md:27`

Первичные источники OpenCode:

- `/home/nbook/opencode/packages/protocol/src/groups/session.ts:205-223`
- `/home/nbook/opencode/packages/schema/src/prompt-input.ts:21-26`
- `/home/nbook/opencode/packages/schema/src/session-input.ts:14-23`
- `/home/nbook/opencode/packages/codemode/test/fixtures/opencode-v2-openapi.json:1147-1288,2499-2579`
- `/home/nbook/opencode/packages/sdk/openapi.json:6203-6355`
- `/home/nbook/opencode/packages/core/src/session/session.ts` и `/home/nbook/opencode/specs/v2/session.md` через Context7
- https://opencode.ai/docs/api/ и https://opencode.ai/docs/cli/
