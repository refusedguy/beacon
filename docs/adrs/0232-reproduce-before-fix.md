# ADR 232: Воспроизведение до исправления

Статус: open

## Контекст

Harbor developer workflow: regression-grounding. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: aif-fix имеет каноническую regression-first политику: минимальный regression check выполняется до реализации и должен воспроизвести проблему.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
