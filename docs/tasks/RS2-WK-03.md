# RS2-WK-03. Восстановление dedupe индекса из журнала
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/infra/webhook/filters.go, internal/infra/store/webhook_events.go, cmd/beacon/mcp.go, internal/app/worker/effect.go, internal/app/worker/effect_test.go
Зависимости: 
Ссылка: PR #872
Агент: нет
Время: нет
Цель: Восстановить множества delivery ID и head SHA из webhook_events на старте порциями.
Контекст: Premise: RS-WK-03 вернула блокер, индекс в webhook/filters.go:49-51, точке старта в app/woker нет, постраничного reader в store нет.
Шаги: Добавить reader, вызвать восстановление в composition root, тест на рестарт после доставки.
Проверка: gofmt, go test в CI.
Приёмка: Повтор после рестарта не создаёт второй эффект.

## Подтверждение
- 2026-11-21, origin/main 17dfe44. PR #872 (https://github.com/refusedguy/beacon/pull/872, merge commit a7634e9) влит. `grep -n 'ListWebhookDeliveries' internal/infra/store/webhook_events.go` = 201, `grep -n 'restoreWebhookFilter' cmd/beacon/mcp.go` = 792, `grep -n 'func (f \*Filter) Restore' internal/infra/webhook/filters.go` = 69. Старт читает journal страницами по 256 строк и наполняет индекс. Тест на рестарт проверяет, что повторная доставка не ставит второй эффект. PR #916 (commit d176349) дополнительно сохранил Conflict после восстановления. Блокер снят. Код не писал, gofmt не применял, OwnerFiles не менял, wiring и .beacon не трогал.
