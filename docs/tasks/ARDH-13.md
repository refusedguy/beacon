# ARDH-13. Убрать capability-интерфейсы с type assertion там, где меняется гарантия
Статус: in_progress
Фаза: P2
Тип: task
Владелец файлов: internal/app/orchestrator/orchestrator.go, internal/infra/cibridge/bridge.go, internal/domain/types.go
Зависимости: 
Ссылка: https://github.com/refusedguy/beacon/pull/1174
Агент: нет
Время: нет
Цель: Сделать обязательное поведение частью основного порта, а необязательные режимы передавать явно, убрав скрытые проверки типа и ветки отката.
Контекст: Источник: docs/research/solid-gof-audit.md, оси OCP и LSP. CheckedMerger, ReasonWaker, WatchHolder, liveWatchSource, activitySource, stateMerger и fixForSource меняют значение успешного ответа или множество Watch. WithWaker(w any) молча игнорирует неизвестный тип. Риск: средний. Нужно сохранить различие между trusted Merger и CheckedMerger.
Шаги: Для каждой пары решить, разделяются ли методы по имени или слабый контракт фиксируется в основном порте. Убрать type switch в WithWaker. Обновить сборку в composition root отдельной задачей, если понадобится.
Проверка: CI зелёный. Нет веток, где отсутствие необязательного метода молча уменьшает результат.
Приёмка: Остались только расширения, не меняющие результат. Гарантии MERGED и LiveWatches сохранены.

baseline: docs/research/solid-gof-audit.md, разделы OCP и LSP, дата 2026-11-21. Замер точек: 7 capability-интерфейсов и `WithWaker(any)` с type switch, 9 проверок типа по `internal/`. Способ: `rg` по `internal/`, код не запускался.

## Выполнено в этом PR

- `CheckedMerger`. В `mergeOne` проверка `merger.(CheckedMerger)` заменена явным полем `Options.CheckedMerger`. Различие между trusted `Merger` и checked merge сохранено: без явной передачи работает обычный `Merger`. Рабочие адаптеры не реализуют `MergeChecked`, поэтому поведение проды не меняется.
- `ReasonWaker`. В `wakeOne` проверка `waker.(ReasonWaker)` заменена явным полем `Options.ReasonWaker` и отдельным параметром функции. Рабочий `daemonWaker` не реализует `WakeWithReason`, поведение проды не меняется.
- Обновлены тесты `orchestrator_test.go`. Четыре теста checked merge передают порт явно. Добавлен `TestWakeOneUsesExplicitReasonWaker`. Прямой вызов `wakeOne` получил новый параметр.
- Правка `cibridge` из первой версии PR отпала после того, как ARDH-11 перенёс код capability в `verdict_applier.go`, `ci_refresh.go` и `wake_dispatcher.go`. Конфликт ребейза решён в пользу версии main, поэтому этот PR меняет только оркестратор и карточку.

## TODO с причиной

Оставленные точки требуют правки wiring в `cmd/beacon` и заглушек в чужих пакетах. Это чужие OwnerFiles. Без локального `go test` и `go build` правка таких мест несёт риск сломать фичи, поэтому они вынесены в отдельный проход. После ARDH-11 часть точек переехала в файлы других карточек.

- `WatchHolder` (`orchestrator.go:372`). Добавление `HoldWatch` в `Store` ломает `emptyOrchestratorStore` (`cmd/beacon/mcp_test.go`) и `orchFakeStore` (`cmd/beacon/orchestrate_test.go`). Передача через новое поле `Options` теряет сохранение причины hold в проде: оба рабочих вызова `Run` передают `Options{}` и находятся вне OwnerFiles карточки.
- `liveWatchSource` (`ci_refresh.go:19`). Откат к `AllWatches` может вернуть закрытые Watch. Сделать `LiveWatches` обязательным можно через `domain.Store`, но это ломает пять заглушек в пакетах domain, mcp и wake. Сузить параметр `New` нельзя из-за `wakeBlockedStore` в `internal/infra/wake/notify_test.go`.
- `activitySource` (`wake_dispatcher.go:475`). `WatchObligations` реализует только `store.DB`. Обязательный метод имеет тот же разлёт по пакетам, что и `LiveWatches`.
- `stateMerger` (`verdict_applier.go:248`). В проде порт реализует `github.BatchProvider`. Убрать проверку без явного `WithStateMerger` в `cmd/beacon/mcp.go` значит добавить чтение GraphQL и потерять детекцию сдвинутой базы.
- `WithWaker(w any)` (`bridge.go:314`). Прода передаёт adapter-aware waker, но тесты `cibridge` передают legacy `WakeDeliverer` через `recordingWaker`, `chainWaker` и `w381Waker`. Снятие type switch требует типизированного сеттера, правки этих тестов и правки wiring. Сигнатура входит в публичный набор из 12 сеттеров.
- `fixForSource` (`verdict_applier.go:825`). Реализация есть только в тестовом `fixerStore`. Удаление fallback требует переписать четыре теста в `bridge_test.go`, которые намеренно проверяют старый путь. В `watchFixFor` там остался `reflect.ValueOf(w).FieldByName("FixFor")`, хотя `domain.Watch` уже содержит поле `FixFor`.
- `ReasonWaker` в `internal/app/orchestrator/rebase.go`. Файл не входит в OwnerFiles карточки. Проверка типа там тоже есть, в проде не срабатывает.

## Подтверждение

PR: https://github.com/refusedguy/beacon/pull/1174
Ветка: `fix/ardh13-capabilities`
HeadSHA: см. `git rev-parse HEAD` в PR.

Локально `go test`, `go build` и `beacon gates` не запускались по правилам репозитория. Проверка идёт в CI PR. Карточка остаётся `in_progress`, пока не сняты точки из раздела TODO.
