# ADR 284: Внутренний планировщик /internal/scheduler

Статус: proposed

## Контекст

Cron, интервалы и webhook-триггеры сейчас размазаны по агентам и demón --interval.

## Решение

Планировщик живёт в сервере на loopback под bearer token, пишет намерение в outbox и не делает внешний I/O.

## Последствия

Расписание работает без активного turn. Нужны signature, filters и dedupe.
