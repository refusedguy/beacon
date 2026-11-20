# ADR 249: Действующие записи против архива аудита

Статус: open

## Контекст

Harbor developer workflow: docs-artifacts. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: llms.txt и llms-full.txt генерируются tools/gen-llms.go; после добавления документа обновляется список источников и проверяется --check.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
