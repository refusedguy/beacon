# R06_cancel_visible

## Сценарий

PRState возвращает проверку со state CANCELLED.

## Проверки

- вердикт retry
- failedChecks не пустой
- запись имеет conclusion CANCELLED

## Негативный контроль

SUCCESS даёт ready и пустой failedChecks.
