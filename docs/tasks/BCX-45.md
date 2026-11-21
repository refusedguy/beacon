# BCX-45. Защитить MCP-контракт от домино-изменений домена
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: internal/infra/mcp/schema.go, internal/infra/mcp/tool_list_running.go, internal/domain/types.go, internal/infra/mcp/contract_test.go
Зависимости: 
Ссылка: 
Агент: нет
Время: нет
Цель: Предотвратить попадание новых полей domain.Watch в tools/list без решения.
Контекст: Источник: docs/research/mcp-contract-drift-audit.md, таблица 4 строка 5 и оговорка 3. listRunningOutputSchema в schema.go:325-341 вручную расширяет null-карты; поля blockerID, blocked, phases сверх ADR в schema.go:219-226. Watch.Depth из commit 1fc573dd попал в схему через отражение []domain.Watch, хотя коммит затронул только contract_test.go. Золотой вырос с 17961 до 18212 байт. Базовый замер и приоритет из отчёта: P1.
Шаги: Вынести выходную модель list_running из прямого отражения domain.Watch в явный DTO. Запретить новые поля домена в MCP-ответе без правки DTO и золотого. Добавить тест на состав полей.
Проверка: CI зелёный. Тест отвергает новое поле домена, не объявленное в DTO. Золотой меняется только вместе с DTO.
Приёмка: Изменение domain.Watch не меняет MCP-контракт молча.
