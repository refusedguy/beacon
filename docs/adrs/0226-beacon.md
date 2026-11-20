# ADR 226: Слои инструкций для агентов в Beacon

Статус: open

## Контекст

Harbor developer workflow: instruction-layering. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: `AGENTS.md` 1025 строк это операционный гид, `CLAUDE.md` 1271 строкаконвенции, `CONTRIBUTING.md` 4,9 КБ; AGENTS прямо отсылает к CLAUDE за соглашениями.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
