# W23_merge_driver_config

## Сценарий

Конфиг задаёт merge.driver=github-auto-merge и merge.onlyAuthor. Другой конфиг задаёт тот же драйвер вместе с непустым merge.waitFor.

## Проверки

- PR с автором из onlyAuthor уходит в драйвер github-auto-merge
- PR с чужим автором остаётся в драйвере beacon
- комбинация github-auto-merge и waitFor отклоняется с путём поля
- конфиг без новых полей даёт driver beacon

## Негативный контроль

Разрешить github-auto-merge вместе с waitFor. Условие Watch из ADR 0072 теряется, и прогон обязан упасть.
