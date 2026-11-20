# ADR 231: Проверка перед коммитом вместо проверки после

Статус: open

## Контекст

Harbor developer workflow: workflow-lifecycle. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: aif-plan имеет быстрый и полный режим; полный может создавать ветку или worktree, быстрый пишет в PLAN.md.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
