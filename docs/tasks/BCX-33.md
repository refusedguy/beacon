# BCX-33. Кэшировать схему и убрать разные CAST для fencing_token
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/store/watch_bindings.go, internal/infra/store/registry_scoped.go, internal/infra/store/backfill_registry.go
Зависимости: 
Ссылка: 
Агент: exe146
Время: 2026-11-21T18:07:46Z
Цель: Перестать читать PRAGMA table_info на каждом вызове и маскировать типы приведением.
Контекст: Источник: docs/research/sqlite-perf-audit.md, топ-10 строка 3. watch_bindings.go:124-166 читает CAST(fencing_token AS INTEGER), registry_scoped.go:167-171 читает CAST(fencing_token AS TEXT). backfill_registry.go:49-89 собирает два варианта INSERT; комментарий в :50-51 устарел. Базовый замер и приоритет из отчёта: P2.
Шаги: Считать набор колонок один раз при Open и хранить в структуре store. После BCX-32 убрать ветвления и CAST. Обновить комментарий в backfill_registry.go.
Проверка: CI зелёный. Тест проверяет отсутствие PRAGMA table_info в горячих путях. Оба ридера получают одно значение.
Приёмка: Чтение схемы происходит один раз на открытии базы. Тип колонки не маскируется приведением.
