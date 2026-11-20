# E13_dependabot_merge_guard

## Сценарий

Изменить head PR между проверкой и мержем dependent PR.

## Проверки

- expectedHeadOid передан
- мерж отменён
- журнал мержа записан

## Негативный контроль

мерж без expectedHeadOid применяет устаревший SHA
