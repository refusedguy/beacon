# ADR 245: Гейт мержа с двумя осями проверки

Статус: open

## Контекст

Harbor developer workflow: conveyor. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: merge gate требует fail=0, pend=0, mergeStateStatus CLEAN и совпадение diff с областью issue; merge только по всем условиям.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
