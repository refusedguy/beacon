# ADR 102: Четыре режима доставки follow-up

Статус: proposed

## Контекст

Один steer не покрывает очередь, перезапуск и активный online turn.

## Решение

Поддерживаются auto, queue, steer и restart. Ключ clientRequestId задаёт command, message и wake id.

## Последствия

Повтор после сетевой ошибки возвращает прежний receipt. Terminal состояние защищено от поздних событий.
