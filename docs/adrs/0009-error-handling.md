# ADR 0009. Обработка ошибок

Статус: accepted

## Решение

Внутри кода используем `(value, error)` и общий тип `AppError` с кодом и статусом. Преобразование в единый error shape инструмента MCP происходит на границе MCP.

Тип `Result[T]` не добавляется.

## Причина

Владелец согласился. `Result[T]` не приживается в Go и усложняет чтение кода.

## Последствия

- Коды ошибок из контракта v2 сохраняются: `INVALID_INPUT`, `WATCH_NOT_FOUND`, `HEAD_SHA_CHANGED`, `RATE_LIMITED`, `LIMIT_EXCEEDED`, `CANCELLED`, `GONE`, `PERSIST_FAILED`.
- Статусы 400, 404, 409, 429, 410, 503 соответствуют кодам.
- Формат ответа MCP содержит поля `ok`, `code`, `message`, `status`, `retryAfterMs`, `newWatchID`.
