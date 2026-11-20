# ADR 157: Побуждение отложенной задачи через PATCH inbox

Статус: accepted

## Контекст

Задачу нужно припарковать до освобождения агента; модель Beacon держит задачу в watching до события пробуждения. Отложенная задача уже лежит в inbox с доставкой queue или resume false.

## Решение

Парковать через prompt с delivery queue или resume false, взять id из data.id ответа. Затем PATCH /api/session/{sessionID}/inbox/{inboxID} с {"delivery": "steer"}. Описание: Change a pending inbox item's delivery mode. Steering wakes session execution. Ответ 204.

## Последствия

Тип inboxID - Session.Message.ID, совпадает с id ответа prompt. Обратная смена на queue тем же вызовом. GET /api/session/{sessionID}/inbox показывает недоставленные задачи в порядке enqueue; DELETE отменяет задачу (no-op для недоступных).
