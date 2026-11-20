# R02_same_pointer_once

## Сценарий

Два последовательных вызова daemonWaker.Wake с одним watchID и revision после рестарта процесса.

## Проверки

- второй вызов не создаёт prompt
- T211DeliveryRegistry.Has возвращает true
- счётчик wake_duplicate_total растёт

## Негативный контроль

новая ревизия создаёт новый prompt.
