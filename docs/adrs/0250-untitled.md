# ADR 250: Контекст репозитория перед чтением файлов

Статус: open

## Контекст

Harbor developer workflow: context-graph. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: graft это проиндексированный граф markdown-нодов с точными file:line,_linked с кодом и синхронизированный через git.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
