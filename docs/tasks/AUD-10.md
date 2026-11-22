# AUD-10. Актуализировать CycleInstructions и описание инструментов
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/server.go, AGENTS.md
Зависимости: 
Ссылка: docs/research/BEACON-EXPLAINER-3.html, аудит fix/c18-a17
Агент: нет
Время: нет
Цель: Привести документацию и CycleInstructions к 10 инструментам MCP.
Контекст: Источник: аудит fix/c18-a17, P1.6. AGENTS.md говорит про девять инструментов и не описывает beacon_task_close. CycleInstructions в internal/infra/mcp/server.go не отражает beacon_task_claim и beacon_task_close. baseline: 9 в документации против 10 в коде.
Шаги: Обновить AGENTS.md и CycleInstructions до 10 инструментов, добавить роль beacon_task_claim и beacon_task_close в цикл. Прогнать текст по правилам unslop: без em dashes, простые слова, активный залог.
Проверка: CI зелёный. Документация и CycleInstructions перечисляют одни и те же 10 инструментов. Текст проходит проверку unslop.
Приёмка: Изменения только в OwnerFiles карточки. Уже существующие newReadOnlyOpencodePort, FileTaskClaimStore, FileTaskCloseStore, card_links и поддержку ATX-R01 не переделывать.
