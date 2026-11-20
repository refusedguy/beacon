# ADR 225: Boot-sweep как защита от commit-then-crash

Статус: accepted

## Контекст

Workflow boot sweep перевооружает keepalive, догоняет routed-but-never-nudged пакеты (last_nudge_attempt IS NULL) и оценивает дедлайны.

## Решение

Beacon при старте восстанавливает потерянные уведомления и истёкшие контрольные точки по durable признакам.

## Последствия

Sweep не движет состояние сам: он arms/nudges/logs, а переход остаётся за проектором.
