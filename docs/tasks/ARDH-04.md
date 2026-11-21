# ARDH-04. Вынести общий скелет региstrации MCP инструментов
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/infra/mcp/tool_watch_pr.go, internal/infra/mcp/tool_ack.go, internal/infra/mcp/tool_defer_wiring.go, internal/infra/mcp/tool_get_result.go, internal/infra/mcp/tool_wait_result.go, internal/infra/mcp/tool_wake.go, internal/infra/mcp/tool_claim.go
Зависимости: 
Ссылка: ADR 0094, ADR 0263
Агент: нет
Время: нет
Цель: Заменить повторяющийся блок проверки nil, RemoveTools и ToolAnnotations одной функцией attachTool с явным флагом DestructiveHint.
Контекст: Источник: docs/research/duplicate-audit.md, дубль 6, и docs/research/go-metaprogramming.md, раздел про Attach-хелперы. Десять копий no := false и блока аннотаций. beacon_cancel_watch имеет DestructiveHint true, остальные false. Риск: низкий. Сигнатуры обработчиков разные, поэтому последний аргумент останется generic или any.
Шаги: Ввести attachTool в пакете mcp. Перевести десять хелперов. Сохранить ручной список из восьми инструментов и золотой контракт tools/list без изменений.
Проверка: tools/list contract test проходит без обновления SHA и байтового счётчика. CI зелёный.
Приёмка: Общий скелет регистрации инструментов один, восемь инструментов и их аннотации не изменились.

Итог: Уже влито в PR #1209
Доказательство: 8090ea6b в origin/main, merge-base --is-ancestor=0