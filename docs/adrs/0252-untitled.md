# ADR 252: Обновление графа после изменений

Статус: open

## Контекст

Harbor developer workflow: context-graph. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: graft ask --sourceвыдаёт ранжированные ноды с встроенными отрывками до 8 строк, --full для целых определений.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
