# E09_executor_loss

## Сценарий

Зарегистрировать executor, истечь lease и отправить wake.

## Проверки

- Executor помечен недоступным
- Wake вернул executor_unavailable
- Эффект помечен retired

## Негативный контроль

Эффект продолжает ждать мёртвого executor.
