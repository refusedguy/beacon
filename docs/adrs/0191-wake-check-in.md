# ADR 191: Wake как подсказка: расписания, каналы и check-in

Статус: accepted

## Контекст

Compozy будит сессию после проверок PR, settle другой сессии или выхода команды и делает тихие check-in без вызова модели. Crush Channels пушат CI failures и webhooks в активную сессию. T3 schedule_task работает по интервалу, времени или webhook с signature, filters и dedupe. Paperclip routines создают issue и будят агента с catch-up.

## Решение

Объединить wake-бэкенды под одним контрактом: webhook, schedule, channel push, child report, external relay. Каждый бэкенд проверяет подпись или idempotency key и сообщает start, queued, duplicate или unsupported.

## Последствия

Один и тот же obligation можно разбудить из разных сред без изменения ядра. Тихий check не тратит model call. Повтор вебхука не создаёт новую работу. Events остаются подсказкой, полное чтение состояния остаётся источником истины.
