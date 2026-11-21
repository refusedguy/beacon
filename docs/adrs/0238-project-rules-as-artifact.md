# ADR 238: Проектные правила как отдельный артефакт

Статус: open

## Контекст

Harbor developer workflow: rules-engine. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: aif-rules добавляет project-specific rules и conventions в RULES.md, каждый вызов добавляет новые правила.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
