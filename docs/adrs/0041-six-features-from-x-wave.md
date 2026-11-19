# ADR 0041. Шесть фич из отчёта X-wave

Статус: accepted

Этот ADR фиксирует шесть фич из отчёта X «X-волна: 16 новых проектов для Beacon» от 20 ноября 2026 года. Идеи собраны в разделе «Фичи» ([X, строка 237](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L237)) и подтверждены разделами конкретных проектов. Здесь зафиксированы только те фичи, которых нет в ADR 0001-0040 и 126 карточках из `docs/tasks/`. Fencing и аренда уже в T3.51 и ADR 0038 пункт 1, backpressure в ADR 0038 пункт 2, claim в T3.47, `needs_input` в ADR 0031 и T3.21, `indeterminate` в ADR 0039 пункт 5 и T3.64, quorum в ADR 0038 пункт 5, общий gate в T3.49, failure contract в T3.50. Несколько новых пунктов похожи на существующие решения по имени, но различаются по уровню. InputRequired в фиче 1 это исход протокола `tools/call`, а состояние `needs_input` из ADR 0031 это состояние задачи. Четыре settled-исхода в фиче 2 это явные глаголы завершения аренды, а `indeterminate` из ADR 0039 описывает потерянный ответ внешнего действия. Продление visibility в фиче 3 удерживает аренду работающего воркера, а fencing из T3.51 инвалидирует аренду устаревшего. Владелец велел принять оставшиеся идеи без дополнительных вопросов, поэтому статус `accepted`. По ADR 0020 карточки создаются только из принятого ADR. Карточки этим ADR не создаются, код, карточки и тесты в этой задаче не трогаются.

## Решение

Beacon получает шесть механизмов. Они описаны на уровне контракта и не привязаны к конкретному продукту.

1. Третий исход `tools/call`. Помимо немедленного результата и запущенной задачи вызов может вернуть `InputRequired`: паузу, пока клиент не пришлёт ввод. `wait_result` в этом состоянии не тратит таймаут и не возвращает ошибку, а ждёт события ввода с тем же обязательством и ревизией. Возврат из паузы продолжает ту же задачу. Это исход протокола вызова, а не состояние задачи `needs_input` из ADR 0031: там вопрос задаёт агент и ответ идёт через `submit(continue_of=…)`, здесь паузу объявляет сам `tools/call` и ввод приходит от клиента MCP. Набор инструментов не расширяется, меняется множество исходов существующего вызова.

2. Четыре явных settled-исхода. Аренда события завершается одним из глаголов `Complete`, `Abandon`, `Reject`, `Defer` вместо бинарного «результат или таймаут». `Complete` публикует результат, `Abandon` возвращает событие в очередь без публикации, `Reject` закрывает событие с причиной отказа, `Defer` откладывает событие до указанного момента. Каждый исход пишется в журнал и в ответ `get_result` как явный исход задачи. Сейчас бинарная модель вынуждена называть таймаутом то, что на самом деле имеет разные причины. `indeterminate` из ADR 0039 пункт 5 остаётся исходом для потерянного ответа внешнего действия и сюда не входит: все четыре новых исхода означают, что воркер знает, что сделал.

3. Продление visibility timeout. Пока агент работает над событием, аренда события продлевается, чтобы не истекать посреди длинной задачи. Продление привязано к живой задаче и прекращается вместе с ней, по `Complete`, `Abandon`, `Reject`, `Defer` или отмене. Просроченная аренда без продления по-прежнему инвалидируется fencing-токеном из T3.51. Это разные механизмы: fencing решает, кто имеет право писать после краша, а продление не даёт аренде исчезнуть у работающего воркера.

4. WatchOutcome. Один результат воркера будит все процессы, открывшие файл состояния, без общего брокера. Факт завершения вместе с результатом пишется в файл, и каждый процесс, который держит файл открытым, видит исход через своё чтение, а не через отдельный вызов брокера. Это механизм `get_result` для пятнадцати агентов в разных процессах. Время жизни Watch из ADR 0019 и честная доставка из ADR 0026 сохраняются: WatchOutcome задаёт способ распространения результата по уже открытому файлу, а не новую лестницу уведомлений.

5. `wake_between` как точка подъёма. Агент, ожидающий события, просыпается в случайной точке заданного окна, а не все одновременно. При пятнадцати агентах, ждущих одного события, это разносит пробуждение по времени и убирает одновременный штурм. Окно задаётся контрактом ожидания, а не настройкой каждого агента. Это точка подъёма ожидающих, а не джиттер повторной доставки из ADR 0017 и не квота backpressure из ADR 0038 пункт 2.

6. Precheck с `init_event_timeout`. Перед тем как ждать, Beacon выполняет precheck с таймаутом по умолчанию 10 секунд. Если bypass mode не настроен, run завершается с `precheck_failure` и понятной причиной, а не молча висит. Причина попадает в ответ `wait_result` и в журнал. Это проверка на старте до входа в ожидание, а не failure contract вызова hook из T3.50: там фиксируется отказ уже вызванного hook, здесь конфигурация проверяется до запуска ожидания.

## Причина

1. longhaul добавляет третий исход `tools/call` `InputRequired`, паузу, пока клиент не пришлёт ввод ([X, строка 22](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L22), [README, blocks 30-36](https://github.com/slatino-dev/longhaul/blob/main/README.md#L30-L36)). Это готовый аналог `wait_result`, который ждёт человека, а не истекает по таймеру. Код не берётся: проект на Rust, привязан к черновику RC 2026-07-28, а `SqliteStore` ловит гонку только внутри одного соединения и требует `BEGIN IMMEDIATE` между процессами ([X, строка 24](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L24)). Отмена задач опрашивается каждые 250 мс.

2. mqlite различает четыре глагола завершения аренды `Complete`, `Abandon`, `Reject`, `Defer` рядом с Peek-Lock и visibility timeout ([X, строка 37](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L37), [README, Why mqlite](https://github.com/mqlitehq/mqlite/blob/main/README.md#why-mqlite)). Beacon сейчас различает только результат и таймаут, поэтому теряет причину завершения. Встроенный режим не поддерживает общий файл между процессами, второй процесс получает `ErrDBLocked` ([X, строка 39](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L39)).

3. goqite умеет продлевать таймаут сообщения на время длинной задачи ([X, строка 50](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L50), [README, Features](https://github.com/maragudk/goqite/blob/main/README.md#features)). Это точная потребность `wait_result`: аренда события не должна истекать, пока агент ещё работает. Это библиотека, а не сервер, без MCP и results API, а пример требует одного соединения через `db.SetMaxOpenConns(1)` ([X, строка 52](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L52)).

4. sqliteq доставляет `WatchOutcome` всем процессам, открывшим файл, вместе с результатом воркера ([X, строка 63](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L63), [README, Features](https://github.com/goptics/sqliteq/blob/main/README.md#features)). Это готовый механизм `get_result` для агентов в разных процессах без брокера. SQLite не знает pub/sub, поэтому под капотом опрос файла каждые 50 мс; нужен cgo и есть оговорка про повторный захват задач при позднем открытии файла ([X, строка 65](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L65)).

5. nidra-protocol поднимает агента в случайной точке окна режимом `wake_between` и называет это защитой от thundering herd ([X, строка 104](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L104), [README, Three Wake Modes](https://github.com/Fredess74/nidra-protocol/blob/main/README.md#three-wake-modes)). При пятнадцати агентах на одном событии это снимает одновременный подъём. Проект живёт только на Railway, без локального режима, на Python ([X, строка 106](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L106)).

6. kaos-control делает precheck за `init_event_timeout_seconds`, по умолчанию 10 секунд, и завершает run с `precheck_failure`, если bypass mode не настроен ([X, строка 132](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L132), [README](https://github.com/kcsinclair/kaos-control/blob/main/README.md)). Так `wait_result` возвращает причину сразу, а не молча ждёт. Проект под AGPL-3.0, бинарник около 250 MB при лимите Beacon меньше 100 MB ([X, строка 134](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L134)).

Числа в разделе «Перформанс» X-отчёта это заявления авторов проектов, а не измерения Beacon ([X, строка 264](/home/nbook/Projects/tmp/beacon-research/X-wave.md#L264)). Ни одно из них не переносится в контракт как показатель Beacon.

## Последствия

- Из каждого пункта выходит одна небольшая карточка в `docs/tasks/` по правилам ADR 0020. Карточки этим ADR не создаются и будут созданы отдельными задачами.
  - Фича 1: `internal/domain/types.go`, `internal/infra/mcp/schema.go`, `internal/infra/mcp/tool_wait_result.go`, `internal/infra/mcp/server.go`, `internal/app/taskcli/service.go`, `internal/infra/taskstore/store.go` и тесты к ним.
  - Фича 2: `internal/domain/types.go`, `internal/domain/transition.go`, `internal/infra/taskstore/store.go`, `internal/infra/mcp/tool_ack.go`, `internal/infra/mcp/tool_get_result.go`, `internal/infra/mcp/schema.go` и тесты к ним.
  - Фича 3: `internal/domain/types.go`, `internal/infra/taskstore/store.go`, `internal/infra/store/db.go`, `internal/app/taskcli/service.go`, `internal/infra/mcp/tool_wait_result.go` и тесты к ним.
  - Фича 4: `internal/domain/fanout.go`, `internal/infra/store/db.go`, `internal/infra/taskstore/store.go`, `internal/infra/mcp/tool_get_result.go`, `internal/infra/mcp/tool_wake.go`, `internal/app/taskcli/service.go` и тесты к ним.
  - Фича 5: `internal/domain/fanout.go`, `internal/domain/types.go`, `internal/infra/ladder/transitions.go`, `internal/app/taskcli/service.go`, `internal/infra/mcp/tool_wake.go` и тесты к ним.
  - Фича 6: `cmd/beacon/doctor.go`, `cmd/beacon/mcp.go`, `cmd/beacon/main.go`, `internal/app/taskcli/service.go`, `internal/infra/mcp/server.go`, `internal/domain/verdict.go` и тесты к ним.
- Проверка карточек: `tools/call` возвращает `InputRequired` вместо таймаута, когда нужен ввод клиента, и ввод возвращает в ту же задачу; четыре глагола дают четыре разных исхода в журнале и в `get_result`, а `indeterminate` остаётся отдельным исходом; аренда события жива, пока задача работает, и прекращается по явному исходу или отмене, а fencing инвалидирует только просроченную аренду; результат воркера виден всем процессам с открытым файлом без вызова брокера; пятнадцать ожидающих просыпаются в разных точках окна, а не одной командой; не настроенный bypass даёт `precheck_failure` с причиной до входа в ожидание.
- ADR 0001, 0003, 0009, 0014, 0017, 0019, 0020, 0024, 0026, 0028, 0031, 0036, 0038, 0039 и 0040 не переписываются. Набор MCP-инструментов остаётся прежним: меняются исходы вызова, а не число инструментов. Fencing T3.51, claim T3.47, backpressure ADR 0038 пункт 2, `needs_input` ADR 0031 и T3.21, `indeterminate` ADR 0039 пункт 5 и T3.64, quorum ADR 0038 пункт 5, общий gate T3.49 и failure contract T3.50 остаются на своих местах. Ключи разные: владелец аренды события из фичи 3 определяет продление, ключ воркера из фичи 4 определяет распространение результата, окно из фичи 5 определяет точку подъёма, а причина из фичи 6 определяет отказ до ожидания.

## Отклонённое

Сводить InputRequired к состоянию `needs_input` из ADR 0031 или выдавать паузу за таймаут. Это разные уровни: состояние задачи и исход протокола вызова.

Оставлять бинарную модель результат/таймаут или приравнивать четыре исхода к `indeterminate` из ADR 0039. Явный глагол завершения говорит, что сделал воркер, а потерянный ответ такого не говорит.

Дать аренде события истечь посреди задачи и полагаться только на fencing. Fencing не вернёт аренду работающему воркеру.

Ставить общего брокера для рассылки результата или заменять им Watch из ADR 0019. Распространение идёт по уже открытому файлу.

Будить всех ожидающих в одну точку или подменять это джиттером повторной доставки из ADR 0017. Разнос подъёма и джиттер повтора решают разные задачи.

Ждать полный таймаут при не настроенном bypass или называть это failure contract из T3.50. Precheck отказывает до старта ожидания.
