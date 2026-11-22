# Пункты 5 и 8 ТЗ: остаток по ID/статусам и хранилищу демона

Дата: 2026-11-22. Режим: только чтение. `go test`, `go build`, гейты, `batch-apply` и ручной merge не запускались. Все выводы сняты с `origin/main` на коммите `b693e56d`. Production Go-код не менялся. Каждое утверждение ниже опирается на путь, номер строки и символ.

Источник задания: пункт 5 (рассинхрон ID и статусов) и пункт 8 (баги хранилища и цикла демона). Соседний отчёт `docs/research/research-dir5-integrity.md` закрывает другой срез направление 5 (merge policy, wake, лок и формат закрытия) и здесь не повторяется.

## Что уже влито, карточки не создаём

| Point | Символ и место | Факт влития | Остаток |
| --- | --- | --- | --- |
| AUD-07, пункт 5 | `internal/app/taskcli/service.go:188` `taskstore.IsValidStatus(v)`, `:323` `IsID(d)`, `:601` `IsID(ref)`; `cmd/beacon/task.go:94` `whyCardID`, `:350` `whyClosed` | PR #1477, коммит `63891e1f`. Локальный `idrRE` удалён. `list --status` принимает `blocked` и `duplicate`. `whyCardID` понимает `ATX-R01` и `RS-CMP-06`. `whyClosed` считает `duplicate` закрытым | Нет. Проверено по коду на `b693e56d` |
| BCX-40 | `docs/tasks/BCX-40.md:2` `Статус: done` | Инкрементальный индекс в `internal/infra/taskstore/index.go` в main | Нет |
| AUD-05, пункт 8 | PR #1469, коммит `0efa2344`. Один лок и формат закрытия в `internal/infra` | `internal/infra/taskstore/store.go:408` `TasksLockPath`, `internal/infra/taskstore/close.go:56` `managedCloseFields` | Вне `internal/infra` остаток есть, его ведёт соседний отчёт IN5-01 и IN5-04. Здесь не дублируем |
| INTG-03, пункт 8 | PR #1460, коммит `f0af1c57`. `internal/infra/store/schedules.go:273` `CreateSchedule`, `:305` `GetSchedule`, `:325` `ListSchedules`, `:358` `UpdateSchedule`, `:390` `DeleteSchedule`, `:420` `SetScheduleEnabled` | CRUD над таблицей `schedules` написан и в main | CRUD не подключён. См. ST5-04 |
| MEM-06, пункт 8 | PR #1465, коммит `5597372f`. `internal/app/taskcli/service.go:794` `ReapStaleCards` | Функция возврата зависших `in_progress` в `todo` написана и покрыта тестами | Нет вызова вне тестов. Подключение закреплено за MEM-04, здесь не дублируем |

Статус карточек AUD-07 и AUD-05 в `docs/tasks` остался `todo`, хотя код влит. Это расхождение книжки карточек, а не пункта 5 или 8. BCX-40 отвечает за синхронизацию, поэтому новой карточки не заводим.

## Уже закреплено за открытыми карточками, не дублируем

| Карточка | Остаток с доказательством | Почему не наша карточка |
| --- | --- | --- |
| AUD-08, пункт 5 | `internal/app/orchestrator/store.go:73` `dependencyCardRef = regexp.MustCompile("(?i)^t\d+\.\d+$")`, используется в `:171` `Satisfied`. Строки `ARDH-08`, `ATX-R01`, `BS.1`, `S1.001` проваливаются в `existsOnMain` и трактуются как git-путь. `docs/tasks/AUD-08.md:2` `Статус: todo`. Коммит `be4ab029` правил только OwnerFiles карточки | Карточка есть, владелец файлов `internal/app/orchestrator/store.go`. Наша выдача её не повторяет |
| AUD-12, пункт 8 | `internal/infra/store/backfill_registry.go:57-59` `registryColumnsMu`, `registryColumnsTx`, `registryColumnsData` и `:62` `registryColumns` кэшируют карту колонок по указателю `*sql.Tx`. `cmd/beacon/daemon.go:866` `daemonPollCards`, `:867` вызов `taskstore.LoadCards`. `internal/infra/taskstore/store.go:305` `fileAdapter`, `:317` `fileAdapter.ReadFile(filePath)` инкрементирует `file_adapter_reads_total` на каждом проходе. `docs/tasks/AUD-12.md:2` `Статус: todo` | Владелец файлов карточки совпадает с перечисленными путями. Не дублируем |
| MEM-04, пункт 8 | `cmd/beacon/mcp.go:1937` `ReconcilePass` и `:1977` `ReconcileLoop` не вызывают `cfg.db.ArchiveRun`, `cfg.db.Checkpoint` и `ReapStaleCards`. Определения: `internal/infra/store/archive.go:198` `ArchiveRun`, `internal/infra/store/db.go:248` `Checkpoint`, `internal/app/taskcli/service.go:794` `ReapStaleCards`. Вне тестов вызовов нет. `docs/tasks/MEM-04.md:2` `Статус: todo`, шаги прямо перечисляют все три подключения | MEM-04 уже владеет `cmd/beacon/mcp.go` и `cmd/beacon/daemon.go`. Не дублируем |
| MEM-02, пункт 8 | `cmd/beacon/watch.go:234` `c.run(c.root, "opencode", []string{"api", "session.list"}, nil)`; `cmd/beacon/agents.go:184` `app.agents` и `:187` `session.list`. Готовый порт `newReadOnlyOpencodePort` в `cmd/beacon/opencode.go:151`. `docs/tasks/MEM-02.md:2` `Статус: todo`. AUD-12 тоже заявляет `cmd/beacon/agents.go` в OwnerFiles | Две карточки уже претендуют на файлы. Не дублируем |

## Новые находки

| ID | Приоритет | Проблема | file:line и символ | Предложение | Тест-кейс |
| --- | --- | --- | --- | --- | --- |
| ST5-01 | P1 | Docs-gate отвергает допустимые статусы карточек | `tools/beacon-gates/docsgate/docs.go:44-46` `isTaskStatus`, вызов в `:240` `checkStatus(f, "DOC-TASK-STATUS", isTaskStatus, &fs)` | Дополнить множество `blocked` и `duplicate` из `internal/infra/taskstore/store.go:261` `cardStatuses`. Импорт недоступен из-за границы модулей, поэтому синхронизировать список и добавить тест на все шесть значений | Фикстура карточки со `Статус: blocked` и со `Статус: duplicate` не даёт finding `DOC-TASK-STATUS`. Старые четыре статуса по-прежнему проходят |
| ST5-02 | P1 | Batch-apply отвергает допустимые статусы карточек | `internal/infra/batchapply/apply.go:255` `allowedCardStatuses = []string{"todo", "done", "dropped"}`, проверка в `:279` `{"status", c.Status, allowedCardStatuses}` | Расширить до канонического множества `cardStatuses`. Оба списка лежат в разных модулях, поэтому добавить тест-контракт, который сверяет их | Батч с карточкой `in_progress`, `blocked` или `duplicate` проходит `cardFieldProblems`. Недопустимый статус по-прежнему даёт `invalidField` |
| ST5-03 | P2 | Разбор связи коммита не видит новые форматы ID | `internal/infra/taskstore/store.go:876` `linkageCardRE = regexp.MustCompile("\b[A-Z][A-Z0-9]*[-.][0-9]+(?:\.[0-9]+)?\b")`, использован в `:891` `ParseLinkage` | Заменить локальную грамматику на `exactIDRE` из `:108`, которая уже понимает `ATX-R01` и `RS-CMP-06`. Сохранить исключение строк, начинающихся с `ADR` | `ParseLinkage("fix(atx): guard (ATX-R01)\n\nСсылка на ADR 0295.")` возвращает `CardRef == "ATX-R01"`. `CheckLinkage` с валидными портами даёт `LinkageLinked` |
| ST5-04 | P1 | Расписания пишутся в JSON, CRUD в SQLite не подключён | `cmd/beacon/task.go:831-832` `scheduleStoreFor` возвращает `server.NewFileScheduleStore(filepath.Join(cfg.StateDir, "schedules.json"))`. Комментарий в `:828` говорит «пока BS.33 не влит», хотя INTG-03 влит. Методы `internal/infra/store/schedules.go:273-420` и `:113` `DueSchedules` не имеют вызовов вне тестов | Добавить адаптер `server.ScheduleStore` поверх `internal/infra/store.DB` и подключить его в `scheduleStoreFor`. Перенести существующие записи из `schedules.json`. Обновить комментарий в `cmd/beacon/task.go:828` | Расписание, созданное через `beacon task schedule`, читается после повторного открытия store из SQLite. Файл `schedules.json` больше не растёт. Старые записи доступны после миграции |

Приоритет P0 в этой подборке пустой. Все P0-пункты пункта 8, найденные в аудите, уже разнесены по AUD-12 и MEM-04 и находятся в работе. Новые находки не блокируют merge и не ломают запись, поэтому идут в P1 и P2.

## Разбор находок

### ST5-01 и ST5-02. Три разных множества статусов

Каноническое множество живёт в `internal/infra/taskstore/store.go:261` `cardStatuses` и содержит шесть значений: `todo`, `in_progress`, `blocked`, `done`, `dropped`, `duplicate`. `taskstore.IsValidStatus` в `:281` читает его. AUD-07 перевёл taskcli на этот контракт. Два других валидатора остались на старых четырёх и трёх значениях.

`isTaskStatus` в `tools/beacon-gates/docsgate/docs.go:44` не знает `blocked` и `duplicate`. Он вызывается для каждой карточки в `:240`, поэтому docs-gate пометит такую карточку как `DOC-TASK-STATUS`. Сейчас в `docs/tasks` нет карточек с этими статусами, поэтому дефект латентный.

`allowedCardStatuses` в `internal/infra/batchapply/apply.go:255` не знает `in_progress`, `blocked` и `duplicate`. Проверка в `:279` сработает на батч, который описывает карточку в работе. Это мешает переносить уже начатые карточки через batch-apply.

Оба валидатора находятся в отдельных Go-модулях, поэтому прямой импорт `internal/infra/taskstore` запрещён границей `layers.json`. Это же ограничение зафиксировано в `docs/tasks/ARDH-08.md` для списка полей. Решение не в копировании константы, а в тесте, который сверяет три списка и падает при расхождении.

### ST5-03. Грамматика ID в разборе коммитов

`linkageCardRE` в `internal/infra/taskstore/store.go:876` принимает префикс из заглавных букв и цифр, затем дефис или точку и цифры. Форматы `ATX-R01` и `RS-CMP-06` не подходят, потому что после дефиса идёт буква. `ParseLinkage` в `:891` возвращает пустой `CardRef`. `CheckLinkage` в `:851-861` требует одновременно валидные ADR и карточку, поэтому коммит с одной такой ссылкой получает `LinkageMissing`.

В AUD-07 эта грамматика уже расширена для `taskstore.IsID`, но в `linkageCardRE` правка не попала. Функция `CheckCommitLinkage` в `internal/app/taskcli/service.go:2762` пока не имеет вызовов вне себя, поэтому дефект латентный. Когда linkage начнут проверять в CLI, коммиты с новыми ID начнут получать предупреждение без причины.

### ST5-04. Два хранилища расписаний

`cmd/beacon/task.go:831` объявляет `scheduleStoreFor` как точку входа для всех команд управления расписаниями. Функция возвращает `FileScheduleStore`, который пишет `schedules.json` в state dir. Комментарий в `:828` объясняет выбор тем, что BS.33 ещё не влит. BS.33 влит давно, а INTG-03 (`f0af1c57`) добавил полный CRUD поверх таблицы `schedules`.

Ни один из новых методов не вызывается в production. `DueSchedules` в `internal/infra/store/schedules.go:113` тоже не имеет вызывающих. Возникает разрыв: CLI пишет в файл, SQLite-таблица пуста. ADR 0313 в `docs/adrs/0313-control-plane-sqlite.md` прямо описывает SQLite как источник состояния control plane и называет расписания. Пока адаптер не подключён, это решение не работает. При подключении демона к SQLite без миграции созданные через CLI расписания исчезнут из видимости.

## Ограничения проверки

- Тесты, сборки, гейты и `batch-apply` не запускались по правилам репозитория. Все выводы получены чтением кода на `b693e56d`.
- Наличие открытых карточек AUD-08, AUD-12, MEM-02 и MEM-04 проверено по их файлам в `docs/tasks`. Их владельцев не меняли.
- Соседний отчёт IN5 снят с `8608917d` и сейчас влит как `b693e56d`. Пересечения по файлам между нашими находками и IN5-01..06 нет.
- Карточки этой подборки не применялись. Batch лежит в `docs/research/research-dir5-p1-store-batch.json` и ждёт команды владельца.
