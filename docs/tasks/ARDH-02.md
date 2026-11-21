# ARDH-02. Выбрать каноническую схему watch_bindings и agent_registry
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/store/migrations/0005_agent_registry.sql, internal/infra/store/registry.go, internal/infra/store/watch_bindings.go, internal/infra/store/backfill_registry.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Свести три определения watch_bindings и два определения agent_registry к одному источнику схемы с описанием миграции существующих баз.
Контекст: Источник: docs/research/duplicate-audit.md, дубли 1 и 3, раздел приоритет 2. В миграции fencing_token INTEGER и есть CHECK, в registry.go тот же столбец TEXT без CHECK. В agent_registry миграция добавляет digest и agent_capabilities, код их не создаёт. Риск: средний. Нужен owner-decision: кто владеет digest и runtime и как обновляются уже созданные базы. Без решения слияние сломает совместимость.
Шаги: Зафиксировать каноническую схему и план backfill. Получить решение владельца. Написать миграцию списания, добавить тест на старые и новые базы, затем удалить два других DDL и ветки определения варианта.
Проверка: Тесты миграций проходят на базе старой и новой схемы. CI зелёный.
Приёмка: Один DDL на таблицу и один путь первичного создания. Карточка остаётся todo до решения владельца.
