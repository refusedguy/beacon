# R03_plain_vs_blocked_ack

## Сценарий

Watch в notifying с verdict=retry и обязательством с readAt и claimedAt.

## Проверки

- plain beacon_ack оставляет verdict=retry
- blocked:not_mine ставит статус blocked
- после blocked нет wake_requested

## Негативный контроль

устаревший revision не блокирует Watch.
