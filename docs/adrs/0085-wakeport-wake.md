# ADR 85: WakePort: общий контракт wake-адаптера

Статус: accepted

## Контекст

Pi предлагает WakeKind newRun|steer|followUp|signal|resume|noop, WakeRequest с target, kind, payload, key, at, deadline и WakePort с state, wake, wait. Cursor предлагает один интерфейс wake(target, payload, options) с бэкендами webhook, followup, channel, pool-claim по конфигурации цели. Kilo разделяет admission через SessionInput.admit и execution.wake/resume, где resume:false — admit-only.

## Решение

Принять WakePort: state(target) -> {idle, settled, busy}; wake(req) -> {started, queued, delivered, duplicate, closed, unsupported}; wait(target, cursor). Бэкенд выбирается конфигурацией цели, не кодом вызвавшего. Если среда не умеет доставлять в открытую сессию, адаптер возвращает unsupported с причиной и не выдаёт очередь за текущий ход.

## Последствия

Ядро видит adapter и target как непрозрачные строки. Решение о режиме доставки принимается в момент доставки по состоянию и не кэшируется при enqueue. Нужны явные лимиты на активные watch: Cursor ставит четыре stream на service account и 50 на вызывающего. Права на wake, чтение статуса и управление воркерами разделены.
