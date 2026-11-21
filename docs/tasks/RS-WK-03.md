# RS-WK-03. Восстановление dedupe индекса из журнала
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/app/worker/effect.go, internal/app/worker/effect_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Перестраивать множества delivery ID и head SHA из webhook_events на старте, чтобы рестарт не терял вторую ступень дедупликации.
Контекст: Premise: проверено 2026-11-21, журнал webhook_events durable, но dedupe index в filters.go живёт в памяти. baseline: `grep -rn 'maxDedup\|seenDelivery' internal/infra/webhook/filters.go internal/tui/socket.go` показывает in-memory множества. Источник: transactional outbox и SSE Last-Event-ID.
baseline: 2026-11-21 по origin/main d3b1bfb. `grep -c 'deliveries\|headSHAs' internal/infra/webhook/filters.go` = 14, `grep -c 'seenDelivery' internal/infra/webhook/filters.go` = 4. `grep -c '^func (d \*DB)' internal/infra/store/webhook_events.go` = 3, это RecordDelivery, DeliverySeen и WebhookDeliveryCount. Постраничного читателя журнала нет: `grep -rni 'DeliveriesAfter\|ListDelivery\|RangeDelivery\|ForEachDelivery\|DeliveryCursor' internal --include=*.go | wc -l` = 0. Вызовов `webhook.NewFilter` и `worker.New` вне тестов нет, определены только сами функции.
Шаги: Добавить шаг восстановления в старт воркера. Читать журнал порциями, не держать всё в памяти. Повтор остаётся no-op за счёт уникального ключа. Тест на рестарт после применённой доставки.
Проверка: gofmt чистый. Тесты в CI. Слои соблюдены: app может читать store.
Приёмка: После рестарта повторная доставка с тем же ID не создаёт второй эффект.

## Блокер

- 2026-11-21. Честное восстановление невозможно внутри OwnerFiles карточки.

  Индекс из карточки живёт в `internal/infra/webhook/filters.go:49-51`. Поля `deliveries` и `headSHAs` наполняет `Filter.Filter` в строках 98-107. Этот файл не входит в OwnerFiles `internal/app/worker/effect.go` и `internal/app/worker/effect_test.go`.

  Мой `internal/app/worker/effect.go:68` хранит другое множество `executed`. Его ключ это ID эффекта из outbox, а не delivery ID и не head SHA. Документация в строках 23-31 говорит, что рестарт теряет этот ledger и что идемпотентность лежит на runner. Заполнение `executed` из `webhook_events` не восстановило бы индекс фильтра и смешало бы ключи доставок с ключами эффектов.

  В `internal/infra/store/webhook_events.go` три метода: `RecordDelivery` (97), `DeliverySeen` (157), `WebhookDeliveryCount` (177). Постраничного чтения нет. Читатель журнала появится только в OwnerFiles store.

  AGENTS.md запрещает infra импортировать соседнюю infra. Поэтому `internal/infra/webhook` не может читать `internal/infra/store` напрямую. Адаптер чтения нужно положить в `internal/app` или `cmd/beacon`.

  Вызова восстановления на старте нет, и вставить его некуда внутри OwnerFiles. `webhook.NewFilter` вне тестов не вызывается, `worker.New` и `Drain` вне тестов не вызываются. Рабочий приёмник использует `webhook.MemoryDeliveryJournal` из `cmd/beacon/mcp.go:787-796` и `internal/infra/webhook/workflow_run.go:52-82`, тоже in-memory. Точка старта лежит в `cmd/beacon`.

  Уникальный ключ уже даёт no-op. `RecordDelivery` пишет через `INSERT OR IGNORE` в `webhook_events.go:111-117`, повтор не создаёт вторую строку. Второй эффект подавляет runner, как записано в `internal/app/worker/effect.go:23-31`.

  Статус todo сохранён. Код не писал, gofmt не применял, `go test` не запускал. OwnerFiles, чужие файлы и wiring/.beacon не трогал. Блокер возвращён оркестратору. Для карточки нужны OwnerFiles с `internal/infra/webhook/filters.go`, `internal/infra/store/webhook_events.go` и точкой старта в `cmd/beacon`, либо переформулировка шагов под уже существующее поведение.
