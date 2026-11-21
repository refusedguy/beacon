# RS2-CMP-03. Явные ступени доставки wake в обязательстве
Статус: done
Фаза: P3
Тип: task
Владелец файлов: internal/domain/types.go, internal/domain/transition.go, cmd/beacon/inspect.go, internal/infra/ladder/transitions.go, internal/infra/ladder/transitions_test.go
Зависимости: 
Ссылка: PR #877
Агент: нет
Время: нет
Цель: Добавить поля deliveryRung, deliveryAttempts, nextRetryAt и вывести их в inspect.
Контекст: Premise: RS-CMP-03 вернула блокер, domain.Obligationtypes.go:295 маршалится напрямую saveObligation, обёртка не сохранится.
Шаги: Расширить Obligation, добавить revoke-событие, вывести маркеры в inspect, тест на четыре отказа доставки.
Проверка: gofmt, go test в CI.
Приёмка: Одно уведомление человеку на инцидент, повторы привязаны к revoke.

## Подтверждение
- 2026-11-21, origin/main 17dfe44. PR #877 (https://github.com/refusedguy/beacon/pull/877, merge commit 961f835) влит. `grep -n 'DeliveryRung' internal/domain/types.go` = 319, `grep -n 'HumanAlerted' internal/domain/types.go` = 138, `grep -n 'func (l \*Ladder) Revoke' internal/infra/ladder/transitions.go` = 499. Тест TestFourDeliveryFailuresIssueOneHumanAlert проверяет одно уведомление на четыре отказа и повторы после revoke. Блокер снят. Код не писал, gofmt не применял, OwnerFiles не менял, wiring и .beacon не трогал.
