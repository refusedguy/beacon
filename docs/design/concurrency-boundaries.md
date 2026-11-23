# Границы параллелизма

Режим документа: reference. Документ перечисляет места, куда не добавляется параллелизм, и три лимита, которые нельзя обходить. Он не меняет код и не заменяет гейты.

Сверка сделана по `origin/main` `e92dd65f3857` 23 ноября 2026 года. Источник: карточка CN-06 из `/tmp/opencode/backlog/17-concurrency.md`, собранная на `d0e58d10`. Все пути и строки ниже проверены по текущему HEAD, а не скопированы из карточки. Если правка сдвигает строку, поправьте ссылку в том же PR.

Прежде чем добавить горутину, найдите целевую функцию в таблице зон. Если функция попадает в зону, горутину не добавляйте. Для остальных мест проверьте лимиты и считайте внутренние горутины отдельно от слотов.

## Пять зон без параллелизма

| Зона | Путь и строка на HEAD | Почему порядок обязателен |
| --- | --- | --- |
| Один writer SQLite | `internal/infra/store/db.go:54-84` описывает `DB` с `writeDB`, `writeConn` и `writeCh`. `:159` открывает пул записи на одно соединение, `:163` открывает пул чтения на четыре. `:219-241` закрепляет `writeConn`, создаёт канал и запускает `writeLoop`. `:329-379` показывает `rawWrite` и цикл, который берёт задания из `writeCh`. | Все `Write` идут через один канал и один цикл. Второй writer не снимает ожидание SQLite lock, а меняет порядок транзакций и нарушает ожидание результата из ATX-16. Чтение уже параллельно и отдельного разрешения не требует. |
| `admissionLock` | `cmd/beacon/orchestrate.go:639-661` объявляет `dispatchAction` и `admissionLock`. `:667-794` содержит `acquire` и `admit`, проверка бюджета стоит в `:781`. `launchElement` в `:838-864` держит слот до конца тела, `launchPlan` в `:866-890` использует тот же замок. | Параллельные действия делят слот, остальные ждут всех читателей. `admit` проверяет guard до и после получения слота, поэтому неготовое действие не занимает слот. Обход замка отключает проверки бюджета и отмены. |
| Упорядоченные generation, MintID, planWave | `internal/app/orchestrator/claim.go:92-190`: `ClaimGeneration` хранит `seq`, `nextLocked` в `:181-184` выдаёт следующий номер, `IssueFor` в `:189` привязывает его к владельцу. `internal/infra/taskstore/store.go:678-717`: `MintID` проверяет общий `claimed` и пишет в него. `cmd/beacon/orchestrate.go:595-635`: `planWave` помечает giant-шаг `Sequential` в `:631`. | Номер generation нужен для stale-claim detection, общий `claimed` не допускает повтор ID, а порядок шагов задаёт план. Параллельная выдача ломает все три контракта. CN-05 может добавить ожидание предыдущего sequential шага, но не распараллеливает эту зону. |
| Побайтовые гейты | `tools/beacon-gates/cardindex/check.go:126-132` сравнивает собранный индекс с зафиксированным через `bytes.Equal` в `:128`. `internal/infra/mcp/server.go:1110` фиксирует побайтовый контракт `tools/list`; тот же контракт упомянут в `:198`, `:326`, `:367` и `:940-943`. `tools/beacon-gates/gate/tool_contract.go:77` проверяет сохранённый ответ. | Гейты сравнивают байты. Любой нестабильный порядок строк, включая обход map, даёт красный результат без изменения смысла. Порядок вывода здесь часть контракта. |
| Файловый замок карточек | `internal/infra/taskstore/store.go:431-521`: имя `.beacon-tasks.lock`, `TasksLockPath` и `AcquireLock` через `Flock(LOCK_EX\|LOCK_NB)` с 50 попытками. Адаптеры повторяют имя в `internal/infra/mcp/tool_task_claim.go:172-177`, `:414` и `internal/infra/mcp/tool_task_close.go:447`. `internal/infra/batchapply/apply.go:96-100`, `:126-132` и `:1648-1649` ограничивают время удержания замка кусками записи. | На `docs/tasks` работает один файловый замок для taskstore, claim и close. Чтение карточек можно параллелить, запись в карточку и общий индекс нельзя. Ссылка карточки CN-06 на `apply.go:1373` устарела: на HEAD эта строка не содержит кода замка. |

## Три лимита

| Лимит | Источник на HEAD | Правило |
| --- | --- | --- |
| Semaphore GitHub, 2 | `internal/infra/github/graphql.go:41` задаёт `MaxParallel = 2`. `:303` создаёт канал, `:337-338` занимает и освобождает слот, `:439` возвращает канал. | Не поднимайте `MaxParallel` и не открывайте второй клиент, чтобы обойти канал. Параллелизм внутри запросов GitHub упирается в этот semaphore. |
| Token bucket, 2 запроса в секунду с burst 4 | `internal/infra/github/batch_provider.go:34-35` задаёт `batchRate` и `batchBurst`, `:137` создаёт `rate.NewLimiter`. | Batch, ping и merge расходуют токены. Не заменяйте вызовы через лимитер прямыми запросами. |
| Бюджет слотов, 4 | `internal/domain/orchestrator.go:17`, константа `DefaultSlotsTotal = 4`. Тот же бюджет задаёт `cmd/beacon/orchestrate.go:426`, `fallbackDefaultSlotsTotal = 4`. | Значение совпадает с карточкой на текущем HEAD. Счётчик учитывает слоты волн, а не внутренние горутины воркеров. Их число проверяйте отдельно. Ограничения 4 в CN-01..CN-05 относятся к воркерам и не отменяют этот бюджет. |

## Карточки CN-01..CN-05

Карточек CN-01..CN-05 в `docs/tasks` на HEAD нет. Ссылки в их разделах Dependencies или Out of scope не вставлены: чужие OwnerFiles не правятся. При создании карточек добавьте в нужный раздел строку `См. docs/design/concurrency-boundaries.md` с номером зоны или лимита из таблицы.

| Карточка | Что добавляет | Граница, которую сохраняет |
| --- | --- | --- |
| CN-01 | Параллельное чтение в `MeasureIDCollisions` | Зона 5. Чтение можно, запись в карточки нельзя. Зона 4: порядок коллизий остаётся сортированным. |
| CN-02 | Параллельные проверки backlink в `ListWithBacklink` | Зона 5. Только чтение git, порядок `entries` сохраняется. |
| CN-03 | Fallback `PRState` через `BoundedRun` | Semaphore GitHub и token bucket. Пул не должен обходить лимитер провайдера. |
| CN-04 | Чтение и парсинг в `RebuildDir` | Зона 1. Запись идёт одним `replaceCards`, новых writer нет. Зона 4 требует стабильного порядка `changed`. |
| CN-05 | Ожидание sequential шагов и production-вызов `launchPlan` | Зоны 2 и 3. Вызов идёт через `admit`, generation и Seed не меняются. |

## Проверка ссылок

Повторите сверку после изменения любого файла из таблиц:

```
git grep -n -E 'DefaultSlotsTotal = |fallbackDefaultSlotsTotal = |MaxParallel = |batchRate  = |batchBurst = ' -- internal/domain/orchestrator.go cmd/beacon/orchestrate.go internal/infra/github/graphql.go internal/infra/github/batch_provider.go
git grep -n -E 'func planWave|type admissionLock|func \(l \*admissionLock\) admit|func launchPlan' -- cmd/beacon/orchestrate.go
git grep -n -E 'tasksLockName = |func AcquireLock|func MintID|bytes.Equal\(rebuilt' -- internal/infra/taskstore/store.go internal/infra/mcp/tool_task_claim.go tools/beacon-gates/cardindex/check.go
```

Это справочные команды, а не гейт. Изменение кода в перечисленных зонах проверяется ревью diff по OwnerFiles этих зон.

## Вне области

Документ не меняет `writeLoop`, `admissionLock`, semaphore GitHub, файловый замок и побайтовое сравнение. Он не отменяет ограничения карточек CN-01..CN-05 и не добавляет новых проверок в CI.
