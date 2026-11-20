# BS.11. Event log с монотонным seq
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/events.go, internal/infra/store/db.go
Зависимости: BS.5
Ссылка: ADR 0190
Агент: нет
Время: нет
Цель: Долговечный журнал с порядком.
Контекст: openrig event-bus и T3 EventSink используют монотонный seq и доставку после коммита.
Шаги: Таблица events(seq, scope, type, schema_version, payload), индекс по scope и seq.
Проверка: Две вставки подряд дают seq+1.
Приёмка: Payload имеет версию схемы для реплея.

## Подтверждение
- Premise проверен 2026-11-20: дубль. Журнал событий с монотонным seq уже в main. `internal/infra/store/events.go` задаёт `LogEvent` и методы `Append` и `ReadAfter`: SQLite присваивает seq при вставке, `ensureEventLog` создаёт индекс `events_by_scope` по `(scope, seq)`, `Append` пишет версию схемы в `schema_version`. Миграция `0004_events.sql` создаёт таблицу `events`, `db.go` вызывает `ensureEventLog` при открытии. Тесты `TestAppendSeqMonotonic`, `TestAppendCarriesSchemaVersion` и `TestReadAfterCursorReplay` покрывают seq+1, версию схемы и реплей. Код в OwnerFiles не писал.
