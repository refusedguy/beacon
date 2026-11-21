# RS2-WK-04. Различить Duplicate и Conflict в приёмнике
Статус: done
Фаза: P3
Тип: task
Владелец файлов: cmd/beacon/mcp.go, internal/infra/webhook/workflow_run.go, internal/infra/store/webhook_events.go, internal/infra/webhook/receiver.go, internal/infra/webhook/receiver_test.go
Зависимости: 
Ссылка: PR #874
Агент: нет
Время: нет
Цель: Пробросить результат RecordDelivery в приёмник и явно отвечать успехом для Duplicate.
Контекст: Premise: RS-WK-04 вернула блокер, порт DeliveryRecorder.Record возвращает только bool, адаптер в cmd/beacon/mcp.go:787-796.
Шаги: Расширить порт, пробросить Duplicate и Conflict, тест на изменение тела и повтор с тем же ID.
Проверка: gofmt, go test в CI.
Приёмка: Провайдер прекращает ретраи после первого эффекта.

## Подтверждение
- 2026-11-21, origin/main 17dfe44. PR #874 (https://github.com/refusedguy/beacon/pull/874, merge commit c1ad8d4) влит. `grep -n 'Duplicate' internal/infra/webhook/receiver.go` = 50, `grep -n 'Conflict' internal/infra/webhook/receiver.go` = 51, `grep -n 'RecordResult' internal/infra/webhook/workflow_run.go` = 75. Порт Record возвращает Created, Duplicate и Conflict. Приёмник отвечает 202 для повтора с тем же телом и для повтора с изменённым телом, эффект публикует только первая доставка. Блокер снят. Код не писал, gofmt не применял, OwnerFiles не менял, wiring и .beacon не трогал.
