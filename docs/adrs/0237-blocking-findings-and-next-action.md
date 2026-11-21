# ADR 237: Блокирующие находки и следующий шаг

Статус: open

## Контекст

Harbor developer workflow: gate-contract. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: схема поля schema_version, gate, status pass/warn/fail, blocking boolean, blockers с id, severity, file, summary, affected_files и suggested_next.command.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
