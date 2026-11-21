# BCX-41. Выбрать один контракт MCP для main и зафиксировать решение
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: internal/infra/mcp/server.go, internal/infra/mcp/contract_test.go, docs/adrs/0094-seven-tools.md, docs/adrs/0263-mcp-boundary.md, docs/adrs/0268-task-claim-tool.md
Зависимости: 
Ссылка: ADR 0094
Агент: нет
Время: нет
Цель: Прекратить сосуществование восьми- и девятиинструментального контрактов в одном хранилище.
Контекст: Источник: docs/research/mcp-contract-drift-audit.md, главная находка 1 и таблица 4 строка 1. beacon_task_claim зарегистрирован в 43 worktrees через toolDescriptors, имеет ADR 0268, схему и золотой 18212 / ba0a9f. Main остался на 16095 и восьми именах. ADR 0268 файла в main нет. Базовый замер и приоритет из отчёта: P0.
Шаги: Проверить текущий main: ToolNames, contract_test.go и наличие ADR 0268. Выбрать восемь или девять имён. При выборе девяти перенести ADR в main, пересмотреть ADR 0094, ADR 0263, AGENTS.md и золотой байт и SHA в одном PR. При выборе восьми явно закрыть ADR 0268 как не принятый.
Проверка: CI зелёный. Золотой tools/list, ADR и AGENTS.md называют одно число инструментов. TestToolsListStabilityAcrossConnections проходит.
Приёмка: Два разных контракта не существуют одновременно. Любая регистрация девятого имени имеет акт решения владельца.
