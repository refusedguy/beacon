# ADR 185: MCP как единый ingress внешнего control plane

Статус: accepted

## Контекст

T3 Code, Compozy, Mission Control и OpenRig держат состояние агентов в локальном сервере на SQLite и открывают его через MCP, HTTP/SSE, CLI и TUI. T3 использует аутентифицированный HTTP MCP на 127.0.0.1 и разделяет toolbox и blocking-инструменты на два registry. Beacon уже MCP-сервер на Go с obligation и wait_result, get_result, ack.

## Решение

Развивать Beacon как control plane: MCP остаётся основным ingress, состояние живёт в локальном durable store, остальные поверхности читают то же состояние. Ввести capability discovery до вызова и отдельный registration для BLOCKING-инструментов wait, чтобы обойти Code Mode и таймауты.

## Последствия

Поверхности не расходятся в статусах. Неподдерживаемый бэкенд возвращает unsupported с причиной вместо имитации очереди. Любая новая команда получает typed failure, а не строку ошибки. Аутентификация transport не даёт права на все методы.

## Происхождение

Перенесён дословно из коммита `35d149d` (HEAD ветки `feat/batch-competitors-v2`) методом `git show 35d149d:docs/adrs/0185-mcp-ingress-control-plane.md`. В `origin/main` этого пути не было: там под номером 0185 лежат другие решения (`0185-mcp-beacon-wake.md`, `0185-otel-tracing.md`, `0185-untitled.md`). Коллизии имён нет, текст не выдуман. Связано с инвентарём `docs/inventory/F01-F80.md` и карточкой `docs/tasks/T3.240.md`.
