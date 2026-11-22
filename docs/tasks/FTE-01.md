# FTE-01. Привязать порты ADR 0314 в композитном корне MCP
Статус: todo
Фаза: P0
Тип: task
Владелец файлов: cmd/beacon/mcp.go, cmd/beacon/mcp_test.go
Зависимости:
Ссылка: README.md:64,130, AGENTS.md:60, docs/research/research-feature-gaps.md:11,27-33
Агент: нет
Время: нет
baseline: git grep по cmd/beacon/mcp.go не находит GraphIndexReader, RegistryStatsReader или GenerationLedger. Порты объявлены в internal/infra/mcp/server.go:481-487 и internal/infra/mcp/tool_stats.go:169-175, но композитный корень их не задаёт.
Цель: Маркер graphReady в beacon_stats и beacon_task_claim отражает состояние реального индекса карточек, а beacon inspect --json не показывает no-reader.
Контекст: README.md:64 и README.md:130 обещают graphReady в ответе beacon_task_claim. AGENTS.md:60 называет этот маркер признаком актуального графа. internal/infra/mcp/server.go:733-734 и :763-765 ставят сеттеры только при не-nil порте. buildMCPDependencies в cmd/beacon/mcp.go:748 не передаёт ни один из трёх портов. internal/infra/mcp/tool_stats.go:573 задаёт источник no-reader, а cmd/beacon/inspect.go:201-202 печатает его как graphReady.source. AUD-04 закрыта, но правка mcp.go в неё не входила.
Шаги: Построить GraphIndexReader поверх индекса задач, который уже отдаёт GraphStatus. Передать GraphIndexReader, RegistryStatsReader и GenerationLedger в mcp.Dependencies. Добавить тест, что production-зависимости несут не-nil порт. Не трогать внутренности обработчиков.
Проверка: beacon_stats содержит маркер свежего графа. beacon_task_claim возвращает graphReady: true. beacon inspect --json показывает graphReady.source отличным от no-reader. Отрицательный контроль: после правки карточки без пересборки индекса источник становится stale.
Приёмка: Три порта привязаны, оба маркера отражают состояние индекса, CI зелёный.
