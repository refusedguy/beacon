# ADR 236: Двухосевой обзор и строгость вердикта

Статус: open

## Контекст

Harbor developer workflow: gate-contract. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: Supported gates: verify, review, security, rules; оркестратор читает последний fence в финальном выводе.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
