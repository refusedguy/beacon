# RS-WK-04. Уже обработанная доставка как успешный ответ
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/webhook/receiver.go, internal/infra/webhook/receiver_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Возвращать успех для уже обработанного события, чтобы остановить ретраи провайдера.
Контекст: Premise: проверено 2026-11-21, RecordDelivery различает Created, Duplicate и Conflict, но поведение приёмника для Duplicate не зафиксировано контрактом. baseline: `grep -n 'Duplicate' internal/infra/store/webhook_events.go` находит результат, `grep -n 'Duplicate' internal/infra/webhook/receiver.go` пусто. Источник: Stripe manage-webhook-endpoints.
Шаги: Явно обработать Duplicate как успешный ответ без повторной публикации. Не путать с Conflict по телу. Тест на повтор с тем же X-GitHub-Delivery и на изменение тела.
Проверка: gofmt чистый. Тесты в CI. Подпись проверяется до дедупликации.
Приёмка: Провайдер прекращает ретраи после первого успешного эффекта.
