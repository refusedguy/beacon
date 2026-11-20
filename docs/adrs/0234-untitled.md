# ADR 234: Добавление логирования и покрытия при исправлении

Статус: open

## Контекст

Harbor developer workflow: regression-grounding. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: aif-fix поддерживает fix-now и plan-first, без аргументов исполняет FIX_PLAN.md, добавляет логирование и предлагает дополнительное покрытие.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
