# ADR 241: Итеративный цикл качества с возвратом

Статус: open

## Контекст

Harbor developer workflow: loops-evolution. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: aif-loop это шесть фаз PLAN, PRODUCE, PREPARE, EVALUATE, CRITIQUE, REFINE; PRODUCE и PREPARE идут параллельно, CRITIQUE и REFINE только при провале.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
