# ADR 191: Wake-кольцо это подписанный указатель с at-least-once

Статус: accepted

## Контекст

OpenRig POST'ит {source: openrig.queue, schema_version: 1, event: work-waiting, qitem_id, destination, summary, from} и ждёт до 10 секунд. Повтор qitem_id допустим. Подпись Standard Webhooks: webhook-id, webhook-timestamp, webhook-signature = v1, + base64 HMAC-SHA256. Секреты только по именам из env или файла 0600.

## Решение

Сделать wake-указатель отдельным примитивом: ID, версия, указатель на obligation, подпись HMAC, таймаут 10 секунд, результат accepted/indeterminate/failed. Тело задачи читается по grant, а не кладётся в prompt.

## Последствия

Получатель обязан дедуплицировать по ID. Таймаут это indeterminate, а не failure. Повтор после падения демона допустим и не считается ошибкой.
