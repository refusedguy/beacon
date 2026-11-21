# RS2-CMP-03. Явные ступени доставки wake в обязательстве
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/domain/types.go, internal/domain/transition.go, cmd/beacon/inspect.go, internal/infra/ladder/transitions.go, internal/infra/ladder/transitions_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Добавить поля deliveryRung, deliveryAttempts, nextRetryAt и вывести их в inspect.
Контекст: Premise: RS-CMP-03 вернула блокер, domain.Obligationtypes.go:295 маршалится напрямую saveObligation, обёртка не сохранится.
Шаги: Расширить Obligation, добавить revoke-событие, вывести маркеры в inspect, тест на четыре отказа доставки.
Проверка: gofmt, go test в CI.
Приёмка: Одно уведомление человеку на инцидент, повторы привязаны к revoke.
