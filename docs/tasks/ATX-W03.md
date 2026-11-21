# ATX-W03. Не считать подтверждённое обязательство висящим
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/db.go, internal/infra/store/db_test.go
Зависимости: 
Ссылка: docs/research/watch-lifecycle-gaps.md, строка ATX-W03
Агент: нет
Время: нет
Цель: Исключить обязательства с `ClaimedAt` или `ReadAt` из `PendingObligations`.
Контекст: Источник: docs/research/watch-lifecycle-gaps.md, раздел «Подтверждённое обязательство остаётся висящим». `beacon_ack` вызывает `StampClaimed` в `internal/infra/mcp/tool_ack.go:631`, который меняет только payload. Колонка `delivered_at` остаётся нулевой, поэтому запрос в `internal/infra/store/db.go:494-516` возвращает строку. Потребители в `cmd/beacon/mcp.go:1024` и `cmd/beacon/registry_stop.go:289` выбирают её как активную и могут послать wake со старым `obligationID` или отказать в остановке агента. Baseline: ack остаётся идемпотентным, но не очищает и не скрывает обязательство.
Шаги: Добавить в SQL-условие проверку payload на ненулевые `ClaimedAt` и `ReadAt` или завести отдельные колонки с миграцией. Сохранить порядок `ORDER BY seq`. Обновить тесты `PendingObligations` на случай с подтверждённым обязательством. Проверить, что повторный `StampClaimed` не ломает идемпотентность.
Проверка: CI зелёный. После `StampClaimed` метод возвращает пустой список. Неподтверждённое обязательство по-прежнему возвращается.
Приёмка: Human wake и остановка агента не видят уже подтверждённые обязательства.
