# R04_no_orchestrator_subscriber

## Сценарий

Eight executor-only Watches, из них один с failedChecks вне diff.

## Проверки

- handleBlocked не находит получателя и логирует skip
- blocked:not_mine всё равно убирает Watch из ActiveWatches
- демон перестаёт будить

## Негативный контроль

с orchestrator-подписчиком wake FIX-агента доставляется.
