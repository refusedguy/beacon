# CLI-менеджер OpenCode поверх Beacon

Это проектное предложение. Оно не меняет карточки и ADR. Ссылки на локальные файлы даны относительно корня `feat/w46`, коммит `c081e9b62f72c8e421918092f30bc72c5b2c82d6`. Версия установленного бинарника: `opencode v2.3.0` (`opencode --version`). Исходники OpenCode ниже взяты с ветки `dev`, поэтому в конце раздела «API для управления» отмечено расхождение между v1-документацией и текущими маршрутами.

## Что умеет opencode serve

`opencode serve` поднимает headless HTTP-сервер с v2 API и веб-интерфейсом. Флаги: `--hostname` (по умолчанию `127.0.0.1`), `--port` (по умолчанию `4096`), `--cors` (повторяемый), `--service`, `--stdio`. Это видно в `opencode serve --help` и в [документации сервера](https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/v1/content/server.mdx); объявление флагов в [CLI-исходнике](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/commands/commands.ts).

`--service` регистрирует сервер как общий фоновый сервис. `--stdio` включает другой транспорт и для HTTP-обвязки не нужен. Оба флага определены в [Spec.make("serve")](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/commands/commands.ts).

Адрес уже работающего сервера можно узнать без спавна. Сервис пишет registration-файл `$XDG_STATE_HOME/opencode/service.json`, то есть обычно `~/.local/state/opencode/service.json`. В файле лежат `url`, `pid`, `version` и приватный пароль, права `0600`. Приватная конфигурация лежит отдельно в `~/.config/opencode/service.json`, база в `~/.local/share/opencode/opencode.db` ([troubleshooting](https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/content/troubleshooting.mdx)). `Service.discover()` читает этот файл, делает health-probe и проверяет версию, но никогда не спавнит процесс; спавн делает `Service.ensure()` ([service.ts](https://github.com/anomalyco/opencode/blob/dev/packages/client/src/effect/service.ts), [service-probe.ts](https://github.com/anomalyco/opencode/blob/dev/packages/client/src/service-probe.ts)).

Готовый CLI-интерфейс для этого уже есть: `opencode service start|restart|status|stop|get|set|unset` и `opencode api <operation | method path...>` с флагами `--data`, `--param`, `--header` (вывод `opencode service --help` и `opencode api --help`). То есть обвязке не обязательно содержать собственный HTTP-клиент.

t3code идёт другим путём. Он не использует `--service`, а спавнит собственный процесс `opencode serve --hostname=127.0.0.1 --port=<свободный порт>` с переменными `OPENCODE_SERVER_PASSWORD` и `OPENCODE_CONFIG_CONTENT`. Порт выбирается через `findAvailablePort(0)`, таймаут старта 30 секунд. Готовность определяется по строке stdout `opencode server listening on http://...`, после чего создаётся SDK-клиент с `baseUrl` и Basic-авторизацией `opencode:<password>` ([opencodeRuntime.ts:80](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/opencodeRuntime.ts#L80), [opencodeRuntime.ts:651](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/opencodeRuntime.ts#L651), [opencodeRuntime.ts:664](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/opencodeRuntime.ts#L664), [opencodeRuntime.ts:274](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/opencodeRuntime.ts#L274), [opencodeRuntime.ts:630](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/opencodeRuntime.ts#L630)). Жизнь процесса привязана к `Scope`, и ребёнок убивается при закрытии скоупа ([opencodeRuntime.ts:212](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/opencodeRuntime.ts#L212)).

t3code проверяет версию через health-ответ `client.server.info()` и требует минимум `1.14.19` ([opencodeRuntime.ts:41](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/opencodeRuntime.ts#L41), [opencodeRuntime.ts:142](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/opencodeRuntime.ts#L142)).

Есть ещё `opencode acp`, сервер Agent Client Protocol (вывод `opencode acp --help`). ADR 0024 прямо указывает, что ACP описывает `session/new`, `session/prompt`, `session/update`, `session/cancel`, но не решает вопросы worktree, облачного жизненного цикла, очередей follow-up и получения артефактов (`docs/adrs/0024-one-bridge-per-protocol.md:13`).

Вывод для Beacon: использовать уже существующий фоновый сервис и его registration-файл. Собственный спавн нужен только если сервис отключён; тогда логика трёх режимов подключения уже написана в [server-connection.ts](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/services/server-connection.ts).

## API для управления

Основные маршруты, нужные CLI:

- `GET /api/session` список сессий; `POST /api/session` создаёт сессию с полями `parentID`, `title`, `agent`, `model`, `location.directory`, `permissions`, `metadata` ([session.ts](https://github.com/anomalyco/opencode/blob/dev/packages/protocol/src/groups/session.ts), [server.mdx](https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/v1/content/server.mdx)).
- `POST /api/session/:sessionID/prompt` отправляет сообщение. Тело текущего маршрута: `id`, `text`, `files`, `agents`, `metadata`, `delivery`, `resume`. Поля `agent` и `model` в теле отсутствуют, агента задают при создании сессии ([session.ts](https://github.com/anomalyco/opencode/blob/dev/packages/protocol/src/groups/session.ts)).
- v1-документация описывает тот же вызов как `POST /session/:id/message` с необязательными `agent`, `noReply`, `system`, `tools` и обязательными `parts`, плюс `POST /session/:id/prompt_async`, который возвращает `204 No Content` ([server.mdx](https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/v1/content/server.mdx)). t3code на SDK v2 вызывает `client.session.promptAsync({ sessionID, agent, parts })` ([OpenCodeAdapter.ts:3187](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/Layers/OpenCodeAdapter.ts#L3187)). Это расхождение версий. Для нового кода надёжнее задавать агента в `POST /api/session`, а не в каждой отправке.
- `GET /api/event` это SSE-поток с необязательным параметром `types`. Первым приходит `server.connected`. Поток живой: нет повтора прошедших событий и нет автопереподключения, медленный потребитель переполняет поток и получает ошибку ([event.ts](https://github.com/anomalyco/opencode/blob/dev/packages/protocol/src/groups/event.ts), [client docs](https://opencode.ai/docs/build/client/#stream-events)). Beacon зафиксировал это же ограничение в ADR 0025 (`docs/adrs/0025-status-and-cursor.md:11`).
- `session.status` это эфемерное событие со значением `idle`, `retry` или `busy`; `session.idle` помечено deprecated, а терминальные исходы приходят в событиях `session.execution.*` ([session-status-event.ts](https://github.com/anomalyco/opencode/blob/dev/packages/schema/src/session-status-event.ts)). Плагин Beacon уже подписан на `session.execution.started` (`.opencode/plugins/beacon-bridge.js:35`).
- Состояние всех сессий доступно снимком: `client.session.status(undefined)` возвращает карту `sessionID -> status`, а `client.session.get({ sessionID })` читает одну сессию ([OpenCodeAdapter.ts:1173](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/Layers/OpenCodeAdapter.ts#L1173), [OpenCodeAdapter.ts:1655](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/Layers/OpenCodeAdapter.ts#L1655)). Это запасной путь на случай потери SSE-соединения.
- `app.agents()` отдаёт список агентов ([sdk.mdx](https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/v1/content/sdk.mdx), [opencodeRuntime.ts:892](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/opencodeRuntime.ts#L892)). Дополнительно t3code разбирает вывод `opencode agent list` ([opencodeRuntime.ts:355](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/opencodeRuntime.ts#L355)).
- Дочерние сессии: v1-документация даёт `GET /session/:id/children` ([server.mdx](https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/v1/content/server.mdx)), текущий маршрут вместо отдельного эндпоинта использует `parentID` при создании ([session.ts](https://github.com/anomalyco/opencode/blob/dev/packages/protocol/src/groups/session.ts)). t3code вызывает `client.session.children()` ([OpenCodeAdapter.ts:789](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/Layers/OpenCodeAdapter.ts#L789)). Не стоит строить CLI на недокументированном в текущей версии маршруте.
- Перевод сессии в `opencode api` доступен без собственного HTTP-клиента: `opencode api session.list`, `opencode api 'GET /api/event'` и так далее (вывод `opencode api --help`).

## Как спавнить сабагентов

Три рабочих пути.

Первый, нативный для OpenCode. Инструмент `subagent` принимает ID агента, короткое описание и полный промпт. Вызов возвращает `sessionID` сразу, ребёнок всегда работает в фоне, родитель получает сообщение `<subagent>` когда ребёнок становится idle и после каждого следующего круга. Использовать можно только агентов в режиме `subagent`, глубина вложенности по умолчанию один ([tools.mdx](https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/content/tools.mdx)). Внутри OpenCode инструмент создаёт дочернюю сессию с `parentID: context.sessionID`, `agent: agent.id`, вызывает `sessions.prompt` с `resume: false`, а запуск сессии отдаёт отдельному job ([subagent.ts](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/tool/plugin/subagent.ts)). Режимы агентов: `primary`, `subagent`, `all` ([agents.mdx](https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/content/agents.mdx)).

Второй, управляемый из Beacon. Создать сессию через `POST /api/session` с `parentID` оркестратора и `agent` нужного субагента, затем отправить промпт в `POST /api/session/:sessionID/prompt`. Для одноразового foreground-запуска есть `opencode run --agent <id> --format json --archived "<message>"`; флаги `--session`, `--fork`, `--worktree`, `--title` перечислены в `opencode run --help`. Флаг `--archived` убирает такую сессию из обычных списков, что удобно для скриптов.

Третий, как это делает t3code. Нативные сабагенты OpenCode попадают в проекцию как дочерние треды с `lineage.relationshipToParent = "subagent"`, а повторный запуск оркестратора идёт после окончания ребёнка, а не после idling родителя ([opencode2_subagent/output.ts](https://github.com/pingdotgg/t3code/blob/main/apps/server/src/orchestration-v2/testkit/fixtures/opencode2_subagent/output.ts), [opencode2_background/output.ts](https://github.com/pingdotgg/t3code/blob/main/apps/server/src/orchestration-v2/testkit/fixtures/opencode2_background/output.ts)).

Здесь важно правило Beacon: протокол цикла говорит `An idle child report is not done` (`internal/infra/mcp/server.go:38`). Оркестратор сливает только после `beacon_get_result` с `verdict=ready` (`internal/infra/mcp/server.go:38`). ADR 0026 требует, чтобы инструмент пробуждения возвращал `steer`, `queue` или `unsupported` и не выдавал очередь за доставку в текущий ход (`docs/adrs/0026-honest-followup-delivery.md:7`). Поэтому CLI должен показывать вердикт Beacon, а не статус `idle` из OpenCode.

## Проект CLI

Минимальная версия состоит из двух команд: `beacon watch` и `beacon attach <session>`. `beacon agents` можно сделать флагом `-agents` у `watch`, но отдельная команда дешевле по коду и не перегружает вывод. `beacon wake` имеет смысл добавить только вместе с будущим инструментом `beacon_wake`: ADR 0026 уже описал контракт `delivery` (`docs/adrs/0026-honest-followup-delivery.md:7`) и карточку на `tool_wake.go` (`docs/adrs/0026-honest-followup-delivery.md:19`). Сейчас в реестре семь инструментов, `tool_wake.go` нет (`internal/infra/mcp/server.go:45`). Ниже псевдокод для всех четырёх команд, с пометкой, какая часть временная.

Обвязка встраивается в существующий dispatch: `switch args[0]` в `cmd/beacon/main.go:73`. Запуск внешних процессов уже абстрагирован типом `runner` (`cmd/beacon/main.go:37`), поэтому вызовы `opencode api` проходят через него и подменяются в тестах. Каталог состояния берётся функцией `stateDir()` (`cmd/beacon/doctor.go:113`), снимок Beacon через `SnapshotReader` (`internal/infra/mcp/tool_list_running.go:32`).

```
beacon agents [--format table|json]
    cfg := read($XDG_STATE_HOME/opencode/service.json)
    if cfg == nil:
        print "фоновый сервис не запущен: opencode service start"
        exit 2
    agents := run("opencode", "api", "app.agents")      # через runner
    rows := [a for a in agents if a.mode in {"subagent", "all"}]
    print id, name, mode

beacon watch [--interval 2s] [--format table|json]
    snap     := beacon.Snapshot()                        # существующий read-only порт
    sessions := run("opencode", "api", "session.list")
    stream   := run("opencode", "api", "GET /api/event", "--param", "types=session.status,session.execution.started")
    loop:
        event := stream.next()
        sessions := refresh(sessions)                   # на событии или по таймеру
        snap     := beacon.Snapshot()
        render table:
          WATCH | VERDICT | REVISION | SUBSCRIBER SESSION | AGENT | OPENCODE STATE
        # JOIN: subscriber target из _meta "ai.opencode/sessionID" (internal/infra/mcp/meta.go:8)
        #       к sessionID из session.list
        # OPENCODE STATE это idle/retry/busy, а не готовность задачи

beacon wake <watchID>
    w   := snapshot.watch(watchID)
    sub := w.subscriberForCurrentUser()
    # ВРЕМЕННО, пока нет tool_wake.go:
    res := run("opencode", "api", "POST /api/session/:id/prompt",
               "--data", pointerOnly(sub.obligationID))
    print res.delivery, sub.obligationID                # steer | queue | unsupported
    # повтор только для transport-failed по ADR 0017, undefined исход не ретраим

beacon attach <sessionID>
    cfg := read($XDG_STATE_HOME/opencode/service.json)
    exec "opencode", "--server", cfg.url, "--session", sessionID
    # CLI завершается, управление получает обычный TUI
    # флаг --session описан в `opencode --help`
```

Где живёт код. Вызовы OpenCode лучше держать в одном тонком адаптере рядом с CLI, например `cmd/beacon/opencode_bridge.go`, а не в `internal/domain` и не в MCP-инструментах. Ядро остаётся на MCP и не знает имен агентов (`docs/adrs/0024-one-bridge-per-protocol.md:7`, `docs/adrs/0006-opencode-adapter-boundary.md:7`). Это единственное место проекта, где возникает напряжение с текущими ADR: они требуют, чтобы весь OpenCode-код жил в одном JS-плагине, а здесь добавляется Go-адаптер в CLI. В рамках этой смены ADR не трогается, поэтому пункт остаётся открытым решением владельца. Альтернатива без нового Go-кода: `beacon watch` читает только квитанции плагина в `$XDG_STATE_HOME/beacon/receipts` (`.opencode/plugins/beacon-bridge.js:22`), а все запросы к серверу делает внешний `opencode api` через уже существующий `runner`.

## Что НЕ стоит делать

Не писать собственный TUI или веб-интерфейс. `opencode serve` уже отдаёт веб-интерфейс, а `opencode --session <id>` открывает обычный TUI (вывод `opencode serve --help` и `opencode --help`). Задача CLI это один текстовый список и переход в сессию.

Не опрашивать сервер в цикле вместо подписки. `/api/event` даёт живые события, а история по ADR 0025 не хранится (`docs/adrs/0025-status-and-cursor.md:7`). После обрыва соединения нужно перечитать `session.status` или терминальный вердикт, а не пытаться нагнать пропущенное.

Не считать `session.idle` или пустой статус признаком готовности задачи. Событие `idle` означает только конец хода ([session-status-event.ts](https://github.com/anomalyco/opencode/blob/dev/packages/schema/src/session-status-event.ts)); готовность приходит из вердикта Beacon (`internal/infra/mcp/server.go:38`).

Не хранить полные транскрипты сессий в Beacon. ADR 0025 ограничивается статусом и курсором (`docs/adrs/0025-status-and-cursor.md:7`).

Не добавлять HTTP-клиент OpenCode в `internal/domain` или в MCP-инструменты. Для домена сессия это непрозрачный идентификатор в `_meta` (`internal/infra/mcp/meta.go:8`), а граница моста описана в ADR 0024 (`docs/adrs/0024-one-bridge-per-protocol.md:7`).

Не поднимать `opencode serve --service` и одновременно спавнить свой сервер. Три режима подключения уже реализованы в [server-connection.ts](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/services/server-connection.ts): явный `--server`, приватный `--standalone` и discovery фонового сервиса.

Не хардкодить порт `4096`. Это значение по умолчанию ([server.mdx](https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/v1/content/server.mdx)); t3code берёт свободный порт через `findAvailablePort(0)` ([opencodeRuntime.ts:651](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/opencodeRuntime.ts#L651)).

Не задавать агента в теле `prompt` для текущего маршрута v2. Поле `agent` там отсутствует, его задают при создании сессии ([session.ts](https://github.com/anomalyco/opencode/blob/dev/packages/protocol/src/groups/session.ts)).

Не использовать `--stdio` для этой обвязки. Это другой транспорт, описанный в [CLI-исходнике](https://github.com/anomalyco/opencode/blob/dev/packages/cli/src/commands/commands.ts).

Не менять в этом PR карточки и ADR. Для продакшн-версии по правилу ADR 0020 понадобится отдельная карточка на `beacon_wake`, и отдельное решение о границе между JS-плагином и CLI-адаптером (`docs/adrs/0020-task-tool.md`, `docs/adrs/0024-one-bridge-per-protocol.md:21`).

## Простыми словами

OpenCode уже умеет работать сервером и сам пишет свой адрес в файл `~/.local/state/opencode/service.json` ([troubleshooting](https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/content/troubleshooting.mdx)). Beacon уже знает, какие задачи висят и кто на них подписан (`internal/infra/mcp/tool_list_running.go:32`). Нужно соединить два списка и распечатать их в терминале (`internal/infra/mcp/server.go:38`).

Новый интерфейс не нужен. `beacon watch` показывает таблицу, `beacon attach` открывает сессию в обычном TUI через `opencode --session` (вывод `opencode --help`). Готовность задачи берётся из вердикта Beacon, а не из слова `idle` (`internal/infra/mcp/server.go:38`). Подписки OpenCode живые и не хранят историю, поэтому таблица обновляется по событиям, а после обрыва перечитывает статус ([client docs](https://opencode.ai/docs/build/client/#stream-events), `docs/adrs/0025-status-and-cursor.md:11`).

Сервер поднимать самому не обязательно. Если фоновый сервис уже запущен, CLI просто читает его registration-файл ([service.ts](https://github.com/anomalyco/opencode/blob/dev/packages/client/src/effect/service.ts)). Если его нет, путь спавна используется в t3code ([opencodeRuntime.ts:664](https://github.com/pingdotgg/t3code/blob/eee05575ebd514db36f61d7eb05d2258a10c96bd/apps/server/src/provider/opencodeRuntime.ts#L664)).
