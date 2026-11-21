# RS2-WK-04. Различить Duplicate и Conflict в приёмнике
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: cmd/beacon/mcp.go, internal/infra/webhook/workflow_run.go, internal/infra/store/webhook_events.go, internal/infra/webhook/receiver.go, internal/infra/webhook/receiver_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Пробросить результат RecordDelivery в приёмник и явно отвечать успехом для Duplicate.
Контекст: Premise: RS-WK-04 вернула блокер, порт DeliveryRecorder.Record возвращает только bool, адаптер в cmd/beacon/mcp.go:787-796.
Шаги: Расширить порт, пробросить Duplicate и Conflict, тест на изменение тела и повтор с тем же ID.
Проверка: gofmt, go test в CI.
Приёмка: Провайдер прекращает ретраи после первого эффекта.
