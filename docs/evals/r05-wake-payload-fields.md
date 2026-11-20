# R05_wake_payload_fields

## Сценарий

Прочитать VerdictRef, который получает исполнитель при verdict=retry.

## Проверки

- в тексте есть verdict
- в тексте есть obligationID
- в тексте есть headSHA и baseRef
- в тексте есть failedChecks или detailsUrl

## Негативный контроль

текущий текст содержит только watchID, pr и revision.
