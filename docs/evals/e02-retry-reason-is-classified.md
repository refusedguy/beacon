# E02_retry_reason_is_classified

## Сценарий

Watch с упавшими чеками gates и test получает verdict retry, другой Watch с mergeStateStatus BEHIND получает rebase.

## Проверки

- в логе первого reasonClass=flaky_check и failedChecks содержат gates
- в логе второго reasonClass=behind_main
- beacon_get_result и лог согласованы по failedChecks

## Негативный контроль

Подстановка reason=retry вместо класса приводит к падению проверки.
