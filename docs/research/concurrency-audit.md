# Аудит конкуренции и разделяемого состояния в beacon

Дата: 21 ноября 2026. Источник: чтение файлов, `git grep` и `go test -race` по явным каталогам `internal/app/event`, `internal/app/orchestrator`, `internal/infra/cibridge`, `internal/infra/wake`, `internal/infra/server`, `internal/infra/httpapi`, `internal/infra/mcp`, `internal/infra/store`, `cmd/beacon`. Код не менялся.

Этот аудит продолжает fp-in-go-audit, но не повторяет его. Там разобраны изменяемые globals и экспортируемые срезы. Здесь разобраны goroutine, каналы, мьютексы, `sync.Once`, контексты и порядок захвата блокировок. Изменяемые globals повторно не описываются.

## Итог

Расхождений с `-race` в существующих тестах нет. Все найденные риски латентные: текущие тесты их не покрывают. Два общих теста красные, но оба падают детерминированно и не связаны с конкуренцией. Это устаревший golden в `tools/list` и проверка проводки портов в `cmd/beacon`.

Главные риски лежат на границах жизненного цикла. Горoutine, запущенная вне запроса, переживает `Shutdown`. Запрос в хранилище возвращается до того, как его работа завершилась. Мьютекс удерживается во время внешнего вызова. Три эти ошибки дают потерю данных или взаимоблокировку, а не только шум в логах.

## 1. cibridge Run и Refresh

`Run` в `internal/infra/cibridge/bridge.go:405` зовёт `Refresh` сразу и дальше по тикеру. `Refresh` в `:508` экспортирован и вызывается напрямую из тестов и других точек (`internal/infra/wake/notify_test.go:382`).
`rebaseMu` и `rebased` в `:166-167` защищают карту откидок. `breakerMu` и `breakers` в `:172-173` защищают автоматы. `cycleID` локальная на вызов. Единой блокировки на один `Refresh` нет. Две параллельные копии `Refresh` над одним store читают один снимок и обе строят действия. Постоянные ключи в store не дают дублей обязательств, но счётчики `observe.PollCycleStart` и `PollCycleEnd` в `:520,528` перемешиваются, а в памяти каждый Bridge держит свои `rebased` и `breakers`.

Сценарий гонки. Поток A и поток B вызывают `Refresh` для одного Watch с одной reversion. Оба видят отсутствующий ключ в `rebased`. Оба решают сделать rebase. Защита от повтора срабатывает внутри каждого вызова отдельно, но не между ними.

How to prove. Тест с двумя goroutine, которые зовут `Refresh` на одном Bridge и одном store с подсчитывающим waker. Проверка: ровно одно wake на `(watchID, revision)`. Добавить `goleak` и прогнать с `-race -count=50`.

Fix. Поставить `refreshMu sync.Mutex` в начало `Refresh` и держать его до конца цикла. Альтернатива: atomic CAS флаг single-flight с возвратом 0 для второго вызова. Приоритет средний.

## 2. Daemon poll loop

`DaemonRun` в `internal/app/orchestrator/daemon.go:63` заканчивает один проход до следующего тика. Один тик sequential: checkall, subscribe, orchestrate, summary, verify, close-merged. Внутри одного `DaemonRun` гонки нет.

В `cmd/beacon/daemon.go:246-258` цикл реконсиляции и heartbeat живут в двух goroutine. Каждая имеет свой `done` канал и ждётся в `defer`. `runDaemonHeartbeat` в `:154` выходит по `ctx.Done()`. `startMCPDaemon` в `cmd/beacon/mcp.go:106-121` и `runReconciler` в `:1420-1442` делают cancel, ждут `done`, потом закрывают порты. Порядок корректный.

Остаточный риск. `pass()` в `internal/app/orchestrator/daemon.go:78` не имеет guards от повторного входа. Если два процесса или два вызова `DaemonRun` делят один store без `acquirePollerLock`, два прохода перекрываются. Blocking lock в `runReconciler:1421` закрывает этот случай в production.

How to prove. Тест с двумя `DaemonRun` на одном хранилище и подсчётом wake. Ожидание: один wake на ревизию.

Fix. Держать distributed lock на весь проход, а не только на старт цикла. Приоритет низкий.

## 3. Wake deliver и registry sync

`T211SessionRegistry`, `T211AdapterRegistry`, `T211DeliveryRegistry` в `internal/infra/wake/deliver.go:1003-1145` защищены `sync.RWMutex`. `SessionRegistry.Bind` в `:1264` и `Rows` в `:1325` копируют данные под блокировкой. `CursorDeliveryJournal` в `:1466` и `DeliveryLeaseGuard` в `:437` тоже держат мьютекс на всех операциях. `SetClock` в `:455` меняет часы под тем же мьютексом. Гонок на картах здесь нет.

`Registry` в `internal/infra/wake/registry.go:68` читает `deliverers` под `RLock` в `:127` и пишет под `Lock` в `:89`. `Deliver` в `:121` копирует ссылку на адаптер и вызывает его вне блокировки. Это правильно: адаптер может блокироваться на сети.

Риск в другом месте. `ServeHTTP` в `internal/infra/webhook/receiver.go:137` запускает `go h.record(d)` с `context.Background()`. `Server.Close` в `:255` зовёт `httpServer.Shutdown`, который ждёт только HTTP handlers. Отсоединённая goroutine не входит в это ожидание.

Сценарий утечки. Webhook получает 202, `Close` возвращается, а `record` ещё парсит payload и пишет в store. Если composition root закрывает store сразу после вебхука, запись идёт в закрытый store. При медленном recorder число goroutine растёт без границы.

How to prove. Тест с recorder, который блокируется на канале. Отправить N подписанных delivery, вызвать `Close`, снять блокировку, проверить, что все N записей завершились до возврата `Close`. Сегодня тест упадёт. `go test -race` с `-count` покажет рост goroutine через `runtime.NumGoroutine`.

Fix. Добавить `sync.WaitGroup` в `Receiver`. `Add` перед `go h.record`, `Done` в `record`. В `Server.Close` после `Shutdown` ждать эту группу с таймаутом. Либо заменить горoutine на один worker с ограниченной очередью. Приоритет высокий.

## 4. Event bus

`internal/app/event/bus.go` держит два мьютекса. `commitMu` в `:76` сериализует append и drain. `mu` в `:78` защищает `subs`, `nextSeq`, `watermark` и `lastPoison`. `Subscribe` в `:99` возвращает отписку с `sync.Once`. `drain` в `:264` копирует список подписчиков под `mu` и вызывает их без блокировки. `deliverOne` в `:284` ловит panic.

Здесь есть deadlock по контракту. `Commit` в `:216` берёт `commitMu` в `:226` и держит его до возврата. `drain` вызывается внутри этой секции в `:232` и зовёт подписчиков синхронно. Документ в `:214` прямо запрещает подписчику коммитить другую транзакцию, но ничего не проверяет. Подписчик, который вызовет `WithTx` или `Commit` на том же bus, встанет на `commitMu`, который держит его же вызывающий. Весь процесс встанет.

How to prove. Тест с подписчиком, который внутри вызывает `b.WithTx(ctx, func(tx) { tx.Emit(...) })`. Запустить с таймаутом. Сегодня тест повиснет.

Fix. Держать `commitMu` только на `assignSeqs`, потом отпустить и сделать `drain` отдельно. Порядок доставки при этом сохраняется через seq. Либо явно детектировать реентрантность и возвращать ошибку. Приоритет средний.

`internal/infra/server/eventbus/bus.go:77` делает snapshot под `mu` и доставляет вне блокировки. Там deadlock нет, потому что `commitMu` отсутствует.

`internal/infra/httpapi/server.go:266` держит `EventBus`. `Publish` в `:286` и `close` в `:363` шлют и закрывают каналы под одним `mu`. Отписка в `:323` тоже закрывает под `mu`. Send на закрытый канал исключён. Это место проверено и в правках не нуждается.

Остаточная проблема в `Server.Close` в `:627`. Сначала идёт `Shutdown`, потом `events.close()` в `:639`. SSE-хендлеры живут до закрытия канала. При активных SSE-клиентах `Shutdown` ждёт до `shutdownTimeout`, потом закрывает соединения насильно.

How to prove. Интеграционный тест с одним открытым SSE-соединением, замерить длительность `Close`.

Fix. Звать `events.close()` до `Shutdown`. Приоритет низкий.

## 5. Scheduler

`MemoryScheduleStore` в `internal/infra/server/scheduler.go:231` и `FileScheduleStore` в `:294` защищены мьютексами. Отдельные операции корректны.

Риск в `ScheduleService.setEnabled` в `:147`. Метод читает `Get` в `:152`, меняет `Enabled` и пишет `Update` в `:157`. Два вызова подряд блокировку не держат. Два параллельных `Pause` и `Resume` для одного id прочитают одно значение, и последний запишет своё. `Pause` может потеряться. Это lost update, не data race.

Второй риск в `buildSchedule` в `:183`. ID без ввода строится как `sched-%d` из `time.Now().UnixNano()`. Два вызова в одну наносекунду или с грубыми часами дадут одинаковый ID, и второй `Create` вернёт `ErrScheduleExists`.

How to prove. Тест с обёрткой store, которая считает `Get` и `Update`. Параллельные `Pause` и `Resume` в цикле, проверка финального `Enabled`. Для ID тест с фиксированными часами и параллельным `Schedule`.

Fix. Перенести выбор в store как одну операцию с предикатом. ID брать из atomic counter или случайных байт. Приоритет средний для lost update, низкий для ID.

## 6. Watch poller

`DBVerdictSource` в `internal/infra/mcp/verdict_source.go:26` одна на процесс. `loop` в `:79` живёт до `stop`. `Stop` в `:74` закрывает `stop` через `Once` и ждёт `done`. Подписчики держатся в карте под `mu`. `subscriptionSnapshot` в `:238` копирует множество под блокировкой. `send` в `:217` шлёт в буферизованный канал и проверяет `done`. Канал `updates` никогда не закрывается, поэтому send на закрытый канал здесь невозможен.

Отдельно проверен источник данных. `LiveWatches` в `internal/infra/store/db.go:398` каждый раз декодирует JSON в новую структуру. Общей карты `Checks` между poller и store нет. Копии в `cloneChecks` в `verdict_source.go:274` закрывают алиасинг между подписчиками.

`watchOutcome` в `internal/infra/store/db.go:1043` создаёт goroutine на каждый вызов. Выход идёт по `ctx.Done` или `closing` в `:1067-1070`. Если вызывающий передаёт `context.Background` и результат не появляется, горoutine живёт до `DB.Close`. Много вызовов с длинным контекстом дают накопление горoutine.

How to prove. Тест на утечку: 1000 вызовов `WatchOutcome` с `Background`, замерить `runtime.NumGoroutine` до и после.

Fix. Требовать ограниченный контекст на границе порта или возвращать функцию отмены вместе с каналом. Приоритет средний.

## 7. Другие места с удержанием мьютекса

`internal/app/tasks/card_monitor.go:155` держит `m.mu` весь `Poll`, включая два внешних вызова `m.port.Card` в `:171` и `m.port.EventsAfter` в `:175`. Пока идёт сетевой вызов, блокируются `Subscribe`, `Unsubscribe`, `Subscribed` и `SubscriptionCount` в `:115-148`. Если порт внутри вызова дотронется до того же `Monitor`, будет deadlock.

How to prove. Тест с портом, чей `Card` вызывает `m.Subscribed()`. Тест повиснет. Второй замер: заблокировать `Card` на канале и измерить, сколько ждёт `Subscribe`.

Fix. Снять snapshot под блокировкой, отпустить её, вызвать порты, потом снова взять и сверить версию состояния. Приоритет средний.

`internal/infra/server/ws.go:301` запускает `readClient`. `serve` выходит по `ctx.Done` или ошибке записи и закрывает соединение в `defer` в `:292`. Настоящий `net.Conn` при закрытии разблокирует `ReadFrame`. Поддельный `frameConn`, который игнорирует `Close`, оставит горoutine висеть.

How to prove. Тест с fake `frameConn`, чей `ReadFrame` блокируется навсегда. Отменить родительский контекст и сверить число горoutine.

Fix. Прописать в контракте `frameConn`, что `Close` должен разблокировать `ReadFrame`, или добавить `done` канал в интерфейс. Приоритет низкий.

## 8. Долгоживущие состояния

`waitStartRegistry` в `internal/infra/mcp/tool_wait_result.go:413` не удаляет записи. Комментарий в `:384` говорит, что индекс живёт всё время сервиса. Каждый уникальный `StartKey` оставляет `*waitStart` и канал. `finish` в `:1089` вызывается после `s.wait` без `defer`. Если `s.wait` запаникует, владелец не опубликует результат. Ждущие в `:1086` останутся на `e.done`, а `await` в `:401` выбирает только `done` и `ctx.Done`. При длинном контексте это навсегда.

How to prove. Тест с 10 тысячами уникальных `StartKey`, сверить размер `s.starts`. Второй тест с источником, который паникует, и ждущим с `Background`.

Fix. Ограничить размер карты по времени или по числу записей. Обернуть ожидание владельца в `defer`, который всегда вызывает `finish`, в том числе при panic. Приоритет средний.

## 9. Проверки

Команды запускались с явными каталогами, без `./...`.

`go build ./cmd/beacon ./internal/app/... ./internal/infra/...` прошёл, код выхода 0.

`go test -race -count=3` по `internal/app/event`, `internal/infra/server/eventbus`, `internal/infra/mcp`, `internal/infra/wake`, `internal/infra/store`, `internal/tui`. Зелёные все, кроме `internal/infra/mcp`. Там падает `TestToolsListStabilityAcrossConnections`: 16153 байт против 16095 в golden, SHA не совпадает. Значение детерминированное на трёх прогонах, сравнение двух соединений в `:280` проходит. Это устаревший golden, не гонка.

`go test -race -count=2` по `internal/infra/cibridge`, `internal/app/orchestrator`, `internal/infra/server`, `internal/infra/httpapi`, `internal/infra/webhook`, `internal/infra/opencode`, `cmd/beacon`. Все зелёные, кроме `TestT3152InstallOpenCodePortsLeavesReaderClientUnwired` в `cmd/beacon/main_test.go:757`. Ошибка детерминированная, порты POST ставятся без T3.150 и T3.151. К конкуренции отношения не имеет.

`beacon-gates layers-ast` и `layers-list` вернули `green: no findings`, код 0. `capabilities` и `reachability` за 9 минут не завершились. Причина в обходе дерева: `capabilities.analyse` в `tools/beacon-gates/capabilities/capabilities.go:52` зовёт `filepath.WalkDir(root)` и не пропускает каталог `worktrees`, в котором лежит много копий репозитория. Вердикт по этим двум гейтам в окне аудита получен не был. Это особенность окружения, не finding по конкуренции в коде beacon.

`git grep -n -E 'make\(chan|close\(|go func\(|sync\.(Mutex|RWMutex|Once|WaitGroup)|atomic\.|context\.WithCancel|ctx\.Done\(\)' --` по девяти явным каталогам дал 518 совпадений. Грубая оценка охвата, не список находок.

## 10. Таблица рисков

| Место | Риск | Proof | Fix | Приоритет |
| --- | --- | --- | --- | --- |
| `internal/infra/webhook/receiver.go:137`, `:255` | detached goroutine переживает `Shutdown`, потеря записи | тест с блокирующим recorder и вызовом `Close` | WaitGroup в `Receiver`, ожидание в `Close` | высокий |
| `internal/infra/store/db.go:236-251` | `Write` возвращается по `ctx.Done` или `closing`, а job всё ещё исполняется | chaos-тест с отменой на занятом writer, сверить ошибки и строки | после enqueue ждать только `job.err` | высокий |
| `internal/app/event/bus.go:216-234` | `commitMu` удерживается на доставке, подписчик с коммитом вешает процесс | тест с подписчиком, зовущим `WithTx`, под таймаутом | отпускать `commitMu` до `drain` | средний |
| `internal/app/tasks/card_monitor.go:155-175` | мьютекс на внешнем I/O, deadlock при реентерабельности | порт зовёт `m.Subscribed()` внутри `Card` | snapshot, release, перепроверка версии | средний |
| `internal/infra/cibridge/bridge.go:405-508` | нет single-flight на `Refresh`, двойные решения над одним снимком | два вызова `Refresh` и подсчёт wake | `refreshMu` или atomic CAS | средний |
| `internal/infra/mcp/tool_wait_result.go:413-433`, `:1084-1089` | карта без выгрузки, `finish` не вызван при panic | 10k ключей и ждущий с `Background` | TTL или LRU, `defer finish` | средний |
| `internal/infra/store/db.go:1043-1073` | goroutine на вызов живёт до `DB.Close` при длинном контексте | замер `NumGoroutine` после 1000 вызовов | ограниченный контекст или `cancel` в результате | средний |
| `internal/infra/server/scheduler.go:147-160`, `:183-185` | lost update в `setEnabled`, коллизия ID | параллельные `Pause`/`Resume`, фиксированные часы | одна операция в store, atomic ID | средний |
| `internal/infra/server/ws.go:291-301` | `readClient` висит, если `frameConn.Close` не будит `ReadFrame` | fake conn с вечным блоком в `ReadFrame` | контракт `Close` или `done` в интерфейсе | низкий |
| `internal/infra/httpapi/server.go:627-643` | `Shutdown` ждёт SSE до таймаута | замер длительности `Close` с открытым SSE | закрыть bus до `Shutdown` | низкий |

## 11. Пять главных находок

1. `internal/infra/webhook/receiver.go:137` отсоединённая goroutine не входит в ожидание `Server.Close`. Это самый вероятный путь потерять запись при остановке процесса.
2. `internal/infra/store/db.go:236-251` результат запроса расходится с исполнением job. Вызов может вернуть ошибку после успешной записи или наоборот.
3. `internal/app/event/bus.go:216-232` держит `commitMu` на доставке. Один подписчик с коммитом внутри останавливает весь процесс.
4. `internal/app/tasks/card_monitor.go:155` держит мьютекс на двух сетевых вызовах. Любая реентерабельность порта даёт deadlock.
5. `internal/infra/cibridge/bridge.go:405` не ограничивает число параллельных `Refresh`. Внутренние мьютексы защищают карты, но не защищают решение целиком.

## Что проверено и не требует правки

`httpapi.EventBus`, `eventbus.Bus`, `verdict_source`, `DeliveryLeaseGuard`, все T211 registry и `wake.Registry` держат блокировки на границах и копируют данные наружу. Каналы в них либо не закрываются, либо закрываются под тем же мьютексом, который используется для send. Send на закрытый канал в этих местах исключён. `db.writeLoop` в `:254` корректно drainит очередь по `closing`. Lifecycle в `internal/infra/server/lifecycle.go:317` ждёт выхода serve-goroutine до публикации `StateStopped`.
