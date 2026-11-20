# BS.11. Event log с монотонным seq
Статус: todo
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
