# Разрывы в тестовом покрытии критичных пакетов

Дата: 2026-11-22. Режим: чтение. Код, тесты, сборки и гейты не запускались. Источники: `git ls-tree`, `git grep` и чтение файлов на базе `origin/main` `2d34645b`. Пути `rg` и `find` по `/mnt/projects`, `/tmp` и `/home` не применялись.

## 1. Что посчитано

Список `.go` файлов в `internal/app`, `internal/infra` и `cmd/beacon` снят через `git ls-tree -r --name-only origin/main`. Всего 206 production-файлов. Для каждого каталога посчитаны production-файлы и файлы `*_test.go`.

Пакеты без единого теста:

| Пакет | Production-файлы | Комментарий |
| --- | --- | --- |
| `internal/infra/hookrun` | `hookrun.go` | Используется `taskcli.Service.RunHooks` (`internal/app/taskcli/service.go:138`). Тест не мешает запуску хуков. |
| `internal/infra/tmux` | `pane.go` | На `origin/main` нет ни одного production-вызова пакета. Поле `Runner.Command` объявлено «для подмены в тестах», но не используется. |

Пакеты с одним тестовым файлом на большой набор production-кода:

| Пакет | Production | Тесты | Оценка |
| --- | --- | --- | --- |
| `internal/app/taskcli` | 11 файлов, `service.go` на 2908 строк | `service_test.go`, 90 тестов | Плотность тестов высокая. Разрыв точечный, в пречек-журнале и проверке связи коммита. См. TG-06. |
| `internal/app/prcreate` | 4 файла, `service.go` на 369 строк | `prcreate_test.go`, 13 тестов | Конструктор, провайдеры и `Run` покрыты. Разрыва не найдено. |

Отдельно проверены файлы без одноимённого `*_test.go`. Таких 39. Большинство покрыто тестами соседних файлов. Разрывы, которые остались после этой проверки, собраны в разделе 3.

## 2. Три недавние ошибки и тесты, которые их не поймали

| Ошибка | Что сломалось | Был ли до исправления тест | Статус теста сейчас |
| --- | --- | --- | --- |
| #1619, `059bbcd4`, `verdict_applier.go` | На вердикте `watching` проверки и merge-state не попадали в store, Watch оставался с `checks=null` | Нет. Единственный тест файла появился вместе с исправлением | `verdict_applier_test.go:30` проверяет запись и идемпотентность. Дыра закрыта. |
| #1620, `ac4eb527`, `redaction.go` | Регулярное выражение Telegram-токена съедало PR и SHA внутри watch ID, `beacon_get_result` отвечал `WATCH_NOT_FOUND` | Нет. Тест добавлен тем же коммитом | `redaction_test.go` содержит 34 строки новых случаев. Дыра закрыта. |
| #1646, PR открыт, `verdict_applier.go` | Информационная проверка `required: false` попадала в `BlockedInfo.FailedChecks`, подпись `BlockerIncident` и текст wake. Два Watch с одинаковой обязательной и разной информационной ошибкой создавали два инцидента | Нет | `blocked_informational_test.go` есть только в открытом PR #1646, в `origin/main` его нет. См. TG-01. |

Итог: #1619 и #1620 закрыты тестами в тех же коммитах. #1646 остаётся дырой на базе `2d34645b`. В `wake_dispatcher.go:810` функция `blockingFailedNames` уже отфильтровывает информационные проверки для текста wake после #1643, но путь block её не вызывает.

## 3. Находки

Приоритет оценён по ущербу от регрессии. P0 означает потерю корректного решения или destructive-операцию без проверки.

| ID | Приоритет | Место в production | Почему теста нет | Какой тест предотвратил бы регрессию |
| --- | --- | --- | --- | --- |
| TG-01 | P0 | `internal/infra/cibridge/verdict_applier.go:714` (`applyBlocked`), `:718` (`realFailedNames`), `:760` (`blockedNotice`), `:905` (`handleBlocked`) | `verdict_applier_test.go` содержит один тест только для `VerdictWatching`. Тесты block в `bridge_test.go:460` и `:529` сеют уже заблокированный Watch через `blockedTestWatch`, поэтому свежий `ciObservation` в `applyBlocked` не попадает ни в один тест | Прогнать `applyVerdict` с `VerdictBlocked` и obs `ci-build` FAILURE required плюс `ci-info` FAILURE optional. Проверить `BlockedInfo.FailedChecks == ["ci-build"]`, один вызов `GetOrCreateOpenBlocker`, текст wake без `ci-info`. Второй тест: два Watch с одинаковой обязательной и разной информационной ошибкой дают один инцидент. |
| TG-02 | P0 | `internal/infra/worktree/gc.go:42` (`List`), `:59` (`parsePorcelain`), `:149` (`WatchIDForHead`), `:176` (`Collect`) | В пакете есть `grace_test.go`, `headguard_test.go`, `marker_test.go`. Ни один не вызывает `parsePorcelain`, `WatchIDForHead` и `Collect`. Исправления WG-02 (#1529) и WG-04 (#1528) добавили тесты к новым файлам `headguard.go` и `grace.go`, оставив `gc.go` без тестов | Подменить `Runner` и отдать porcelain с главным worktree, locked, dirty, чистым с живым head, чистым с мёртвым head и защищённым каталогом. Проверить, что `Collect` удаляет только один кандидат и возвращает причины для остальных. Отдельно проверить `parsePorcelain` на detached, locked и неизвестных ключах и стабильный выбор `WatchIDForHead` при коротком SHA. |
| TG-03 | P1 | `internal/infra/store/failure_excerpts.go:42` (`Get`), `:72` (`Put`), `:102` (`boundExcerpt`) | Ноль ссылок на файл во всех `*_test.go` пакета `store`. Исправление #1617 (EH-02) добавило тесты только в MCP-слое | Проверить miss, запись и чтение, отказ второго `Put` перезаписать первый ответ, miss при новом head SHA, кэширование пустой строки, обрезку `boundExcerpt` по границе UTF-8 на 2048 байт и безопасность nil-приёмника. |
| TG-04 | P1 | `internal/infra/webhook/signature.go:53` (`ReadBody`) | `VerifySignature` и `LoadSecret` покрыты в `workflow_run_test.go` и `receiver_test.go`. `ReadBody` не вызывается ни одним тестом, хотя `workflow_run.go:180` использует его как единственную защиту от bodies больше 25 МБ | Проверить проход ровно `limit`, отказ при `limit+1` с `ErrBodyTooLarge`, дефолт `MaxBodyBytes` при неположительном лимите, `nil` reader и обёртку ошибки чтения. |
| TG-05 | P2 | `internal/infra/hookrun/hookrun.go:13` (`Run`) | Пакет не содержит ни одного `*_test.go`. Вызывается из `taskcli.Service.RunHooks`, где тесты подменяют сам хук, а не запуск процесса | Проверить, что `Run` передаёт stdin и возвращает combined stdout/stderr. Для устойчивости использовать helper-процесс через `os.Args[0]`, а не внешний shell. Отдельно проверить передачу ошибки при ненулевом коде выхода. |
| TG-06 | P2 | `internal/app/taskcli/service.go:2376` (`RecordPrecheckFailure`), `:2401` (`PrecheckLog`), `:2902` (`CheckCommitLinkage`) | В `service_test.go` 90 тестов, но ни один не упоминает эти символы. Журнал handoff и fix-checks покрыты, precheck-журнал нет | Проверить отказ пустой причины, запись записи с `outcome=precheck_failure` и UTC-временем, копию среза из `PrecheckLog`, `LinkageEmpty` для `chore` и делегирование в `CheckLinkage` для `feat`/`fix`. |
| TG-07 | P3 | `internal/infra/tmux/pane.go:23` (`Runner.Run`) | Пакет без тестов. Поле `Runner.Command` создано для подмены в тестах, но не используется. В production пакет не вызывается | Либо удалить пакет как мёртвый, либо добавить тест с подставным `Command`: проверить передачу `Binary`, аргументов и возврат stdout. Приоритет низкий, пока нет вызывающего кода. |

## 4. Что проверено и дырой не считается

`internal/infra/cibridge/` содержит 14 тестовых файла. Пути ready, merged, merge-hold, wake-cap, breaker, park, orphan и in-work stall имеют прямые или косвенные тесты в `bridge_test.go`, `w2a_test.go`, `chaos_test.go`, `in_work_stall_test.go`, `terminal_confirmation_test.go`. Осталась только дыра TG-01.

`internal/app/orchestrator/decision.go` на 654 строки имеет `decision_test.go` на 960 строк и 26 вызовов `Decide`. Внутренние helpers `decisionVerdict`, `decisionHoldReason` и `unsatisfiedRefs` не упоминаются в тестах по имени, но вызываются через `Decide`, то есть покрыты.

`internal/infra/taskstore/` и `internal/infra/store/gc.go` получили тесты в последних исправлениях: #1642 добавил 54 строки в `gc_test.go`, #1656 ещё 149. Три текущих теста `GCObligations` закрывают live retry и перепроверку кандидата в транзакции.

`internal/infra/mcp/` не имеет инструментов без одноимённого теста после #1638. `tool_task_close_test.go` на 513 строк появился вместе с закрытием ATX-M01.

`internal/infra/webhook/signature.go` проверка HMAC имеет control vector из GitHub и cases с кривым digest.

## 5. Карточки

| Карточка | Приоритет | Файл |
| --- | --- | --- |
| docs/tasks/TG-01.md | P0 | `internal/infra/cibridge/verdict_applier.go` |
| docs/tasks/TG-02.md | P0 | `internal/infra/worktree/gc.go` |
| docs/tasks/TG-03.md | P1 | `internal/infra/store/failure_excerpts.go` |
| docs/tasks/TG-04.md | P1 | `internal/infra/webhook/signature.go` |
| docs/tasks/TG-05.md | P2 | `internal/infra/hookrun/hookrun.go` |
| docs/tasks/TG-06.md | P2 | `internal/app/taskcli/service.go` |
| docs/tasks/TG-07.md | P3 | `internal/infra/tmux/pane.go` |

Карточки описывают недостающий тест, а не переписывание кода. Где исправление ещё не в main, это указано в контексте карточки.

## 6. Что не проверялось

Тесты, сборка, гейты, CLI Beacon и ручной merge не запускались. Числа строк и номера строк сняты чтением файлов на базе `2d34645b`. PR регистрируется через `beacon_watch_pr` с `autoMerge: true`, решение о мерже принимает Beacon.
