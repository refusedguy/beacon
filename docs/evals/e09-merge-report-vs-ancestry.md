# E09_merge_report_vs_ancestry

## Сценарий

Строка несёт merged:<PR>; проверяющий видит squash-merge и integration_ref.

## Проверки

- MergeReport помечен как report с recordedAt
- Ancestry кандидата не подтверждает merged
- Решение остаётся indeterminate до проверки ref

## Негативный контроль

Считать тег merged доказательством: тест обязан отклонить такой вывод.
