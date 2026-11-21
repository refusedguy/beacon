# ADR 229: Жизненный цикл задачи как последовательность команд

Статус: open

## Контекст

Harbor developer workflow: workflow-lifecycle. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: `.agent/workflows/` содержит 9 action workflows: aif, aif-explore, aif-plan, aif-improve, aif-fix, aif-implement, aif-verify, aif-commit, aif-rules-check.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
