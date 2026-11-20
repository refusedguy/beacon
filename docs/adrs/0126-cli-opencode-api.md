# ADR 126: Использование CLI opencode api без плагина

Статус: accepted

## Контекст

Нужен простой путь побуждения извне без регистрации RPC-плагина; orchestrator работает в shell и имеет доступ к opencode api.

## Решение

Синтаксис: opencode api [flags] <operation | method path...>, флаги --data/-d, --header/-H, --param key=value, --server. Путь и operation ID дают одинаковый результат: opencode api post /api/session/ses_123/prompt --data '{"text":"...","delivery":"steer"}' либо opencode api session.prompt --param sessionID=ses_123 --data '...'.

## Последствия

Обнаружение эндпоинтов: opencode api GET /openapi.json. SSE: GET /api/event с types; подписка начинается с server.connected и не воспроизводит прошедшие события. --server подключается к URL вместо фонового сервиса. interrupt и experimental.session.wait доступны тем же CLI.
