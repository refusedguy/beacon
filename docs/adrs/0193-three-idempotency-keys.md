# ADR 193: Три ключа идемпотентности для всех фоновых задач

Статус: proposed

## Контекст

Сетевая ошибка после доставки может создать вторую работу или второй wake.

## Решение

clientRequestId задаёт command и message id, (watchID, revision) задаёт obligation, X-GitHub-Delivery задаёт webhook_event.

## Последствия

Повтор возвращает прежний receipt. Три ключа не делят слот.
