# RS2-WK-03. Восстановление dedupe индекса из журнала
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/webhook/filters.go, internal/infra/store/webhook_events.go, cmd/beacon/mcp.go, internal/app/worker/effect.go, internal/app/worker/effect_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Восстановить множества delivery ID и head SHA из webhook_events на старте порциями.
Контекст: Premise: RS-WK-03 вернула блокер, индекс в webhook/filters.go:49-51, точке старта в app/woker нет, постраничного reader в store нет.
Шаги: Добавить reader, вызвать восстановление в composition root, тест на рестарт после доставки.
Проверка: gofmt, go test в CI.
Приёмка: Повтор после рестарта не создаёт второй эффект.
