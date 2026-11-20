# ADR 80: Сравнение wake-механизмов Pi, Kilo Code и Cursor

Статус: accepted

## Контекст

Отчёты от 20 ноября 2026 описывают три модели. В ядре Pi нет фоновых агентов и планировщика сна; продолжение идёт через очереди, sendMessage и durable. Kilo Code (коммит bd8b98aeab348c08221a836ba091b855a62814e9) имеет четыре слоя: Wakeup-таймеры, goal wait, background task и durable-события с seq. Cursor делит входящий триггер, follow-up/steer, SSE-доставку и пул воркеров list-then-watch.

## Решение

Принять один контракт wake-адаптера поверх трёх моделей. Wake — совет перечитать состояние, а не источник истины (Kilo, Cursor). Различать newRun, steer, followUp, signal и resume (Pi). Busy-режимы: preempt, coalesce, queue (Cursor). Агент сам не просыпается: нужен внешний слушатель, таймер или durable resume (Pi). Бэкенд выбирается конфигурацией цели.

## Последствия

Потеря сигнала не теряет работу, дубликат не запускает вторую копию из-за блокировки на записи (Kilo). FINISHED/IDLE у Cursor и agent_end у Pi не равны готовности. Решение steer/followUp/newRun принимается по состоянию в момент доставки, не при enqueue (Pi). В Kilo wakeup пишется в Storage до таймера и перечитывается в adopt.
