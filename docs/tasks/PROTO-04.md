# PROTO-04. Зафиксировать разделение каналов в шаблонах агентов
Статус: todo
Фаза: P2
Тип: doc
Владелец файлов: docs/templates/executor-task.md, docs/templates/orchestrator-task.md
Зависимости: PROTO-01
Ссылка: 
Агент: нет
Время: нет
Цель: Описать в шаблонах, когда агент зовёт MCP, когда запускает CLI, а когда ждёт Wake.
Контекст: Источник: docs/research/audit-04-protocols.md, раздел а. Диагностика уже доступна через beacon inspect, beacon watch и beacon board, cmd/beacon/catalog.go:35,44,51. CycleInstructions в internal/infra/mcp/server.go:93-101 Велит не опрашивать. AUD-10 обновляет CycleInstructions и AGENTS.md, эта карточка трогает только шаблоны.
Шаги: Добавить в оба шаблона правило: состояние цикла меняется через MCP-инструменты, диагностика идёт через CLI, пробуждение приходит как Wake. Указать, что после Wake агент читает результат через beacon_get_result.
Проверка: Текст проходит проверку unslop: без em dashes, простые слова, активный залог. Шаблоны не противоречат AGENTS.md после AUD-10.
Приёмка: Изменения только в OwnerFiles карточки. AGENTS.md и CycleInstructions не трогаются.
