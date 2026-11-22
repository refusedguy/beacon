# FINAL-STATE: верификация main после цикла мержа

Дата: 2026-11-22. Проверку выполнил VERIFY-агент, сессия `ses_dfe43cf91ffexV8mzhWs6kf1Cr`. Код и карточки не изменялись.

## main

- SHA при первом fetch: `0efa2344ba84ced85d9dae11d63947613259818c`.
- SHA на момент написания отчёта: `8608917dab33456083ef9f510e8b883e1309a02e`.
- main двигался во время проверки. Зафиксированы `0efa2344`, `ca302a57`, `851d533e`, `823d852b`, `346638e9`, `8608917d`.

## Обязательные проверки

Основа: завершённый CI run `44989757531` для SHA `851d533e4f3240d64445a596e3d25997af43024f`. Этот SHA является предком текущего main (`git merge-base --is-ancestor` вернул 0). Все джобы в нём завершились success.

| Проверка | Результат | Где проверено |
| --- | --- | --- |
| build | success | run `44989757531` |
| test | success | run `44989757531` |
| e2e | success | run `44989757531` |
| gates | success | run `44989757531` |
| gates-binary | success | отдельный workflow, run `44989831753`, SHA `823d852b` |
| js-test | success | run `44989757531` |
| registry-check | success | check-runs SHA `851d533e`, отдельный workflow Registry check |
| registry-live | success | run `44989757531` |
| rules-broken | success | run `44989757531` |
| seed-spec | success | run `44989757531` |
| cardindex | success | run `44989757531` |
| docs | на main не запускается | `docs.yml` срабатывает только на pull_request с изменениями docs |

Дополнительно проверены все check-runs для SHA `851d533e`: одиннадцать success, `ci-provider-probe` skipped. Проходов с conclusion failure или timed_out среди обязательных джоб не найдено.

Вердикт: main зелёный по последнему завершённому прогону. Оговорка: для самых свежих SHA на момент отчёта прогоны были in_progress, queued или cancelled без списка джоб. Отмены вызваны частыми пушами в продолжающемся цикле мержа, а не ошибками проверок.

## Распределение статусов bcx.json

| Момент | done | todo | dropped |
| --- | --- | --- | --- |
| SHA `0efa2344`, первый fetch | 79 | 2 | 1 |
| SHA `8608917d`, момент отчёта | 79 | 1 | 2 |

На первом fetch распределение совпало с ожидаемым: todo `BCX-05`, `BCX-17`; dropped `BCX-10`.

После влития PR `#1480` (`346638e9`) карточка `BCX-05` получила dropped и распределение стало 79 / 1 / 2. PR `#1405` (`823d852b`) влит, но `BCX-17` в bcx.json остался todo. Оба расхождения между bcx.json и карточками требуют синхронизации отдельным проходом.

## Валидность research-батчей

- `python3 -m json.tool`: `audit-01-batch.json`, `audit-02-batch.json`, `audit-03-batch.json`, `audit-04-batch.json`, `audit-05-batch.json`, `batches/bcx.json` прошли разбор.
- `python3 docs/research/batches/fix_bcx.py`: `cards=82 missing=0 directory_refs=0 overlap_groups=0`, код выхода 0.

## Открытые PR

| PR | headSHA | Проверки |
| --- | --- | --- |
| `#1482` | `1154cd6a24d83d1946eaf0d06c402dffb7c0442b` | `docs` fail, остальных проверок нет |
| `#1440` | `a91d27c732e6bd470d213f61e16f1e5c4b98c67b` | проверок нет, commit status pending |

PR `#1482`, красная проверка `docs`, job `137108605679`, run `44989951855`. Цитата из лога: `DOC-ADR-STATUS: docs/adrs/0312-mergepolicy-cibridge.md:3: invalid status "default"`, затем `red: 1 finding(s)` и `exit status 1`. Нужно заменить статус в ADR на допустимое значение.

PR `#1440`, ветка `fix/autosp05`, `gh pr checks` сообщает `no checks reported`. Commit status API возвращает `state=pending total=0`. Нужно запушить коммит повторно или перезапустить workflow.

PR `#1481` был открыт в начале проверки, все проверки имели pass, влит как `8608917d` в ходе верификации.

## Оговорки

- VmRSS в этой проверке не измерялся. Это цель будущих измерений, а не результат. Скилл `benchmark-checklist` не применялся, бенчмарки не запускались.
- e2e нестабилен. Три завершённых прогона на предках main упали только по e2e: job `137106314538`, `137102553493`, `137098662438`. Во всех трёх логах одна причина: `Server at http://127.0.0.1:<port> did not provide a compatible V2 health response: UnexpectedStatus: 503`. Проверка помечена `continue-on-error` и не блокирует merge. При необходимости повторить: `gh run rerun --failed`.
- Текущая проверка не запускала `go test`, `go build`, `beacon gates` и `batch-apply`. Все выводы основаны на данных GitHub Actions и содержимом origin/main.
