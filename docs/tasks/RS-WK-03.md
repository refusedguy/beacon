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
Шаги: Добавить шаг восстановления в старт воркера. Читать журнал порциями, не держать всё в памяти. Повтор остаётся no-op за счёт уникального ключа. Тест на рестарт после применённой доставки.
Проверка: gofmt чистый. Тесты в CI. Слои соблюдены: app может читать store.
Приёмка: После рестарта повторная доставка с тем же ID не создаёт второй эффект.
