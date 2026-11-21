# RS-CMP-03. Явные ступени доставки wake
Статус: todo
Фаза: P3
Тип: task
Владелец файлов: internal/infra/ladder/transitions.go, internal/infra/ladder/escalation.go, internal/infra/ladder/transitions_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Различать отказ доставки из-за промпта, лимита, исчезнувшей панели и отсутствия человека, с полями deliveryRung, deliveryAttempts и nextRetryAt.
Контекст: Premise: проверено 2026-11-21, жёсткий лимит в шесть wake и ESCALATIONS.md есть, но ступени доставки не поименованы. baseline: `grep -rn 'deliveryRung\|nextRetryAt' internal/infra/ladder` пусто. Источник: OpenRig queue.wake_human_rung и Claude workflows с лимитом пять рестартов.
Шаги: Добавить поля ступени и времени следующей попытки в обязательство. Разрешить только одно активное human alert на инцидент. Тест на четыре отказа доставки: в чат уходит одно уведомление, повторы привязаны к revoke-событию.
Проверка: gofmt чистый. Тесты в CI. Счётчик wake по Watch не меняется.
Приёмка: Лестница доставки видна в beacon inspect. Повтор не создаёт новое уведомление человеку.
