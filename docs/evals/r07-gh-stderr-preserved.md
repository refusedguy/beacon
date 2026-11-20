# R07_gh_stderr_preserved

## Сценарий

Поддельный gh возвращает exit status 1 с текстом API rate limit exceeded.

## Проверки

- ошибка провайдера содержит этот текст
- provider_call логирует method
- есть таймаут вызова

## Негативный контроль

успешный вызов не порождает warn.
