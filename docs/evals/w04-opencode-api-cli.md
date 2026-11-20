# W04_opencode_api_cli

## Сценарий

orchestrator в shell не хочет писать плагин. Нужно побудить сессию через opencode api и observe idle через SSE.

## Проверки

- opencode api session.prompt --param sessionID=... --data ... будит сессию
- Путь post /api/session/.../prompt даёт тот же результат
- GET /api/event с types показывает session.idle

## Негативный контроль

Подписка не видит прошедшие события до server.connected; без плагина RPC не вызывается.
