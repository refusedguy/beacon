# E04_check_conclusion_mapping

## Сценарий

Набор CheckRun с conclusion SUCCESS, FAILURE, CANCELLED и незавершённый check.

## Проверки

- все SUCCESS дают verdict ready
- FAILURE или CANCELLED дают retry
- незавершённый check оставляет watching

## Негативный контроль

Текущий ghCheckConclusion_UNKNOWN мапит неизвестное состояние в PENDING.
