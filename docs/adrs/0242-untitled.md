# ADR 242: Самоулучшение скиллов по ошибкам проекта

Статус: open

## Контекст

Harbor developer workflow: loops-evolution. Проверено в /home/nbook/Projects/Harbor-Harness.

## Решение

Harbor: EVALUATE запускает подготовленные проверки и контентные правила, агрегирует score; пороги A 0.8 и B 0.9 по умолчанию, максимум 4 итерации.

## Последствия

Требует карточек HD и eval HD. Правила применяются к Beacon без переноса кода Harbor.
