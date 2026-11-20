# ADR 251: Дешёвые запросы вместо полных чтений

Статус: open

## Контекст

Harbor developer workflow: context-graph. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: AGENTS.md требует для ЛЮБОЙ задачи сначала получать контекст из графа перед grep и открытием исходников.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
