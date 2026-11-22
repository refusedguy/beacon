# Eval: контракт сборщика worktree (WG-05)

Три сценария из раздела 4 исследования [research-worktree-gc.md](../../docs/research/research-worktree-gc.md)
закрепляют правила удержания worktree. Eval воспроизводит потерю работы в
временном git-репозитории и показывает, что старые правила действительно
удаляют каталог. Раннер не трогает боевые worktree.

Карточка: [docs/tasks/WG-05.md](../../docs/tasks/WG-05.md). Зависимости: WG-01
(ограничение области сбора), WG-02 (защита по живому Watch при ушедшем HEAD),
WG-03 (маркер активного агента). Eval лежит в отдельном пакете. Он не добавляет
кейсы в `evals/main.go`, поэтому набор AUTOSP-10 не дублируется.

## Запуск

Из каталога `evals`:

```bash
GOPROXY=off /tmp/opencode/go126/bin/go run -tags=eval ./worktree-gc
```

Можно использовать любой Go из `PATH`: `go run -tags=eval ./worktree-gc`.
Единственная зависимость вне стандартной библиотеки — пакет
`internal/infra/worktree` корневого модуля, подключённый через `replace` в
`evals/go.mod`. Сеть не нужна. Файл имеет build tag `eval`, на обычную сборку
он не влияет. `evals/run.sh` этот раннер не вызывает: у сценариев свой вход и
свой вывод.

Раннер не вызывает `os/exec` и файловые операции из стандартной библиотеки:
git идёт через инфраструктурный `worktree.CommandRunner`, а коммиты создаются
пустыми (`--allow-empty`). Это держит eval в слое `outside` без исключений в
гейте capabilities. Временный каталог после прогона остаётся на диске для
осмотра: его удаление потребовало бы `os.RemoveAll`, исключения в
`permanent.json` и пересборки бинарника гейтов.

Код выхода `0` только при четырёх условиях: три сценария `PASS` и негативный
контроль `PASS`. Любой `FAIL` даёт код `1`.

## Ожидаемый вывод

Раннер печатает JSON со сценариями, фактами и сводкой. Успешный прогон:

```json
{
  "generated_at": "2026-11-22T12:00:00Z",
  "fixture": "временный git-репозиторий; runner записывает remove без удаления каталогов",
  "scenarios": [
    {
      "name": "detached-worktree-survives-unrelated-merge",
      "status": "PASS",
      "facts": {"removed": 0, "remove_calls": 0, "reason": "out_of_scope", "dir_exists": true, "closed_pr": 2}
    },
    {
      "name": "local-commit-keeps-worktree",
      "status": "PASS",
      "facts": {"removed": 0, "remove_calls": 0, "reason": "active_watch:watch-pr1", "local_commit_present": true}
    },
    {
      "name": "marker-blocks-collection",
      "status": "PASS",
      "facts": {"fresh_reason": "active_marker", "fresh_removed": 0, "expired_reason": "remove", "expired_removed": 1}
    }
  ],
  "negative_control": {
    "name": "negative_control_old_behavior",
    "status": "PASS",
    "facts": {"detached_removed": 1, "local_commit_removed": 1, "marker_removed": 1}
  },
  "summary": {"passed": 4, "failed": 0}
}
```

Имена сценариев совпадают с полем `evals` в
[research-worktree-gc-batch.json](../../docs/research/batches/research-worktree-gc-batch.json).
Факты `removed`, `remove_calls`, `reason` и `dir_exists` показывают, какое
решение принял сборщик и сохранился ли каталог.

## Сценарии

| Имя | Условие | Ожидаемый факт |
| --- | --- | --- |
| `detached-worktree-survives-unrelated-merge` | Чистый detached worktree без Watch; Watch зарегистрирован для PR 2 и переведён в `merged` | `removed=0`, причина `out_of_scope`; каталог на месте |
| `local-commit-keeps-worktree` | У worktree есть Watch `watch-pr1` с HEAD до коммита; в worktree сделан локальный коммит без push, HEAD ушёл вперёд; собирает другой merged Watch | `removed=0`, причина `active_watch:watch-pr1`, объект локального коммита доступен |
| `marker-blocks-collection` | Чистый worktree без Watch; маркер агента истекает через пять минут | свежий маркер даёт `active_marker` и `removed=0`; просроченный даёт `remove` и `removed=1` |

В карточке для первого сценария перечислены причины `active_marker`,
`active_watch` и `dirty`. Модель принимает любую из них и дополнительно
`out_of_scope`: фильтр WG-01 не пускает чужой worktree в сбор, поэтому это
основная причина удержания в этом сценарии.

Каждый сценарий включает ровно одно новое правило. Так видно, какое решение
отвечает за удержание. В рабочей системе правила действуют вместе.

## Негативный контроль

Тот же набор прогоняется с пустой политикой, то есть с текущими правилами:
lock, совпадение HEAD по префиксу SHA и `git status --porcelain`. Все три
сценария дают `removed=1`. Негативный контроль `PASS` означает, что eval видит
потерю работы со старым поведением. Если старые правила перестанут удалять
каталог, контроль красный и прогон падает. Проверка больше не ловит потерю работы.

## Что не проверяется

Модель `collect` повторяет таблицу решений из раздела 1 исследования, но не
вызывает production-код: защиты WG-01, WG-02 и WG-03 ещё не влиты в
`origin/main`. Это тот же приём, что кейсы W14-W18 в `evals/main.go`: контракт
фиксируется исполняемой моделью, а не заглушкой с заранее верным ответом.
Когда защиты попадут в production, сценарии нужно перевести на реальный
сборщик, сохранив имена и факты.

`git worktree remove` не выполняется. Runner записывает путь в `remove_calls`,
поэтому eval не может удалить worktree вне временного каталога. `dir_exists`
показывает, что каталог остался, а `removed` и `remove_calls` показывают
решение сборщика.

## Измерение

Baseline из раздела 3 исследования, dry-run 2026-11-22: 318 worktree в отчёте,
11 кандидатов на удаление, 303 locked, 4 dirty. Числа движутся, потому что
другие агенты создают и блокируют worktree параллельно. Eval не сравнивает эти
числа: он даёт детерминированное воспроизведение трёх причин потери работы на
временном репозитории.
