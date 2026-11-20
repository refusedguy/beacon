# R01_retry_not_rebase

## Сценарий

cibridge publish с verdict=retry, затем один проход orchestrator.Run с Waker.

## Проверки

- есть wake_requested verdict=retry
- нет rebase_requested
- wake_delivered ровно один

## Негативный контроль

verdict=rebase по-прежнему даёт rebase_requested.
