# ADR 200: Ортогональные оси состояния и легальный unknown

Статус: accepted

## Контекст

Agent state taxonomy описывает оси session, activity, resumability; unknown —-first-class ответ, а needs-input не является значением статуса.

## Решение

Beacon разделяет durable-статус, активность и возобновимость и разрешает unknown как честное значение.

## Последствия

Смешивание осей в один enum запрещено; UI отображает неизвестность отдельно.
