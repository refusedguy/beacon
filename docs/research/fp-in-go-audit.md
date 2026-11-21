# Аудит функциональных принципов в Go-коде beacon

Дата: 21 ноября 2026. Источник: чтение файлов и `rg` по явным каталогам `internal`, `cmd`, `tools`. Тесты, сборка и gates не запускались. Код не менялся.

## Итог

Функциональное ядро в `internal/domain` уже идиоматично и приносит пользу. Чистые функции, value semantics, `(value, ok)`, early return и функции как порты встроены в сам язык Go. Они здесь работают и их стоит сохранить.

Чужеродны три вещи: обобщённые `Map`/`Filter`/`Reduce` поверх срезов, отдельные типы `Option`/`Maybe` вместо `val, ok`, и цепочки замыканий ради композиции. В репозитории нет ни одного FP-хелпера, ни одного `Option`-типа. Это правильное решение. Добавлять их не нужно.

Главный риск в коде не в FP, а в изменяемых глобальных переменных: function vars в `cmd/beacon` и экспортируемые срезы в `internal/infra/mcp`. Это shared state между вызовами и тестами.

## 1. Чистые функции и функциональное ядро

`Transition` в `internal/domain/transition.go:254` принимает `Watch` и `Event` по значению, копирует состояние через `cloneWatch` и возвращает `(Watch, []Effect)`. Функция не трогает аргумент, не читает часы и не делает I/O. Время приходит в `Event.At`. Побочные действия описаны данными `Effect` и выполняются адаптером. Это чистая модель ядра с внешним императивным слоем. Она позволяет проверять переходы property-based тестами без базы.

Тот же приём в других местах:

- `internal/domain/verdict.go:475` `ShipAllowed` и `verdict.go:507` `GateMetricFlag` - формулы над структурами фактов. Никакой политики снаружи, изменение правила требует правки кода.
- `internal/domain/verdict.go:570` `CompletionGate` и `verdict.go:663` `EvaluateExternal` - чистые гейты с early return. `EvaluateExternal` строит `exitBy` и `seen` локально, возвращает новый `ExternalEvidence`.
- `internal/domain/verdict.go:850` `ParseReviewerExitCode` - чистый парсер текста. Позиции `reviewerTokens` (`:747`), `reviewerApproved` (`:786`), `reviewerChanged` (`:801`), `reviewerBlocked` (`:823`) делят задачу на маленькие функции без состояния.
- `internal/app/orchestrator/workload.go:117` `BuildWorkload` - типичный reduce: один проход по `watches`, три карты-агрегата, возврат нового `Workload`. Вход не меняется.
- `internal/app/orchestrator/checkall.go:433` `VerdictFromChecks` - чистый свод правил по списку проверок.
- `internal/domain/fanout.go:201` `normalizeWatch` и `fanout.go:184` `findByAlias` - чистые функции над копией Watch.

Рекомендация: оставить как есть. Это тот случай, когда FP-стиль совпадает с идиоматичным Go: маленькие функции, явные аргументы, возврат значения, никакой магии.

## 2. Неизменяемость и value semantics

Защита от алиасинга сделана вручную и последовательно:

- `internal/domain/transition.go:459` `cloneWatch` копирует `Checks`, `Subscribers`, `Blocked`, `Flaky.Names` и `Flaky.Used`. Помощники `cloneSubscribers` (`:481`) и `cloneBlockedInfo` (`:490`) копируют вложенные срезы и указатель.
- `internal/domain/transition.go:189` `BlockEvent` принимает `BlockedInfo` по значению и берёт адрес копии, чтобы вызывающий не изменил событие после создания.
- `internal/domain/capabilities.go:45`, `:86`, `:151`, `:214` функции `ValidRoles`, `ValidRuntimes`, `ValidCapabilities`, `ValidScopes` возвращают копии словарей. Пакетные переменные `roleValues` (`:41`) и другие остаются приватными.
- `internal/domain/types.go:794` `WithTaskDescriptor` возвращает новый `context.Context` через `context.WithValue`, не меняя исходный.

Рекомендация: оставить как есть. Это дешёвая и понятная замена неизменяемости, которая не требует лишних типов.

Одно место стоит поправить точечно: `internal/infra/mcp/server.go:34` `LegacyProtocolVersions` и `:106` `ToolNames` - экспортируемые срезы. `server.go:484` кладёт `LegacyProtocolVersions` прямо в ответ `SupportedProtocolVersions`, то есть вызывающий получает ссылку на глобальный срез. Пока это таблица констант, риск невелик, но копия при выдаче или приватная переменная с функцией-геттером убрали бы его полностью. Рекомендация: внедрить.

## 3. Option, Maybe и Result-подобные значения

Репозиторий использует идиому `val, ok`, а не отдельные типы:

- `internal/domain/capabilities.go:33`, `:76`, `:141`, `:205`: `ParseRole`, `ParseRuntime`, `ParseCapability`, `ParseScope` возвращают `(T, bool)`. Неизвестное значение отклоняется, zero value не проходит валидацию.
- `internal/domain/verdict.go:442` `ParseReviewVerdict`, `internal/infra/observe/observe.go:29` `ParseLevel`, `internal/infra/wake/telegram.go:269` `ParseBotCommand` - та же пара.
- `internal/domain/verdict.go:594` `proofForPhase` возвращает `(Proof, bool)`.
- `internal/domain/types.go:800` `TaskDescriptorFrom` возвращает `(TaskDescriptor, bool)` из context.
- `internal/infra/github/errors.go:122` `ParseConflict` возвращает `(*domain.ConflictResult, bool)`.
- `internal/app/orchestrator/checkall.go:754` `FuncRegistry.Find` возвращает `(WatchRef, bool, error)`, совмещая `ok` и ошибку.

Result-подобные значения здесь тоже есть, и они выражены structs или строковыми enum, а не обобщённым `Either`:

- `internal/domain/types.go:24` `Verdict` с набором значений в `:27`-`:38`. Это сумма результатов, на которой ветвится домен.
- `internal/domain/verdict.go:557` `CompletionResult` с `Outcome`, `Reason` и `PhaseID`. Причина заполняется только при `REWORK`.
- `internal/app/command/processor.go:42` `Result` с флагами `Created` и `Replayed`.
- `internal/app/orchestrator/checkall.go:433` `Verdict` как результат классификации проверок.

Рекомендация: оставить как есть. Вводить `Option[T]` или `Result[T, E]` здесь было бы лишним boilerplate: пришлось бы писать конструкторы, методы `IsSome`/`Unwrap`, адаптеры к уже существующему `(val, ok)` и объяснять это команде. Go-компилятор и так заставляет обработать оба возвращаемых значения.

## 4. Функции как значения и замыкания

Идиоматичные случаи, которые стоит сохранить:

- `internal/infra/ladder/transitions.go:182` `type Option func(*Ladder)` и `:186` `WithJitter`. Классический functional options. `New` в `:203` применяет опции к новому объекту.
- `internal/infra/otel/local.go:73` `type Option func(*Sink)`, тот же приём.
- `internal/infra/server/http/auth.go:17` `Bearer` и `internal/infra/server/http.go:131` `Bearer`. Middleware как функция от handler к handler, стандарт net/http.
- `internal/app/event/bus.go:99` `Subscribe` возвращает функцию отписки с `sync.Once`. Это идиома для cleanup.
- `internal/infra/observe/otel.go:688` `SetTracer` возвращает функцию восстановления предыдущего tracera.
- `internal/app/orchestrator/checkall.go:753` `FuncRegistry` и `:769` `FuncWriter` адаптируют замыкания к интерфейсам. Это нормальный Go-приём для подмены порта в тесте.
- `internal/infra/github/errors.go:275` `nowOr` и `internal/infra/github/graphql.go:471` `sleepOr`. Замыкания подменяют часы и ожидание в тестах. Один метод вместо поля `func` с nil-проверкой на каждом вызове.
- `cmd/beacon/observe.go:94` `startMetricsEndpoint` возвращает `noop` или функцию остановки сервера. Вызывающий всегда делает `defer stop()` без проверки на nil.

Пограничные случаи, где композиция сложнее задачи:

- `cmd/beacon/watch_test.go:337` `t3114AlivePID` - функция, возвращающая константную функцию `func(pid int) bool { return pid == 222 }`. Прямое присваивание замыкания в `gcProcessAlive` на `:343` читалось бы проще, чем лишний уровень косвенности. Рекомендация: убрать обёртку при ближайшей правке этого теста.
- `internal/app/taskcli/service.go:1006`-`:1043` несколько вызовов `check(..., func() bool {...}())`. Немедленно вызванные замыкания нужны, чтобы каждый сценарий менял opts и не делил состояние с соседним. Читается тяжело, чем маленький helper с аргументами. Рекомендация: оставить как есть, это изолированные тестовые кейсы, но не копировать стиль в новый код.
- `internal/infra/mcp/schema.go:295` `outputSchemaFactories` - map из замыканий, каждое вызывает `OutputSchema[WatchPROutput]()` и так далее. Go не позволяет хранить инстанцированную дженерик-функцию как значение без обёртки, поэтому это не FP ради FP, а обход ограничения языка. Рекомендация: оставить как есть.

Отдельно про map/filter/reduce цепочки. Их в репозитории нет, и это хорошо:

- `internal/infra/github/points.go:701`-`:711` фильтрует события прямо в цикле и строит `out`.
- `internal/app/orchestrator/workload.go:123`-`:157` считает агрегаты одним проходом.
- `internal/infra/observe/otel.go:661` `appendProtoFixed64` принимает `dst []byte` и возвращает расширенный срез. Это каноничный Go-подход вместо concatenation.

Дженерики используются точечно и по делу: `internal/app/orchestrator/store.go:198` `setWatchVerdict[T ~string]`, `internal/infra/mcp/schema.go:289` `OutputSchema[T any]`, `internal/infra/opencode/client.go:259` `decodeList[T any]`. Рекомендация: оставить как есть, не строить универсальную библиотеку `Map/Filter/Reduce`.

## 5. Мутация и shared state

Изменяемые глобальные function vars в `cmd/beacon`. Это самая заметная группа: `watchSnapshot` (`cmd/beacon/watch.go:29`), `watchRunner` (`:41`), `watchLiveRunner` (`:500`), `deliverSnapshot` (`cmd/beacon/deliver.go:62`), `deliverRunner` (`:73`), `deliverSleep` (`:217`), `metricsServerStart` (`cmd/beacon/observe.go:83`), `metricsRunner` (`:113`), `mcpRunner` (`cmd/beacon/mcp.go:324`), `serveMCP` (`:373`). Тесты подменяют их через `t.Cleanup`, например `cmd/beacon/deliver_test.go:81` и `cmd/beacon/watch_test.go:962`. Пока команды запускаются по одной и тесты не параллелят подмену, работает. При параллельных тестах или двух командах в одном процессе фактически default будет общим. Рекомендация: оставить как есть в CLI, но не расширять этот приём. Для новых портов передавать функцию в конструктор, как это делает `internal/app/event/bus.go:93` `NewBus(journal)`.

Глобальные значения с мьютексом. `internal/infra/observe/observe.go:154` `defaultSink` защищён `defaultMu`, `SetDefault` в `:168` меняет его под блокировкой. `internal/infra/observe/otel.go:668` `current` защищён `currentMu`, `Current` в `:679` читает под RLock. `cmd/beacon/mcp.go:728` `cycleInfo` защищён `cycleMu`. Все три легитимные процессные singleton-объекты с корректной синхронизацией. Заменить их возвратом нового значения нельзя: их читают вызовы по всему процессу. Рекомендация: оставить как есть.

Состояние в домене. `internal/domain/fanout.go:42` держит `sync.Mutex`, `:81` блокирует `Subscribe`, чтобы сериализовать check-then-act для лимитов. В `:111` и `:112` написано прямое правило: работать с копией сохранённого Watch, чтобы не алиасить значение вызывающего. `findByAlias` в `:184` возвращает `*Watch` внутрь полученного из store среза, но вызывающий копирует его перед мутацией. Риск закрыт, но сам указатель остаётся лёгким источником ошибок при будущих правках. Рекомендация: оставить как есть, mutex здесь оправдан и объяснён комментариями.

Горутины. Поиск нашёл много `go func()` и мьютексов в `internal/infra/mcp`, `internal/infra/wake`, `internal/tui`, `internal/infra/server`. В production-коде они почти всегда сопровождаются мьютексом для разделяемой карты или slices, например `internal/infra/mcp/tool_wake.go:1186`, `internal/infra/wake/deliver.go:1004`, `internal/tui/socket.go:186`. Это императивная часть системы, и попытка переписать её как чистое преобразование добавила бы копирований без выигрыша. Рекомендация: оставить как есть.

## Рекомендации по действиям

Внедрить:

1. Вернуть копию или убрать экспорт у `internal/infra/mcp/server.go:34` `LegacyProtocolVersions` и `:106` `ToolNames`, чтобы ответ API не держал ссылку на глобальный срез.
2. Не добавлять новые package-level function vars в `cmd/beacon`. Передавать замыкание в конструктор, как в `internal/app/event/bus.go:93`.

Оставить как есть:

1. Чистые функции `Transition`, `ShipAllowed`, `CompletionGate`, `EvaluateExternal`, `BuildWorkload`, `VerdictFromChecks`.
2. Ручные копии в `cloneWatch` и функциях `Valid*`.
3. `(value, bool)` вместо `Option[T]`, structs результатов вместо `Either`.
4. Functional options, middleware, function ports, cleanup-функции.
5. Циклы вместо `Map`/`Filter`/`Reduce`.

Убрать:

1. `t3114AlivePID` в `cmd/beacon/watch_test.go:337`, заменить прямым замыканием на месте присваивания.
2. Ничего больше. Массовое FP-рефакторирование этому репозиторию не нужно.

## Пять главных находок

1. `internal/domain/transition.go:254` - чистая машина состояний возвращает новое значение и `Effect`, не меняет аргумент. Это образец для всего репозитория.
2. `internal/domain/capabilities.go:33`-`:214` и `internal/domain/types.go:800` - `(value, ok)` и копии словарей закрывают задачу Option без лишнего типа.
3. `internal/app/orchestrator/workload.go:117` и `internal/infra/github/points.go:701` - reduce и filter написаны циклами. Так и надо, дженерик-хелперы здесь не помогли бы.
4. `cmd/beacon/watch_test.go:337` - единственный по-настоящему избыточный слой замыканий. Функция возвращает функцию-константу.
5. `cmd/beacon/watch.go:29`-`:45`, `cmd/beacon/mcp.go:324` и `internal/infra/mcp/server.go:34` - изменяемые глобалы и экспортируемый срез. Это реальный shared state, а не FP-проблема.
