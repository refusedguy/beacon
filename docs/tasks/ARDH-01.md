# ARDH-01. Свести пять сканеров PRAGMA к одной функции
Статус: done
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/events.go, internal/infra/store/watch_bindings.go, internal/infra/store/backfill_registry.go, internal/infra/store/receipts.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Заменить пять копий сканера PRAGMA table_info функцией tableColumns(q columnQuerier, table) и убрать вторую константу int64 вместе с дублем проверки revision.
Контекст: Источник: docs/research/duplicate-audit.md, таблица дублей 2 и 4, раздел итог с приоритетами. Пять сканеров в events.go:47, watch_bindings.go:125, backfill_registry.go:53, receipts.go:483 и receipts.go:1139. Интерфейс columnQuerier уже есть в watch_bindings.go:118. Риск: низкий. Меняется способ чтения схемы, не меняется поведение вызывающих.
Шаги: Ввести tableColumns в пакете store. Перевести пять вызывающих на множество имён колонок. Оставить одну константу maxSQLiteInt64 и вынести проверку диапazona revision в validateRevision. Обновить затронутые тесты.
Проверка: CI зелёный. Поиск PRAGMA table_info в internal/infra/store показывает только одну реализацию сканера.
Приёмка: Пять сканеров заменены одной функцией, форматы данных и сообщения об ошибках не изменились.

## Подтверждение

PR: https://github.com/refusedguy/beacon/pull/1163

Пять сканеров `PRAGMA table_info` сведены в `tableColumns` в `internal/infra/store/columns.go`. Вторая константа `maxBindingSQLiteInt64` удалена, проверку диапазона revision выполняет `validateRevision`. Добавлен тест `TestTableColumns`. gofmt применён, локальные go test и go build не запускались.
