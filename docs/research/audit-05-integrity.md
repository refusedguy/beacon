# Аудит 05: пропущенные разрывы целостности

Отчёт проверяет пять направлений задания по состоянию `origin/main` на коммите `3137a74c`. Источники: `AGENTS.md`, `GLOSSARY.md`, `tools/beacon-gates/gate/config/layers.json`, карточки `docs/tasks/AUD-*.md` и код по адресам ниже. Задача: зафиксировать разрывы, сверить их с существующими карточками и завести карточки только на то, что в них не вошло. Код в этом PR не меняется.

## Измерение

Числа сняты прямым чтением кода. `runnerFor` содержит 33 ключа, каталог содержит 24 записи, 9 команд есть в диспетчере, но отсутствуют в каталоге. У трёх операций закрытия три разных формата записи. У сеттеров реестра генераций три определения и ноль вызовов вне тестов. У расписаний два хранилища: JSON-файл в CLI и таблица в SQLite без адаптера под порт `ScheduleStore`.

## Таблица разрывов

| Разрыв | file:line и символ | Покрыто AUD | Целевое решение | Эффект |
| --- | --- | --- | --- | --- |
| Процесс открывает БД без production-обёрток | `cmd/beacon/mcp.go:698` `openProcessStore`, `:702` `store.Open` | AUD-04 | Вызвать `store.OpenProduction`, добавить тест роста `persist.seq` и редактирования токенов | Все записи идут через `Redact` и `ObservedPersist`, `persist_progress.seq` растёт |
| Три файловых замка на карточки | `internal/infra/taskstore/store.go:401` `LockPath` (`task-<hash>.lock`), `:422` `AcquireLock`; `internal/infra/mcp/tool_task_claim.go:312,317` `.beacon-task-claim.lock`; `internal/infra/mcp/tool_task_close.go:323,324` `.beacon-task-close.lock`, `:327,328` sidecar `.beacon-task-close.json` | AUD-05 | Один замок на каталог `docs/tasks`, выровнять поля закрытия | Claim, close и CLI не перезаписывают друг друга |
| Закрытие по MERGED идёт без замка | `cmd/beacon/mcp.go:129` `closeWiringCard`, `:157` `closeLinkedCardFile`, запись через `:124` `wiringCardWriteFile` в `:150` и `:179` | AUD-04 ставит лок, AUD-05 выравнивает формат | Взять общий замок и писать те же поля, что `taskstore.CloseCard` | Закрытие вотча атомарно и совпадает с закрытием через CLI |
| Третий формат закрытия не описан в AUD | `cmd/beacon/mcp.go:174-179` пишет `Статус`, чистит `Состояние` и `Вопрос`, добавляет подтверждение; `:147-150` пишет `Статус` и подтверждение; `internal/infra/taskstore/close.go:52-54` `managedCloseFields` содержит шесть полей; `internal/infra/mcp/tool_task_close.go:327` держит отдельный sidecar | Частично AUD-04 и AUD-05, но оба называют два формата и не упоминают этот | Добавить этот путь в шаги AUD-04 или AUD-05: все три вызова идут через `ApplyCloseFields` | Статус, причина и время читаются из `.md` независимо от канала закрытия |
| Локальные парсеры ID и статусов расходятся с контрактом | `internal/infra/taskstore/store.go:373` `IsID`, `:281` `IsValidStatus`, `:261` `cardStatuses`; `internal/app/taskcli/service.go:149` `idrRE`, используется в `:322` и `:515`; `cmd/beacon/task.go:91` `whyCardID`, `:98` `whyEdgeRE`, `:346` `whyClosed`; `internal/app/orchestrator/store.go:73` `dependencyCardRef`, используется в `:171` | AUD-07, AUD-08 | Заменить regex и списки на `taskstore.IsID` и `taskstore.IsValidStatus` через фасад `internal/app/taskcli/taskstore_facade.go:28`, считать `duplicate` закрытым | `ATX-R01`, `RS-CMP-06`, `blocked` и `duplicate` принимаются во всех четырёх точках |
| Глобальный кэш колонок по `*sql.Tx` | `internal/infra/store/backfill_registry.go:56-60` globals, `:62-79` `registryColumns`, `:63` приведение к `*sql.Tx` | AUD-12 | Перенести кэш в локальную переменную вызова | После `ALTER TABLE` запрос видит актуальные колонки |
| Метрика чтения файлов растёт на каждом тике | `cmd/beacon/daemon.go:756` `daemonPollCards`, `:757` `taskstore.LoadCards`, `:786` интервал 30 с, `:801` тик | AUD-12 | Читать через индекс без инкремента `file_adapter_reads_total` | Метрика перестаёт расти на пустом цикле |
| Каталог команд отстаёт от диспетчера | `cmd/beacon/main.go:243-277` `runnerFor` (33 ключа); `cmd/beacon/catalog.go:32-54` (21 запись) плюс `up.go:25`, `human.go:21`, `main.go:76` (итого 24). В каталоге нет `ci`, `daemon`, `deliver`, `metrics`, `orchestrate`, `orchestrator`, `pr`, `registry`, `workload` | AUD-04 | Синхронизировать каталог с `runnerFor`, добавить тест равенства множеств | `beacon list --format json` совпадает с доступными командами |
| `graphReady` не попадает в ответ claim | `internal/infra/mcp/tool_stats.go:567` `graphReady`; в `internal/infra/mcp/tool_claim.go` поля нет | AUD-04 | Вернуть `graphReady` в `TaskClaimOutput` и подключить `SetGraphIndexReader` | Исполнитель видит свежесть графа в ответе claim |
| Источник реестра не подключён | `cmd/beacon/registry_list.go:44` `registryListSource`, не вызывается в `cmd/beacon/mcp.go:710` `buildMCPDependencies` | AUD-04 | Подключить читателя к `beacon_stats` | Счётчики реестра появляются в статистике |
| Сеттеры реестра генераций не вызываются | `internal/infra/mcp/tool_claim.go:397` `SetGenerationStore`, `:406` `GenerationStore`; `internal/infra/mcp/tool_ack.go:472` `SetGenerationStore`; `internal/infra/cibridge/bridge.go:382` `WithGenerationStore`. Вызовов вне тестов нет. Единственная реализация `internal/app/orchestrator/claim.go:79` живёт в памяти | AUD-04 упоминает `GenerationLedger`, но не фиксирует ноль вызовов и потерю при рестарте | Завести один общий хранимый реестр и вызвать сеттеры в composition root. В карточку INTG-02 | Просроченный claim отвергается после resolve и после рестарта демона |
| Расписания пишутся в JSON, хотя таблица SQLite есть | `cmd/beacon/task.go:828-829` `scheduleStoreFor` вызывает `NewFileScheduleStore(schedules.json)`; `:825` комментарий "пока BS.33 не влит"; `internal/infra/server/scheduler.go:64` утверждает, что порт реализован поверх таблицы; `internal/infra/store/schedules.go:73,111,150,184` содержит `SaveSchedule`, `DueSchedules`, `MarkTriggered`, `DisableSchedule`, но не `Create`, `Get`, `List`, `Update`, `Delete` | Не покрыто | Добавить CRUD в `store.DB`, адаптер под `server.ScheduleStore`, перевести CLI. В карточку INTG-03 | Один источник расписаний, переживающий рестарт |

## Новое, что не вошло в AUD

Три пункта требуют отдельных карточек. Остальные пункты покрыты AUD-04, AUD-05, AUD-07, AUD-08 и AUD-12 и в этом пакете не дублируются.

1. `cmd/beacon/server.go:130` определяет `serverRunner`, но в `cmd/beacon/main.go:243-277` нет ключа `server`, а в `cmd/beacon/catalog.go:32-54` нет записи. Отдельного бинарника `beacon-server` в репозитории нет. Команда `beacon server` не диспетчеризуется. AUD-04 синхронизирует существующие ключи и этот случай не описывает. Карточка INTG-01.
2. Сеттеры `SetGenerationStore` и `WithGenerationStore` определены, но не вызваны. Реализация `ClaimGeneration` в `internal/app/orchestrator/claim.go:79` хранит состояния в структурах процесса. После рестарта генерации сбрасываются. AUD-04 говорит про подключение `GenerationLedger`, но не про долговечность и ноль вызовов. Карточка INTG-02.
3. Порт `server.ScheduleStore` в `internal/infra/server/scheduler.go:64` заявлен как реализованный поверх таблицы SQLite. Реализации нет. CLI использует `schedules.json`. Карточка INTG-03.

Третий формат закрытия из таблицы выше не получает отдельной карточки. Он ложится в шаги AUD-04 и AUD-05 как уточнение, потому что оба уже владеют `cmd/beacon/mcp.go` и форматом закрытия.

## Границы файлов

Карточки INTG-01, INTG-02 и INTG-03 имеют непересекающиеся `owner_files`. Все пути существуют в дереве. Каждая карточка лежит в одном слое: `cmd` для INTG-01, `internal/app` для INTG-02, `internal/infra` для INTG-03. Новых рёбер импорта между слоями не требуется.
