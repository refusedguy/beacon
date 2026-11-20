# E08_metrics_stay_low_cardinality

## Сценарий

Проген с десятью сессиями и двадцатью вотчинами.

## Проверки

- в beacon metrics --format prometheus нет меток sessionID и watchID
- provider_call_total размечен adapter, method и outcome
- retry_open_duration_seconds имеет корзины и sum

## Негативный контроль

Метка с sessionID в любой метрике ломает тест.
