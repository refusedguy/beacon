# ADR 244: Конвейер issue и worktree и subagent

Статус: open

## Контекст

Harbor developer workflow: conveyor. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: harbor-conveyor запускает и аудирует параллельный конвейер PR, суб агенты изолированы worktree и дают ровно один PR против dev.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
