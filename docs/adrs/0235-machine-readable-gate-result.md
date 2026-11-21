# ADR 235: Машиночитаемый результат гейта

Статус: open

## Контекст

Harbor developer workflow: gate-contract. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: machine-readable gate result contract требует human-readable отчёт и ровно один финальный fence `aif-gate-result` с JSON.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
