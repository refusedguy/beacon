# AP05_orchestrator_idempotency

## Сценарий

Повторить регистрацию с тем же watchID, revision и sessionID.

## Проверки

- одна запись registry
- одна квитанция
- SlotsUsed не вырос

## Негативный контроль

Повтор создаёт вторую подписку.
