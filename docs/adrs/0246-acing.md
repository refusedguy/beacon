# ADR 246: Пacing и предотвращение конфликтов между агентами

Статус: open

## Контекст

Harbor developer workflow: conveyor. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: красный PR принадлежит агенту, оператор направляет агенту, повторный rerun только для timing flakes.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
