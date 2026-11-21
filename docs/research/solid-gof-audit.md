# Аудит SOLID и GOF в beacon-impl

Дата: 2026-11-21. Режим: чтение, без запуска тестов, сборок и gate. Источник: `internal/`, `cmd/`, `evals/main.go` без каталога `worktrees/`.

Метод: ручное чтение крупных файлов, перечисление интерфейсов скриптом по `type X interface {`, подсчёт методов, проверка точек использования через rg. Всего найдено 194 интерфейса, из них около 130 с одним или двумя методами.

## Пять главных находок

1. `internal/infra/cibridge/bridge.go` (2158 строк) и `internal/app/taskcli/service.go` (2669 строк) боги-объекты. Bridge держит 15 полей и 12 необязательных сеттеров, совмещает опрос CI, маршрутизацию вердиктов, мерж, блокировки, доставку wake с circuit breaker, эскалацию и API чтения. Service несёт восемь независимых файловых конечных автоматов с повторяющимся квартетом `path/lock/load/save`.
2. Расширяемость построена на необязательных интерфейсах через type assertion: `CheckedMerger`, `ReasonWaker`, `WatchHolder`, `liveWatchSource`, `activitySource`, `stateMerger`, `fixForSource`. Каждый новый режим добавляет интерфейс, проверку типа и ветку отката в ядре. Это OCP через условия, а не через полиморфизм.
3. Три реализации event bus: `internal/app/event/bus.go`, `internal/infra/server/eventbus/bus.go` и `internal/infra/httpapi/server.go:266`. Первые две почти повторяют контракт «stage, commit, deliver, изоляция подписчика».
4. Мёртвые абстракции: `domain.Notifier` используется только в своём тесте, `domain.ControlPlaneStore` это alias без использований, `mcp.Bridge` с шестью методами подключён только в тесте `tool_watch_pr_test.go:702`. АDR 0086/0094 запрещают второй протокол, поэтому точка расширения не нужна.
5. DIP нарушен в шести местах: слой `app` импортирует конкретные пакеты `infra` в продуктивном коде (`app/orchestrator` это `observe`, `wake`, `store`; `app/session` это `store`; `app/command` это `channel`; `app/taskcli` это `taskstore`).

## Ось SRP

### Находки

`internal/infra/cibridge/bridge.go:149` объявляет структуру `Bridge` с 15 полями. По файлу можно насчитать минимум восемь обязанностей:

- опрос CI и нормализация снимков, `fetchSnapshots` (452) и `Refresh` (508);
- маршрутизация вердикта, `applyVerdict` (739) и `publishActionable` (778);
- автомерж и ребейз, `applyMerge` (860), `callMerge` (840), `claimRebase` (332);
- работа с блокировками и инцидентами, `applyBlocked` (1092), `handleBlocked` (1280), `unblockFixed` (1965);
- доставка wake и per-adapter circuit breaker, `wakeOne` (1509), `breakerPermit` (1426), `breakerRecord` (1451), константы 1372–1378;
- эскалация сирот и зависших in_work подписчиков, `markOrphaned` (1648), `markInWorkStalled` (1772);
- запись в `ESCALATIONS.md`, `appendEscalation` (1625) с прямым `os.ReadFile` и `os.OpenFile`;
- API чтения для инструментов, `RegisterSelfWatch` (2045), `ReadVerdict` (2084), `ConfirmRead` (2125).

Дополнительно 12 методов `With*` (179, 198, 208, 220, 231, 271, 285, 296, 307, 319, 355, 365) превращают конструктор в строитель с необязательной конфигурацией.

`internal/app/taskcli/service.go` содержит 68 методов в одном файле и восемь файловых state machine: история воркеров (1243–1354), CI healing (1438–1555), резервирование файлов (1657–1930), precheck (1965–2040), журнал handoff (2080–2200), fix-checks (2276–2500), подтверждения handoff (2558–2662), rate limit (637). У каждого свой путь, своя блокировка и свои функции `load`/`save`. Это не одна ответственность, а восемь репозиториев, приклеенных к сервису.

`internal/infra/httpapi/server.go:448` держит listener, токен, SSE шину, метрики, аудит и карту решений. Для HTTP адаптера это терпимо, но аудит и метрики логично вынести в отдельные типы.

### Рекомендация

Упростить. Разделить `Bridge` минимум на `CIRefresh`, `VerdictApplier`, `WakeDispatcher` и `ReadAPI`, оставив тонкий координатор. Разнести `taskcli.Service` по файловым репозиториям, оставив в сервисе только правила. Сделать это точечно, по одному выносу за раз, с проверкой после каждого шага. `httpapi` оставить как есть на этом этапе.

## Ось OCP

### Находки

Хорошая сторона: точка входа MCP открыта для расширения через порты. `mcp.NewServerWithDeps` (server.go:479) собирает восемь реальных обработчиков из `Dependencies` (253), регистрация идёт циклом по `ToolNames` (487). Добавление инструмента не меняет домен.

Плохая сторона: новые режимы вводятся не через замену реализации, а через дополнительный интерфейс и type assertion в ядре.

- `orchestrator.go:220 CheckedMerger` проверяется в `mergeOne` (464), иначе используется `Merger` (468).
- `orchestrator.go:254 ReasonWaker` проверяется в `wakeOne` (573), иначе обычный `Waker` (576).
- `orchestrator.go:239 WatchHolder` проверяется в `RunWithResolver` (364).
- `bridge.go:104 liveWatchSource`, `111 activitySource`, `134 stateMerger`, `1944 fixForSource` проверяются в 435, 1727, 845 и 1956 соответственно.
- `bridge.go:271 WithWaker(w any)` делает type switch по двум интерфейсам и молча игнорирует неизвестный тип.

Каждый такой случай это закрытие для изменения без изменения ядра, но ценой скрытой проверки типа и ветки отката. Поведение зависит от того, что реализация случайно реализует, а не от явной конфигурации.

### Рекомендация

Упростить. Где поведение обязательно для продакшена, сделать его частью основного порта и убрать type assertion. Где режим действительно необязателен, передавать его явно как функцию или отдельный объект в конструкторе. Оставить расширения только там, где есть реальный второй держатель, отсутствующий в репозитории.

## Ось LSP

### Находки

Необязательные варианты портов меняют гарантии, сохраняя имя метода.

- `domain.Merger` (types.go:468) комментарий в `orchestrator.go:424` явно говорит, что простой `Merger` «trusted on its success return», а `CheckedMerger` подтверждает состояние MERGED. Подстановка одной реализации вместо другой меняет значение успешного ответа.
- `domain.WakeDeliverer` (types.go:403) и `domain.AdapterWakeDeliverer` (411) оба называют метод `Deliver`, но с разной сигнатурой. `WithWaker(any)` в bridge.go:271 выбирает между ними по типу.
- `domain.Store` (types.go:421) и `liveWatchSource` (bridge.go:104): откат к `AllWatches` (438) может вернуть закрытые Watch, чего нет в `LiveWatches`. Подписчик получает другое множество.
- `stateMerger` (bridge.go:134) повторяет `MergePRWithState` и меняет число GraphQL чтений. Для вызывающего это невидимо.

Формально подстановка возможна, но контракт каждого порта неполон. Если реализация не реализует необязательный метод, ядро молча делает меньше.

### Рекомендация

Упростить. Убрать пары «базовый порт плюс расширение» там, где замена меняет гарантии. Либо разделить на два явно разных метода с разными именами, либо зафиксировать слабый контракт в основном порте и не обещать больше. Оставить расширения только для чистых оптимизаций, которые не меняют результат, например `PointStats`.

## Ось ISP

### Находки

Жирные интерфейсы:

- `domain.Store` (types.go:421–429), 7 методов: три для Watch, три для Obligation, плюс `PendingObligations`. Вынуждает adapters реализовывать всё.
- `domain.BlockerStore` (types.go:476–485), 8 методов: инциденты и привязки оркестратора в одном порте.
- `mcp.Bridge` (server.go:126–133), 6 методов, и он не подключён в продакшене.
- `mcp.Dependencies` (server.go:253–319), около 20 полей. Каждый инструмент использует своё подмножество, но конфигурация одна для всех.
- `blockerControl` в bridge.go:118, 5 методов: создание инцидента, две резолюции, список, отметка доставки.

При этом ISP в малом соблюдается хорошо. Большинство портов узкие и лежат рядом с потребителем: `orchestrator/store.go:35 WatchReader`, `mcp/tool_list_running.go:187 SnapshotReader`, `app/command/processor.go:31 Outbox`, `app/event/bus.go:35 Journal`, `app/tasks/night_shift.go:135–156` четыре порта по одному-два метода.

### Рекомендация

Оставить узкие порты, упростить жирные. Разделить `domain.Store` на `WatchStore` и `ObligationStore`, собирать их в один агрегат только на уровне composition root. Разделить `BlockerStore` на порт инцидентов и порт привязок. Удалить `mcp.Bridge`, если второй агент не появится. `mcp.Dependencies` оставить как таблицу конфигурации, но разбить на подструктуры по инструментам, если список полей продолжит расти.

## Ось DIP

### Находки

Правильная сторона: домен объявляет порты, инфраструктура их реализует. `domain.WakeDeliverer`, `domain.Clock`, `domain.Store`, `domain.CIProvider`, `domain.Merger` живут в `internal/domain`. В `infra/wake/registry.go:63` стоит явная проверка `var _ domain.AdapterWakeDeliverer = (*Registry)(nil)`. Пакет mcp не импортирует `internal/infra/store`, вместо этого `BlockerControl` (server.go:173) и комментарий ADR 0005.

Нарушения в продуктивном коде слоя app:

- `app/orchestrator/orchestrator.go:18` импортирует `internal/infra/observe`;
- `app/orchestrator/daemon.go:18–19` импортирует `observe` и `wake`;
- `app/orchestrator/store.go:16` импортирует `internal/infra/store`;
- `app/session/registry.go:12` импортирует `internal/infra/store`;
- `app/command/processor.go:14` импортирует `internal/infra/channel`, чтобы реализовать `channel.CommandSink`;
- `app/taskcli/service.go:22` импортирует `internal/infra/taskstore`.

Политика верхнего уровня зависит от конкретных адаптеров. Особенно заметен `app/command`: файл объявляет порт `Outbox` и одновременно несёт адаптер к `channel`, смешивая абстракцию и конкретный протокол.

### Рекомендация

Упростить. Перенести метрики в `app` за счёт собственного узкого порта или оставить `observe` как исключение с пояснением. Убрать импорты `infra/store`, `infra/taskstore`, `infra/channel` из `app`, определив нужные интерфейсы в пакете потребителя. Адаптеры собрать в `cmd/beacon`, где уже живёт большая часть wiring.

## GOF-паттерны: что реально работает

### Adapter

Реальный и нужный. `wake/acp.go:50` `ACPAdapter` реализует `domain.WakeDeliverer` поверх ACP, `ACPStdioTransport` и `ACPHTTPTransport` (121, 181) изолируют транспорт. `app/command/processor.go:106 ChannelSink` адаптирует команду канала к обработчику. `cmd/beacon/mcp.go` содержит набор маленьких адаптеров: `httpStateProvider` (540), `humanDecisionSink` (611), `webhookDeliveryRecorder` (787), `ladderStamper` (836), `bridgeLadder` (894), `ladderPointerSender` (1239). Поведение адаптеров проверяется тестами, слой не знает конкретный протокол. Оставить.

### Strategy

`ACPDeliveryPolicy.Plan` (acp.go:355–377) чистая модель: политики и capabilities на входе, план на выходе. `domain.EvaluatePR`, `domain.Transition`, `domain.Decide` также чисто выбирают правило. Хороший пример. Оставить.

### Observer / event bus

Паттерн используется, но в трёх экземплярах.

- `internal/app/event/bus.go` Journal, Tx, Envelope с seq и poison, post-commit доставка.
- `internal/infra/server/eventbus/bus.go` Port, Publish, Commit; Envelope только Type и Payload.
- `internal/infra/httpapi/server.go:266 EventBus` Publish, Subscribe, Since, Last для SSE.

Первые два почти совпадают по смыслу. Третий решает другую задачу, но мог бы подписываться на один из первых. Упростить: оставить один event bus в `app/event`, а SSE шину сделать подписчиком.

### Command

`app/command/processor.go` записывает намерение и outbox одной транзакцией, затем запускает эффект, ключ идемпотентности в `Effect.Key`. Паттерн оправдан. Оставить, но убрать зависимость от `infra/channel` внутри файла.

### Factory

Конструкторы и функциональные опции идиоматичны: `NewServerWithDeps`, `NewWorkerRuntime` (acp.go:477), `BatchOption` (github/batch_provider.go:66), `Option` в `ladder/transitions.go:182` и `otel/local.go:73`. В `cmd/beacon` есть шесть фабрик зависимостей (`workload.go:29`, `orchestrator.go:42` и `198`, `inspect.go:75`, `deferred.go:27`, `orchestrate.go:43`, `prcreate.go:246`). Они нужны, чтобы открывать store на каждый вызов CLI. Оставить, не раздувать.

### Repository

`domain.Store` и `infra/store/db.go` (2687 строк) классический репозиторий. `infra/taskstore/store.go` (842) второй. Оба нужны. Упростить только сигнатуру `domain.Store`, как описано в ISP.

## GOF-паттерны: лишние слои

- `domain.ControlPlaneStore = BlockerStore` (types.go:487–490). Комментарий объясняет, что это «alternate name» для тех, кто мыслит категориями control plane. Использований нет. Удалить.
- `domain.Notifier` (types.go:493–495). Единственные ссылки вне объявления это `types_test.go:42, 175–178`. В продакшене порт не востребован. Удалить или подключить.
- `mcp.Bridge` (server.go:117–133) и `WithBridges` (tool_watch_pr.go:335). Шесть операций Start/Status/Events/Wake/Cancel/Artifacts, цикл рассылки в tool_watch_pr.go:725. В продуктивном коде `WithBridges` не вызывается, только в `tool_watch_pr_test.go:702`. Две записи в ADR 0086/0094 запрещают второй протокол, так что расширение не имеет держателя. Удалить вместе с тестом.
- Необязательные capability-интерфейсы с type assertion, перечисленные в OCP. Упростить.
- `WakeSender` и `FallbackWakeSender` (mcp/tool_wake.go:473, 482) один метод с почти той же сигнатурой, разница в дополнительном `WakeChannel`. Один порт с явным параметром канала проще.
- `Bridge.watchFixFor` (cibridge/bridge.go:1949–1961) читает поле `FixFor` через `reflect.ValueOf(w).FieldByName("FixFor")`, хотя `domain.Watch` уже содержит это поле и код сам присваивает его в 713. Заменить на прямое обращение `w.FixFor`.

## Где Go идиоматичнее без OOP

В репозитории уже есть правильные примеры. `cibridge.OrchestratorIDSource` (bridge.go:99) и `escalationWriter` (128) это именованные функции. `app/event.Subscriber` (bus.go:41), `app/command.Sink` (processor.go:38), `mcp.SnapshotFunc` (tool_list_running.go:194), `SharedCIWaiter` (tool_wait_result.go:846), `wake.PromptFunc` и `wake.SessionActiveFunc` (deliver.go:36, 40) тоже функции. Этот путь дешевле интерфейса, когда реализация одна и не требует имени.

Кандидаты на упрощение до функции:

- `app/session/delegation.go:220 Notifier` вместе с `NotifierFunc` (224). Порт существует только чтобы адаптировать функцию. Передавать `func(context.Context, ParentNotice) error`.
- `infra/server/ws.go:61 ParentNotifier` вместе с `ParentNotifierFunc` (66). Та же причина.
- `infra/wake/acp.go:46 ACPTransport` с единственным методом `Call` и двумя реализациями. Здесь интерфейс оправдан, оставить.
- `domain.Clock` (types.go:416). Один метод `Now`, много реализаций только в тестах. Можно заменить на `func() time.Time` и сэкономить адаптер в cmd/beacon, например `ladderWallClock` (mcp.go:828).

Интерфейсы с одним методом, определённые не там где используются:

- `mcp.Bridge` объявлен в `infra/mcp/server.go:126`, единственный потребитель `WatchPRHandler` в `infra/mcp/tool_watch_pr.go:284`, и то только в тесте. Явный случай «не там и не используется».
- `domain.Notifier` (types.go:493) не имеет продуктивного потребителя.
- `domain.Clock`, `domain.CIProvider` (types.go:432), `domain.Merger` (468) живут в общем ядре и импортируются многими пакетами. Это общая лексика, оставить.
- `infra/cibridge` объявляет свои порты (`ReadStamper` 78, `WakeLadder` 91, `Rebaser` 142) рядом с вызовами. Пример правильного размещения, оставить.

## Boilerplate: границы против бизнес-логики

### Что сосредоточено на границах, оставить

Обработка ошибок вынесена на границу MCP. Домен возвращает `AppError` (types.go:511) и почти не проверяет ошибки: в `internal/domain` всего 5 вхождений `if err != nil` в нетестовых файлах. Обработчики инструментов возвращают доменные ошибки как есть, комментарий в `tool_get_result.go:342`, а преобразование делает одна функция `mcp.ToErrorShape` (schema.go:388). HTTP адаптер проверяет вход в `httpapi.Start` (server.go:468–476). Это правильная концентрация.

Адаптация к go-sdk ограничена: четыре обработчика с сигнатурой `Handle(ctx, *CallToolRequest, In)` в `mcp/tool_get_result.go:343`, `tool_watch_pr.go:346`, `tool_ack.go:466`, `tool_wait_result.go:1369`, плюс таблица регистрации в `server.go:509–525`. Повтор коренится в типе SDK. Оставить.

### Что размазано, упростить

`app/taskcli/service.go`: 75 проверок `if err != nil` и 38 вызовов `strings.TrimSpace` в нетестовом коде. Валидация, чтение и запись JSON файлов, блокировки и бизнес-правила перемешаны внутри методов сервиса. Это следствие восьми state machine в одном типе. Вынос репозиториев уберёт большую часть повтора.

`infra/store/db.go` содержит 321 проверку ошибки. Для SQL адаптера это норма, граница там и есть. Оставить.

`cmd/beacon/mcp.go`: около десяти adapter-типов, каждый реализует один метод порта. Это цена 194 интерфейсов. Часть можно убрать, заменив порты функциями, как описано выше.

## Итоговая таблица решений

| Область | Решение |
| --- | --- |
| `cibridge.Bridge`, `taskcli.Service` | Разделить, по одному выносу за шаг |
| Capability-интерфейсы с type assertion | Убрать там, где меняют гарантии |
| `domain.Notifier`, `ControlPlaneStore`, `mcp.Bridge` | Удалить |
| Три event bus | Свести к одному плюс SSE подписчик |
| Жирные `domain.Store` и `BlockerStore` | Разделить по сущностям |
| Порты в `app` с одним методом | Передать функцию, где нет второй реализации |
| Импорты `infra` из `app` | Перенести адаптеры в `cmd/beacon` |
| Валидация и ошибки на границах | Оставить |
| Валидация внутри `taskcli.Service` | Вынести вместе с репозиториями |
| Адаптеры go-sdk и конструкторы | Оставить |
