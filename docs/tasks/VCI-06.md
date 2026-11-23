# VCI-06. Снять мьютекс с доставки wake и ограничить параллелизм

Premise: проверено 2026-11-23 по свежему `origin/main` `e589b3e49f292b7d6b8153bf3982b03db9a45830`. Реализация из PR 1565 и PR 1988 уже влита. Для обоих merge-коммитов `git merge-base --is-ancestor <SHA> origin/main` вернул 0. Код и тесты на месте, дубль писать нельзя. Карточка закрывается с доказательством, без правки OwnerFiles.

Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/app/orchestrator/daemon.go, internal/infra/cibridge/wake_dispatcher.go, internal/app/tasks/night_shift.go, internal/domain/types.go
Зависимости: CR-01, SL-01, SL-02
Шаги: Сверить свежий origin/main. Проверить merge-коммиты через git merge-base --is-ancestor. Прочитать код и тесты на HEAD. Закрыть карточку с доказательством в отдельном docs PR. Зарегистрировать Watch с автослиянием.
Ссылка: https://github.com/refusedguy/beacon/pull/1565, https://github.com/refusedguy/beacon/pull/1988; бэклог `/tmp/opencode/backlog/04-verdict-ci.md`; границы `/tmp/opencode/backlog/17-concurrency.md`
Агент: vci06-executor
Время: 2026-11-23T06:32:55Z

Цель: один зависший промпт не блокирует доставку wake остальным Watch. Параллелизм имеет верхнюю границу с одним источником порога.

Контекст: `daemonWaker.Wake` держал `w.mu` на внешней доставке через `route` и `backend.Deliver`. Один медленный промпт блокировал все Watch. Параллельным wake и делегированиям не было верхней границы, а ошибка доставки не возвращалась вызывающему.

Проверка: `gofmt` не применялся, ни один `.go`-файл не менялся. Локальные `go test`, `go build` и гейты не запускались по правилам AGENTS.md. Тесты уже выполняются в CI на `origin/main`.

Приёмка: доставка идёт без удержания мьютекса, повтор с тем же obligation не даёт второй эффект, число воркеров ограничено и ожидается, ошибка доставки возвращается. Тесты на HEAD подтверждают поведение. Дедлоков нет, `gofmt` чистый.

Причина: already_landed
Закрыл: vci06-executor
Время закрытия: 2026-11-23T06:32:55Z

## Подтверждение

Два merge-коммита в `origin/main`, проверка предка вернула 0 для каждого:

- PR #1565, merge SHA `e7c006a4121184ac3bc0b5f38f33dffb6aa76050`, `git merge-base --is-ancestor` exit 0.
- PR #1988, merge SHA `bd9c5157060cb91a3f1ae233256c546dacd20b3c`, `git merge-base --is-ancestor` exit 0.

Код на HEAD:

- `internal/app/orchestrator/daemon.go:490-515` резервирует ключ obligation в `inFlight` под `w.mu` и отпускает лок до доставки. `:519-532` зовёт `route` или `backend.Deliver` без лока. `:534-542` под новым локом чистит `inFlight`, пишет `deliveredKeys` и вызывает `dedup.Claim` только при успехе. `:543` возвращает `deliverErr` вызывающему.
- `internal/infra/cibridge/wake_dispatcher.go:21` `DefaultParallelLimit = domain.DefaultSlotsTotal`. `:46` `BoundedRun` создаёт ровно `limit` воркеров, `:64` подставляет порог по умолчанию, `:102` `wg.Wait()` ждёт все начатые воркеры. `:508-512` `DeliverWakes` вызывает `BoundedRun`. `:381` `wakeOne` возвращает ошибку доставки.
- `internal/app/tasks/night_shift.go:338` делегирование идёт через тот же `cibridge.BoundedRun`, второй пул не создаётся.
- `internal/domain/orchestrator.go:17` `DefaultSlotsTotal = 4`. На текущем HEAD константа лежит в `orchestrator.go`, а не в `types.go` из списка OwnerFiles. Единственный источник порога сохранён.

Границы CN-06 не нарушены: semaphore GitHub равен 2, token bucket даёт 2 запроса в секунду с burst 4, бюджет слотов равен 4. Новые горутины не появляются в пяти защищённых зонах: writer SQLite, `admissionLock`, generation и `MintID`, побайтовые гейты, файловый замок карточек. Wake-dedup VCI-02, outbox, receipt и порядок обязательных проверок остаются без изменений, потому что production-код не трогается.

Тесты на HEAD:

- `TestDaemonWakerSlowRouteDoesNotBlockAnotherWatch` в `internal/app/orchestrator/daemon_test.go:1295`: медленный route не блокирует другой Watch.
- `TestDaemonWakerConcurrentSameObligationDeliveredOnce` там же на `:1341`: параллельные вызовы с одним obligation дают одну доставку.
- `TestDaemonWakerWithRouteErrorStaysRetryable` там же на `:1272`: ошибка route возвращается и остаётся retryable.
- `TestDaemonWakerDeliversSameObligationOnce` там же на `:792`: повтор с тем же obligation не создаёт второй эффект.

Конфликты по OwnerFiles: открытые PR #2248, #2176, #1887 и #1880 меняют `internal/domain/types.go`, но не меняют доставку wake. Открытых PR с правками `daemon.go`, `wake_dispatcher.go` и `night_shift.go` нет. Ветка `fix/vci01` содержит только закрывающую карточку VCI-01. Правка этого PR ограничена `docs/tasks/VCI-06.md`, поэтому чужие файлы не затронуты.
