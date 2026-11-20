# E11_metrics_counters

## Сценарий

Скрэпить /metrics при 15 активных вотчах.

## Проверки

- счётчики совпадают с beacon_stats
- RSS в пределах цели
- Processes gh не растут

## Негативный контроль

метрики с watchID в labels дают взрыв серий
