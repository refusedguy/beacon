# E10_erasure_ledger_replay

## Сценарий

Применён батч стирания, ledger записан до мутации, база восстановлена из более раннего backup.

## Проверки

- Replay воспроизводит только committed batch
- Scrub не превращается в delete
- Неопределённый батч reported как unresolved

## Негативный контроль

Удалить ledger line: replay не должен угадывать исход батча.
