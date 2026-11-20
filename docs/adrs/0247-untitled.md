# ADR 247: Документация как отдельный гейт

Статус: open

## Контекст

Harbor developer workflow: docs-artifacts. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: ci.yml игнорирует **.md и docs/ намеренно, docs-only PR идёт в docs.yml с check-md-links и md-lint и self-test, доказывающим, что оба падают на плохом вводе.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
