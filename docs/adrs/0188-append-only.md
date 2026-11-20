# ADR 188: Восемь состояний обязательства и append-only переходы

Статус: accepted

## Контекст

QUEUE_STATES в коде: pending, in-progress, done, blocked, failed, denied, canceled, handed-off; queue_transitions — append-only аудит.

## Решение

Beacon вводит явную машину состояний обязательства и неизменяемый журнал переходов, доступный для replay и аудита.

## Последствия

Любая мутация состояния пишет переход; редактирование истории запрещено, чистка только по ретенции.
