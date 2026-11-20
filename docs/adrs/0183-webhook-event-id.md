# ADR 183: Webhook как подсказка и event_id как ключ идемпотентности

Статус: accepted

## Контекст

GitHub не делает автоматический redelivery: доступен только ручной за последние 3 дня, и он сохраняет тот же X-GitHub-Delivery. Repository webhooks получают не все действия check_run и check_suite. Поле sender бывает ghost, а push в fork даёт пустой pull_requests. Payload ограничен 25 MB. Текущий cibridge тикает каждые 5 секунд и тратит около 4700 GraphQL-поинтов в час на 15 Watch.

## Решение

Принимать check_run, check_suite, pull_request и workflow_run на loopback. Проверять HMAC-SHA256 на сыром теле constant-time, дедуплицировать X-GitHub-Delivery через domain.IdempotencyKey('github-delivery', id) и журнал с UNIQUE(delivery_id). Мапить событие на Watch по repo + PR + head_sha. Handler не меняет вердикт, а помечает Watch к перечитыванию; вердикт публикуется только после чтения состояния батчем и Transition.

## Последствия

Идемпотентность ввода по event_id и вывода по (watch_id, revision) из ADR 0015 не смешивается с clientRequestId ADR 0028 и deliveryId wake ADR 0081. Устаревший headSHA не меняет ревизию. Потеря доставки закрывается polling-fallback. Повреждённый журнал доставок даёт отказ, а не молчаливый сброс.
