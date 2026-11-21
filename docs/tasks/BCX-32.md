# BCX-32. Привести пять определений DDL registry к одному каноническому
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/migrations/0005_agent_registry.sql, internal/infra/store/registry.go, internal/infra/store/watch_bindings.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать конфликт типов и три одинаковых индекса с разными именами.
Контекст: Источник: docs/research/sqlite-perf-audit.md, топ-10 строка 2. fencing_token INTEGER в миграции и watch_bindings.go:34-48, но TEXT в registry.go:77. capabilities TEXT в миграции и BLOB в registry.go:56. digest и CHECK-ограничения есть только в миграции. Индексы watch_bindings_by_agent, by_executor, by_project_agent все (project_key, agent_id). Известное исправление PR #1168 не лежит в main. Базовый замер и приоритет из отчёта: P1.
Шаги: Выбрать канонический DDL из встроенной миграции. Миграцией удалить два лишних индекса и привести типы к одному виду. Проверить современное состояние main на PR #1168 перед началом. Спроектировать пересборку таблицы и миграцию данных.
Проверка: CI зелёный. Тест схемы проверяет типы и число индексов. CAST-обходные пути удалены или помечены как устаревшие.
Приёмка: Одно определение двух таблиц. Тип fencing_token и capabilities одинаков во всех местах.
