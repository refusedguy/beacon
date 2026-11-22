# bcx.json: финальная синхронизация BCX-05 и BCX-17

## Две строки изменений

- BCX-05. `status` переведён с todo на dropped, добавлен `note` со ссылкой на PR #1471. Карточка `docs/tasks/BCX-05.md` содержит `Статус: dropped`, коммит `92ea90a7` влит в main (`merge-base --is-ancestor` exit 0), индекс ADR удалён в IDX-11.
- BCX-17. `status` остаётся todo, добавлен `note` о ожидании PR #1405. `gh pr view 1405 --json state` вернул OPEN, оснований для закрытия нет.

## Проверки

- `python3 -m json.tool docs/research/batches/bcx.json` прошёл, JSON валиден.
- `python3 docs/research/batches/fix_bcx.py` вывел `cards=82 missing=0 directory_refs=0 overlap_groups=0`.
- Распределение статусов до правки: 79 done, 2 todo, 1 dropped. После правки: 79 done, 1 todo, 2 dropped.
- Поля `id`, `owner_files`, `type`, `phase`, `deps` не изменились ни у одной из 82 карточек. Изменены только BCX-05 и BCX-17.
- `internal/infra/batchapply/apply.go` допускает `dropped` в `allowedCardStatuses`. Поле `note` отсутствует в structs и игнорируется при `json.Unmarshal`, поскольку `DisallowUnknownFields` не применяется.
