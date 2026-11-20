# ADR 187: Durable facts против производного статуса и lease на ожидание

Статус: accepted

## Контекст

Agent Orchestrator хранит только activity_state, hibernated_at, is_terminated, session_mode и PR facts, а working, needs_input и ci_failed вычисляет при чтении. Hibernation подтверждённо-idle сессии использует view lease 10 секунд и просрочку 30 секунд. T3 использует sequence и snapshotSequence как cursor, Paperclip DB-backed wakeup queue с coalescing.

## Решение

Хранить в Beacon факты и события, а статус watch выводить при чтении. Wake считать подсказкой перечитать состояние, а не источником истины. Ввести lease на obligation с wakeTimeoutMs и явными release и fail.

## Последствия

Потеря события не теряет работу, дубликат не запускает вторую копию. По истечении окна obligation снова выглядит созданной, а не тихо зависшей. Клиент с устаревшим cursor уходит в snapshot. Решение о busy-режиме принимается в момент доставки.
