# ADR 240: Проверка правил без полного обзора

Статус: open

## Контекст

Harbor developer workflow: rules-engine. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: aif-rules-check это standalone read-only compliance gate против изменённых файлов или git ref и читает references/RULES-CHECK-CONTRACT.md как канонический источник семантики вердикта.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
