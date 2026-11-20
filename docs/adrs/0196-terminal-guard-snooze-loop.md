# ADR 196: Terminal guard и snooze как защита от loop

Статус: proposed

## Контекст

Поздние события и completion wake могут заново будить завершённую работу.

## Решение

Терминальный статус защищён от поздних записей. Snoozed thread будится только при pending request или failure.

## Последствия

Статусы стабильны. Нужны тесты на seeded late event.
