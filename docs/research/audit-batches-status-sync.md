# Синхронизация статусов research-батчей аудита с origin/main

Дата: 2026-11-22. Проверено против `origin/main` на коммите `5b1f470d45446f736a90cba9035b9097111750be`.

Карточки с уже влитым кодом получили `status: done`. В поле `note` записан PR, полный SHA и результат `git merge-base --is-ancestor <sha> origin/main`. Поле `note` не входит в структуру `card` в `internal/infra/batchapply/apply.go`, поэтому валидатор его игнорирует. Остальные поля карточек не менялись.

## Распределение до и после

| Батч | todo до | done до | dropped | todo после | done после | Переведено в done |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| audit-01-batch.json | 12 | 0 | 0 | 2 | 10 | 10 |
| audit-02-batch.json | 7 | 0 | 1 | 1 | 6 | 6 |
| audit-03-batch.json | 9 | 0 | 3 | 3 | 6 | 6 |
| audit-04-batch.json | 7 | 0 | 1 | 0 | 7 | 7 |
| audit-05-batch.json | 3 | 0 | 0 | 0 | 3 | 3 |
| Итого | 38 | 0 | 5 | 6 | 32 | 32 |

## Карточки, переведённые в done

Для каждой карточки ниже указан мерж-коммит в `origin/main`. `merge-base --is-ancestor` вернул 0 для каждого SHA.

| Карточка | PR | SHA | Основание |
| --- | ---: | --- | --- |
| AUTOSP-01 | 1448 | `e724f0eca86582a9f2353257b06293c18023af89` | Проверка MergePolicy перед applyMerge в verdict_applier.go |
| AUTOSP-02 | 1441 | `95c5bd42b7e4df8bf0091df2aded8d2b9bc5f711` | Prompt с resume и delivery в opencode-клиенте |
| AUTOSP-03 | 1435 | `63aada03c1fa287a46c9c4382de47769982c296b` | gemini зарегистрирован в acpRuntimes |
| AUTOSP-06 | 1442 | `f9c481cd4187010b764ff067cc3cda4b1480908c` | Единый гейт допуска в taskcli service |
| AUTOSP-07 | 1439 | `ca302a57bb646a4bfb819be320e88ff506c442ad` | Слот гаснет после проверки артефакта |
| AUTOSP-08 | 1438 | `4c4714ae38b15aac02664c568eeb39eff49c3a93` | fetch перед worktree add в Go-пути |
| AUTOSP-09 | 1436 | `d0d9fc6fbf103972a8730de75f7b6116797c655f` | Явная связь cardID и watchID в store |
| AUTOSP-10 | 1478 | `0ef4bf205960391caa95d6f75a540280abc2eb31` | Eval автоспавна без ручного пинка |
| AUTOSP-11 | 1437 | `95843cb669ef62ac7214dc3ff01f9bb84e0818e5` | CheckLaunchFootprint и LaunchDecision в metrics.go |
| AUTOSP-12 | 1447 | `d03c77f3897453931e5cfe45bb54c5c7e09e7078` | Решения и владельцы чисел аудита 01 зафиксированы |
| MEM-01 | 1462 | `9e5d3b91d0f9bb15ff090c711bae7beada07ae3f` | DeleteSession в HTTP-клиенте opencode |
| MEM-02 | 1588 | `5b1f470d45446f736a90cba9035b9097111750be` | beacon watch читает сессии через newReadOnlyOpencodePort |
| MEM-03 | 1455 | `254267c2394dfdb4bc2eeb772ddeaa791b15c989` | PromptWithResumeFunc и CombinedOutput; продолжение в PR 1473, `55e88825d2a9d0ddfce877e3c4280d48bbab5833` |
| MEM-05 | 1444 | `b1c647615f0d17bafa30899ad4b77594443aef48` | GC удаляет сессии закрытых Watch |
| MEM-06 | 1465 | `5597372fac5c26150888eb4f627a93dfd19e16ac` | ReapStaleCards в taskcli service |
| MEM-08 | 1451 | `8fc00ad63dc2997b062d01cd99beb548e38a0074` | Ранбук наблюдаемости описывает бюджет RSS и GC |
| TUI-02 | 1446 | `ce5df17be03ad85b332170bdcf1b18d636fe273d` | Единая Bubble Tea модель по снимку и карточкам |
| TUI-03 | 1453 | `70f11147cacd980a33a56f709fdbcc2ea1ff1461` | Один контракт проекции, budget и `?replay=64` |
| TUI-05 | 1454 | `01816965bfae75bd93da91cd75d8e3622302adaa` | Восемь verbs human-decide на сокете управления |
| TUI-06 | 1111 | `9becefcca0086917650d757b0151fc33aa4c0bbd` | Ребро cmd к internal/tui, фикстура и пересобранный бинарник; WIRING-915 в статусе done |
| TUI-07B | 1476 | `536fb18494db479c47a25abb17b277fab89ed73d` | Eval с замерами VmRSS и фактом отсутствия Node.js |
| TUI-08 | 1452 | `4284487a1a89575074b5c97d07a9f5dc6d0ae05e` | Термины в GLOSSARY и premise в WIRING-915 |
| PROTO-01 | 1443 | `9483e105fb8c8aec93e9e3ac17436b7f10080086` | Один каталог имён в контракте и e2e |
| PROTO-02 | 1445 | `554e2306706bca76cc521399d45112b0707473a8` | Типизированная SSE-обёртка событий |
| PROTO-03 | 1481 | `8608917dab33456083ef9f510e8b883e1309a02e` | Аутентифицированный /mcp на одном HTTP-сервере |
| PROTO-04 | 1463 | `fd7286b0cb31c6485e49932d9d2501d6c769e805` | Разделение каналов в шаблонах агентов |
| PROTO-05 | 1464 | `aeb250dac07b422e3bdf8be1e89814757217045b` | Eval дрейфа каталога и размера tools/list |
| PROTO-07 | 1457 | `058f943014719b3daf64b4479398feeffbe21068` | GraphReadySignal в tool_stats.go; карточка задач done через PR 1562, `fdaadff52848dc9f324fdc055237e023462b2acd` |
| PROTO-08 | 1449 | `daa93aa5cd82ccea3e688e299ecf2f7e7d692c1b` | registryListSource привязан к текущему проекту; карточка задач done через PR 1562 |
| INTG-01 | 1450 | `851d533e4f3240d64445a596e3d25997af43024f` | serverRunner зарегистрирован в runnerFor и каталоге |
| INTG-02 | 1456 | `7de845ba304dcd83ff7997603c69f04eafcd282a` | Долговечный GenerationLedger в claim.go; карточка задач done через PR 1562 |
| INTG-03 | 1460 | `f0af1c579b4064d6622c5515b629ebac000b63b8` | CRUD расписаний поверх таблицы schedules |

Замечания по границе карточек. AUTOSP-11 добавил гейт `CheckLaunchFootprint`, но вызов гейта из цикла остаётся задачей MEM-04. INTG-02 и INTG-03 реализовали код в своих OwnerFiles; вызов сеттеров и подмена адаптера в `cmd/beacon` карточками не предусмотрены и остаются отдельной wiring-работой. TUI-06 не существует как отдельный код PR: ребро и фикстуру влил PR 1111 по карточке WIRING-915. MEM-02 влился в main во время работы над отчётом, PR 1588 вошёл в проверку после ребейза. Коммит `d673417c5f92ba3a24cfdde48f924f021387448e` упоминает TUI-07A в связи с разбором ID карточек, но не добавляет тест холодного старта в OwnerFiles TUI-07A, поэтому карточка остаётся в todo.

## Карточки, оставшиеся в todo

| Карточка | Причина |
| --- | --- |
| AUTOSP-04 | В cmd/beacon/mcp.go нет auto-spawner. Влитого PR с этим кодом нет. |
| AUTOSP-05 | В tool_task_claim.go нет выбора по фазе и графу. Найденный коммит `3541a1a9cb627` не является предком origin/main. |
| MEM-04 | В cmd/beacon/mcp.go и daemon.go нет вызовов ArchiveRun, Checkpoint и CheckLaunchFootprint. |
| TUI-01 | Флага --live в cmd/beacon/board.go и порта cmd/beacon/tui.go нет. |
| TUI-04 | В httpapi.Snapshot нет полей queue, slots и workers. |
| TUI-07A | Тест холодного старта с replay в internal/tui/client_test.go и cmd/beacon/watch_w8b_test.go отсутствует. |

Карточки MEM-07, TUI-07C, TUI-09, TUI-10 и PROTO-06 уже были в статусе dropped и не менялись.

## Проверки

- `python3 -m json.tool` прошёл на всех пяти файлах.
- Для каждого переведённого в done SHA проверен через `git cat-file -t` и `git merge-base --is-ancestor <sha> origin/main`, результат 0. Номер PR в `note` совпадает с номером в теме мерж-коммита.
- Скрипт санитайзера сравнил каждую карточку до и после. Отличаются только `status` и новое поле `note`. `id`, `title`, `owner_files`, `type`, `phase`, `deps`, `goal`, `steps`, `acceptance` и остальные поля сохранены.
- `id` уникальны внутри каждого батча. Пересечений `owner_files` в фазе санитайзер не создавал, набор путей не менялся.
- Производственный Go-код, карточки в docs/tasks, bcx.json и batch-validation-fixes.json не тронуты.
