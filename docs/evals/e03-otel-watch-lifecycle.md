# E03_otel_watch_lifecycle

## Сценарий

Полный цикл Watch: регистрация, вердикт ready из CI, wake, мерж и повторная доставка.

## Проверки

- watch_registered=1 и verdict_published=1 с верным verdict
- wake_delivered и adapter_errors не смешиваются
- merge_attempt=skipped при повторе WasMerged
- rebase_requested содержит ConflictType=merge

## Негативный контроль

Повторная доставка того же webhook не удваивает счётчики.
