# ADR 285: Webhook receiver с подписью и дедупликацией доставки

Статус: proposed

## Контекст

GitHub отдаёт только часть actions для check_run и check_suite, а доставка может потеряться.

## Решение

Receiver слушает loopback, проверяет HMAC по сырому телу constant-time, пишет UNIQUE(delivery_id) и оставляет polling fallback.

## Последствия

Одна доставка порождает одну работу. Публичный IP не нужен, нужен туннель.
