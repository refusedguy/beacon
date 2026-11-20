# E06_block_reason_survives_mcp_restart

## Сценарий

Executor вызвал beacon_ack с decision=blocked:not_mine, затем процесс MCP перезапустился.

## Проверки

- Watch по-прежнему имеет BlockedInfo с failedChecks
- events содержит watch_blocked с blockedBy из _meta
- reason не пустой после рестарта

## Негативный контроль

Хранение только в in-memory BlockedRegistry приводит к потере причины и падению теста.
