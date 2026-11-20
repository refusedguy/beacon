# W10_file_spool_fallback

## Сценарий

Событие experimental.mcp.event не подтверждено или stdio-процесс оборвался; Beacon пишет events.jsonl.

## Проверки

- Плагин видит новую запись через watch или polling не реже 2 с
- Курсор offset плюс epoch в ctx.storage не допускает повторной обработки
- Вызов ctx.session.prompt приводит к session.execution.started

## Негативный контроль

Тот же файл без плагина-наблюдателя: данные записаны, но сессия не будится.
