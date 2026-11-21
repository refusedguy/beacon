# BCX-46. Документировать одиннадцать добавочных полей get_result
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/schema.go, internal/infra/mcp/tool_get_result.go, docs/adrs/0094-seven-tools.md
Зависимости: 
Ссылка: ADR 0094
Агент: нет
Время: нет
Цель: Свести перечень полей ответа get_result с документацией контракта.
Контекст: Источник: docs/research/mcp-contract-drift-audit.md, таблица 4 строка 6. schema.go:115-138 определяет resultType, taskId, jobID, taskStatus, pollIntervalMs, instructions, nextAction, nextCommand, nextHint, failureExcerpt, configId. Большинство не перечислены в ADR 0094. Forward-compat зонд в contract_test.go:526-544 добавляет поля без объявления. Базовый замер и приоритет из отчёта: P1.
Шаги: Перечислить все поля в ADR или в версионированном приложении схемы. Разделить обязательные и forward-compatible поля. Заменить зонд, который добавляет поля без объявления, на проверку известного набора.
Проверка: CI зелёный. Тест сверяет набор полей ответа с документацией. Все одиннадцать полей имеют описание.
Приёмка: Клиент видит полный перечень полей и их назначение в контракте.
