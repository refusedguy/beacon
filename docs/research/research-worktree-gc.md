# Исследование: почему удаляются worktree

Дата: 2026-11-22. Режим: чтение кода и один dry-run. Production Go-код не менялся. Ссылки на строки даны по `origin/main` на коммите `b693e56d`.

## Краткий ответ

Сборщик удаляет worktree, который не заблокирован, не имеет живого Watch с совпадающим HEAD и чист по `git status --porcelain`. Возраста и маркера активного агента в решении нет. Защита идёт только по HEAD SHA из `LiveWatches`.

Демон не запускает GC по таймеру. Он вызывает сборщик при переходе Watch в терминальный вердикт `merged`. Вызов один, но сборщик проходит по всем worktree репозитория. Поэтому detached worktree без Watch, созданный для batch-apply, исчезал при первом же мерже любого PR, пока в нём не было изменений. Причина удаления не в вызове `beacon gc-worktree --apply` из CLI. Такого вызова в демоне нет. Причина в прямом вызове `worktree.Collect` с рабочим git runner.

## 1. Алгоритм решения

Команда регистрируется в `cmd/beacon/gc.go:475-477` и вызывается из таблицы команд в `cmd/beacon/main.go:275`. Флаг `--apply` по умолчанию выключен, `cmd/beacon/gc.go:595`. Цикл удаления находится в `cmd/beacon/gc.go:620-639`. Без флага команда только печатает план, `cmd/beacon/gc.go:611-618`.

| Проверка | Где | Результат |
| --- | --- | --- |
| Главный worktree исключён | `internal/infra/worktree/gc.go:42-55` | `List` отбрасывает первый porcelain-блок, поэтому корень репозитория не кандидат |
| Worktree заблокирован | `cmd/beacon/gc.go:531-532`, `internal/infra/worktree/gc.go:194-196` | `kept`, причина `locked`. Проверка идёт первой |
| Живой Watch с совпадающим HEAD | `cmd/beacon/gc.go:482-500`, `:506-522`, `:533-534`, `internal/infra/worktree/gc.go:201-203` | `kept`, причина `active_watch:<id>` |
| Ошибка `git status` | `cmd/beacon/gc.go:535-536`, `:563-565`, `internal/infra/worktree/gc.go:205-209` | `kept`, причина `status_error` |
| Непустой `git status --porcelain` | `cmd/beacon/gc.go:537-538`, `internal/infra/worktree/gc.go:89-103`, `:210-212` | `kept`, причина `dirty` |
| Пустой статус, нет Watch, нет lock | `cmd/beacon/gc.go:539-541`, `internal/infra/worktree/gc.go:214-219` | `remove` в dry-run, `removed` после apply |
| Возраст worktree | `cmd/beacon/gc.go:528-544`, `internal/infra/worktree/gc.go:176-221` | Условия возраста нет вообще |
| Маркер активного агента | Там же | Условия маркера нет |
| Защита корня проекта | `cmd/beacon/mcp.go:1918-1924` | Есть только вdaemon-пути. CLI-путь в `cmd/beacon/gc.go:549-569` не защищает корень |
| Удаление | `internal/infra/worktree/gc.go:105-120` | `git worktree remove -- <dir>` без `--force` |

`LiveWatches` возвращает три статуса: `watching`, `notifying`, `blocked` (`internal/infra/store/db.go:649-655`). Закрытый Watch в защите не участвует. Сопоставление идёт по префиксу HEAD SHA (`cmd/beacon/gc.go:506-522`), а не по имени ветки и не по каталогу.

## 2. Кто вызывает GC и как часто

Порт объявлен в `internal/infra/cibridge/bridge.go:194` и устанавливается через `WithWorktreeGC` в `internal/infra/cibridge/bridge.go:690`. Точка сборки связывает порт с `worktree.Collect` в `cmd/beacon/mcp.go:1899-1930` и подключает его в `ReconcilePass` в `cmd/beacon/mcp.go:1956`.

Единственный вызов порта находится в `collectWorktrees` (`internal/infra/cibridge/verdict_applier.go:546-560`). Функция вызывается из `closeWatch` только при вердикте `merged` и только после успешного закрытия карточки (`internal/infra/cibridge/verdict_applier.go:579-582`). Для `closed`, `blocked`, `ready` и `watching` GC не вызывается. Проверка `Revision` в `internal/infra/cibridge/verdict_applier.go:565-568` даёт один вызов на Watch.

`ReconcilePass` крутится в `ReconcileLoop` (`cmd/beacon/mcp.go:1977-1995`). Интервал берётся из `github.Interval`: 10 s после недавнего изменения Watch, 30 s в тишине, умножение на три при исчерпании бюджета (`internal/infra/github/tier1.go:25-33`, `:39-47`). Есть также одноразовые проходы `beacon daemon --once` (`cmd/beacon/daemon.go:396`) и `beacon up --once` (`cmd/beacon/up.go:222`). Они тоже могут вызвать GC, если в этом проходе есть Watch с вердиктом `merged`.

Автоматического вызова `beacon gc-worktree --apply` из демона, скриптов или workflow нет. Единственный автоматический путь удаления это `worktree.Collect` в `cmd/beacon/mcp.go:1924`. У этого пути нет dry-run и нет флага `--apply`: он удаляет сразу.

## 3. Замер

Dry-run `beacon gc-worktree --format json` в общем дереве 2026-11-22: 318 worktree в отчёте, 11 кандидатов на удаление, 303 locked, 4 dirty. Кандидатами были в том числе research-worktree в `/tmp/opencode/dir2-memory` и `/tmp/opencode/research-cli-ux`. Числа движутся, потому что другие агенты создают и блокируют worktree параллельно. Повторный `git worktree list --porcelain` через несколько минут дал 313 worktree, из них 308 locked и 10 detached.

Dry-run использует те же правила, что и демон. Разница в том, что CLI не защищает корень и не имеет автоматического режима. Блокировка (`git worktree lock`) сейчас основной рабочий способ удержать worktree. Сборщик уважает её в первом же условии.

## 4. Сценарии потери работы

Сценарий A, detachment без Watch. Агент создаёт worktree через `git worktree add --detach` и делает checks out коммит. Watch ещё не зарегистрирован или зарегистрирован для другого PR. Worktree чистый и незаблокированный. Любой `merged` Watch в другом PR запускает `collectWorktrees`, сборщик видит пустой `watchID`, пустой `status` и удаляет каталог. Это точная причина исчезновения detached worktree из условия задачи.

Сценарий B, незакоммиченная работа с исключениями. Обычные правки попадают в `git status --porcelain` и защищены причиной `dirty`. Защита не покрывает файлы, игнорируемые `.gitignore`, и не покрывает уже закоммиченные, но не запушенные коммиты. После локального коммита HEAD worktree уходит вперёд от `HeadSHA`, записанного при создании Watch (`internal/infra/mcp/tool_watch_pr.go:429`, head берётся из `resolveHead` в `:405`). Сопоставление в `cmd/beacon/gc.go:506-522` не находит совпадения, статус чистый, worktree удаляется вместе с коммитом. Агент теряет работу, хотя Watch жив.

Сценарий C, смена HEAD PR. Демон закрывает Watch при смене head (`internal/infra/cibridge/bridge.go:878-881`) и создаёт successor с новым SHA. Worktree остаётся на старом коммите, поэтому перестаёт совпадать с живым Watch. Следующий `merged` убирает его как чистый.

Сценарий D, гонка без второй проверки. `Status` и `Remove` идут отдельными вызовами (`internal/infra/worktree/gc.go:205-219`). `Remove` не применяет `--force`, поэтому git refuse удалить грязный каталог в момент вызова. Повторной проверки перед удалением нет. ADR 0050 требует переименовать кандидата, записать удаление и проверить состояние второй раз (`docs/adrs/0050-six-features-from-ah-wave.md:17`, `:31`, `:46`). Текущий код этого не делает.

Риск для активного агента без Watch высокий. Риск для агента с Watch ограничен сценариями B и C. `AGENTS.md` в разделе «Перезапуск после смерти» прямо говорит, что работа в worktree не теряется. Текущий сборщик это обещание не выполняет для чистых worktree без совпадающего Watch.

## 5. Таблица разрывов

| ID | Приоритет | Разрыв | Доказательство |
| --- | --- | --- | --- |
| G1 | P0 | GC проходит по всем worktree при закрытии одного Watch | `internal/infra/cibridge/verdict_applier.go:546-560`, `:579-582`, `cmd/beacon/mcp.go:1924` |
| G2 | P0 | detached worktree без Watch и без маркера удаляется при первом же мерже | `cmd/beacon/gc.go:528-544`, `internal/infra/worktree/gc.go:201-219` |
| G3 | P0 | Защита привязана к HEAD SHA и ломается при локальном коммите или force-push | `cmd/beacon/gc.go:506-522`, `internal/infra/mcp/tool_watch_pr.go:405`, `:429`, `internal/infra/cibridge/bridge.go:878-881` |
| G4 | P1 | Нет маркера активного агента. Реестр привязок в `cmd/beacon/agents.go:854` и `:1022-1033` сборщик не читает | `cmd/beacon/gc.go:528-544`, `cmd/beacon/agents.go:854`, `:1022-1033` |
| G5 | P1 | Нет отсрочки и второй проверки перед удалением, вопреки ADR 0050 | `internal/infra/worktree/gc.go:205-219`, `docs/adrs/0050-six-features-from-ah-wave.md:17` |
| G6 | P1 | CLI-путь не защищает корень проекта, в отличие от daemon-пути | `cmd/beacon/gc.go:549-569`, `cmd/beacon/mcp.go:1918-1924` |
| G7 | P2 | Нет eval, который воспроизводит исчезновение detached worktree | Отсутствие теста в `evals/`, поведение воспроизведено только dry-run |

Приоритеты P0 и P1 поставлены по размеру потери. G1, G2 и G3 теряют закоммиченную работу без предупреждения. G4 и G5 закрывают те же случаи на уровне политики и больше не зависят от точного совпадения SHA. G6 требует правки файла, которым владеет MEM-05, поэтому в батч взята только проверка через новый файл.

## 6. Сверка с MEM-05, MEM-07, AUD-04 и audit-02-memory

`docs/research/audit-02-memory.md:31` говорит, что автоуборка уже подключена и не требует новой карточки. MEM-07 в `docs/research/audit-02-batch.json` имеет статус `dropped` и прямо запрещает писать второй сборщик (`internal/infra/worktree/gc.go` в OwnerFiles). Настоящее исследование не предлагает второй сборщик. Оно предлагает добавить условия защиты перед существующим `Collect` и ограничить область вызова. Для dropped-карточки это не дубль, а исправление её premise. MEM-05 владеет `cmd/beacon/gc.go` и `cmd/beacon/gc_test.go` и занимается удалением сессий закрытых Watch. Карточки WG не трогают эти файлы.

AUD-04 владеет `cmd/beacon/mcp.go`, `cmd/beacon/mcp_test.go`, `cmd/beacon/registry_list.go` и `cmd/beacon/catalog.go` по тексту карточки `docs/tasks/AUD-04.md` в ветке `fix/audit-04-protocols`. Карточки WG не назначают эти файлы себе. Если реализация потребует правки `mcp.go`, владелец AUD-04 делает её своей карточкой. WG-карточки готовят фильтр, маркер и eval в новых файлах и указывают зависимость от AUD-04 там, где нужна проводка порта.

Пересечение OwnerFiles с MEM-05, MEM-07 и AUD-04 равно нулю. Все файлы WG-карточек новые, за исключением `cmd/beacon/agents.go`, который не входит в OwnerFiles этих трёх карточек.

## 7. Карточки батча

Батч лежит в `docs/research/batches/research-worktree-gc-batch.json`. Карточки WG-01 и WG-02 закрывают P0-разрывы. WG-03 добавляет маркер при создании worktree. WG-04 добавляет отсрочку и вторую проверку. WG-05 закрепляет поведение eval. OwnerFiles не пересекаются с MEM-05, MEM-07 и AUD-04. Eval проверяет, что чистый detached worktree переживает мерж чужого Watch, что локальный коммит не делает worktree кандидатом и что маркер блокирует удаление.

## 8. Ограничения

Тесты, сборки и гейты не запускались по заданию. Dry-run запускался бинарником `.opencode/bin/beacon` из общего дерева. Логика worktree GC в нём совпадает с `origin/main`, но соседние части GC отличаются, поэтому числа из dry-run относятся к моменту замера, а не к зафиксированному коммиту. Удаление ни в одном виде не выполнялось.

## 9. Аудит перед чисткой

Политика аудита записана в `AGENTS.md`, раздел «Аудит перед чисткой», и в `docs/templates/orchestrator-task.md`, поле «Закреплённые worktree». Это исследование не меняет механизм GC, разобранный в разделах 1 и 2.

Аудит работает только на чтение. Он берёт список worktree из `git worktree list --porcelain` и классифицирует каждый путь по размеру, возрасту, состоянию мержа, незакоммиченным правкам и состоянию PR. Скрипт аудита только читает состояние и не содержит команд удаления. Его результат это совет, а не разрешение на удаление. Закреплённый чат или активная сессия побеждает метку `safe` и останавливает чистку.

Отдельный случай это `wip:N`. Метка означает N незакоммиченных правок. Их надо показать и получить решение владельца до любой чистки. Сборщик из раздела 1 видит только `git status --porcelain` и не различает закреплённые чаты. Аудит добавляет проверку, которая не зависит от совпадения HEAD SHA с живым Watch.

Основание: `pstack/skills/poteto-mode/playbooks/worktree-cleanup.md:3-7` и `pstack/skills/poteto-mode/scripts/worktree-audit.sh:1-5`, `:30-48`. У pstack один аудиторский скрипт с тремя входными источниками и пять шагов проверки перед удалением. У beacon до карточки CP-12 аудиторского скрипта в дереве не было.
