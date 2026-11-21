# ATX-30. Восстановить NoChecksTimeout и переносить дедлайн преемнику
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/domain/verdict.go, internal/domain/types.go, internal/infra/cibridge/bridge.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Сделать правило таймаута без проверок достижимым и сохранить дедлайн после force-push.
Контекст: Источник: docs/research/watch-lifecycle-audit.md, находка 3 и строка таблицы 116. internal/domain/verdict.go:52-54 описывает NoChecksSince и NoChecksTimeout. Правило 8 в :230-233 требует оба поля. internal/domain/types.go:262-263 хранит только NoChecksSince. Ни один продакшен-путь его не пишет, а snapshotFor в bridge.go:640-659 не заполняет NoChecksTimeout. Проверка в verdict.go:315-318 всегда ложна. handleHeadChanged в bridge.go:694-712 создаёт преемника без DeadlineAt, NoChecksSince, BlockerID, FixBlockerID и ConfigID. Baseline: два поля правила, одно из которых всегда ноль.
Шаги: Добавить поле timeout в Watch и конфиг. Заполнять его в snapshotFor. Копировать DeadlineAt и NoChecksSince в преемника вместе с BlockerID, FixBlockerID и ConfigID.
Проверка: CI зелёный. Тест с заданным NoChecksSince и истёкшим timeout даёт VerdictTimeout. Тест force-push сохраняет DeadlineAt у преемника.
Приёмка: Таймаут без проверок срабатывает. Преемник наследует все поля дедлайна и блокировок.
