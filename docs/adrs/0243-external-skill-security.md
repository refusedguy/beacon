# ADR 243: Безопасность внешних скиллов

Статус: open

## Контекст

Harbor developer workflow: loops-evolution. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: aif-loop требует явного подтверждения критериев успеха и максимального числа итераций до старта, даже если они написаны в задании.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
