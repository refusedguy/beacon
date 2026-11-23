ARDH-01 | done | # ARDH-01. Свести пять сканеров PRAGMA к одной функции
ARDH-02 | done | # ARDH-02. Выбрать каноническую схему watch_bindings и agent_registry
ARDH-03 | done | # ARDH-03. Вынести проверку флага формата в отдельную функцию CLI
ARDH-04 | done | # ARDH-04. Вынести общий скелет региstrации MCP инструментов
ARDH-05 | done | # ARDH-05. Перенести checkFixture в пакет gate
ARDH-06 | done | # ARDH-06. Убрать дубли данных в шаблоне WIRING-карточки
ARDH-07 | done | # ARDH-07. Свести шесть таблиц MCP к одному дескриптору инструмента
ARDH-08 | done | # ARDH-08. Завести один список полей карточки для taskstore и docsgate
ARDH-09 | done | # ARDH-09. Сделать генератор индекса ADR
ARDH-10 | done | # ARDH-10. Вывести CLI catalog из единого реестра команд
ARDH-11 | done | # ARDH-11. Разделить cibridge.Bridge по ответственностям
ARDH-12 | done | # ARDH-12. Разнести восемь файловых state machine из taskcli.Service
ARDH-13 | done | PR #1174 влит в origin/main, merge commit e9d114010ab244307cce29823431c10faea2c4b5, git merge-base --is-ancestor exit 0. Код и тесты оркестратора в main.
ARDH-14 | done | # ARDH-14. Разделить жирные порты Store и BlockerStore
ARDH-15 | done | # ARDH-15. Убрать импорты infra из слоя app
ARDH-16 | done | # ARDH-16. Упростить мелкие порты и убрать reflection в cibridge
ARDH-17 | done | # ARDH-17. Подключить ToErrorShape или удалить мёртвый конвертер
ARDH-18 | done | # ARDH-18. Сохранить причину ошибки через %w в 35 местах
ARDH-19 | done | # ARDH-19. Сохранить причину внутри AppError без изменения JSON
ARDH-20 | done | # ARDH-20. Проверить проглоченные ошибки в оркестраторе
ARDH-21 | done | PR #1186 влит в origin/main, merge commit ef029392bf9971e6305d3a2beda141377f0f92ad, git merge-base --is-ancestor exit 0. Premise снята CLEAN-02, формат httpapi зафиксирован error_format_test.go.
ARDH-22 | done | # ARDH-22. Убрать ссылку на глобальный срез в ответе MCP
ARDH-23 | done | # ARDH-23. Убрать лишний слой замыканий в тесте watch
AT-02 | done | Реализация уже в main: коммит b740996a7a83f00df40e109ea8a8dc82c1ce73b1 (PR #1811, feat(mcp): paginated failed CI log reader (AT-02)). git merge-base --is-ancestor b740996a7a83f00df40e109ea8a8dc82c1ce73b1 origin/main -> exit 0. В origin/main есть internal/infra/mcp/tool_ci_failed_log.go и tool_ci_failed_log_test.go с CIFailedLogHandler/FailureLog. Дублирование кода не требуется; статус карточки доводится до main docs-only PR.
AT-03 | done | Реализация уже влита в main через PR #1821, коммит bddd03a3ee520e733cf591cf6c384a62e3f2d49f (feat(mcp): add merge status read tool (AT-03)). Проверка git merge-base --is-ancestor bddd03a3ee520e733cf591cf6c384a62e3f2d49f origin/main вернула 0. Файл internal/infra/mcp/tool_merge_status.go присутствует в origin/main. Дубли кода нет, в PR входит только статус карточки с доказательством.
AT-04 | done | Реализация beacon_obligations влита через PR #1819, коммит 26523e6b423af1486adbad492a9c362c9d898eff. git merge-base --is-ancestor 26523e6b origin/main вернул 0. Дубль кода не нужен. Ветка 6b закрывает карточку и несёт статус с доказательством в docs/tasks/AT-04.md.
AT-05 | done | AT-05 уже влит в main через PR #1812, commit 809af588bb6ebd1cea3765c645d95d8b5d4156b6; git merge-base --is-ancestor 809af588 origin/main -> exit 0; реализация internal/infra/mcp/tool_checks_explain.go присутствует в origin/main. Дублирующий код не создаём, PR несёт только статус карточки и доказательство.
AT-06 | done | Реализация влита в main через PR #1828, коммит 1fadd99272f0c866ac9ace7d6e9f9f43c986eacf (feat(mcp): add beacon_rerun_failed tool with budget guard (AT-06) (#1828)). `git merge-base --is-ancestor 1fadd99272f0c866ac9ace7d6e9f9f43c986eacf origin/main` вернул 0. Дубль кода не создаём.
AT-07 | done | Premise не подтвердилась: решения владельца по ADR 0004 нет. git grep по origin/main даёт только docs/tasks/AT-07.md и docs/research/research-agent-tooling-gaps.md, PR #1664 прямо помечает AT-06/07/08 как требующие решения владельца. Авто-rebase уже реализован: internal/infra/cibridge/bridge.go:692 WithAutoRebase, :753 spendRebase, verdict_applier.go:441-470. По приёмке карточки при авто-rebase она закрывается без кода; дубль tool_rebase.go не создаю.
ATX-01 | done | # ATX-01. Убрать гонку JSON.parse квитанции в тесте T3.16 плагина
ATX-02 | done | # ATX-02. Сохранять installedServerEnsurer в t3145RestorePorts
ATX-03 | done | # ATX-03. Убрать опрос лога и TOCTOU порта в e2e старте opencode serve
ATX-04 | done | already landed in PR #1211 ecf63c3a7940
ATX-05 | done | # ATX-05. Заменить сон 250 мс и опрос 1 мс ожиданием счётчика участников
ATX-06 | done | # ATX-06. Заменить time.Sleep(1ms) барьером в TestWebhookConcurrentRedeliverySingleEffect
ATX-07 | done | already landed in PR #1216 efb55590
ATX-08 | done | # ATX-08. Убрать негативную проверку через сон в s2-channel.test.js
ATX-09 | done | # ATX-09. Проверять sender слэш-команд Telegram через ActorResolver
ATX-10 | done | Уже влито в PR #1220 c19fd797
ATX-11 | done | # ATX-11. Ограничить адрес Telegram API и дополнить Redact
ATX-12 | done | # ATX-12. Валидировать ID карточек в batch-apply до filepath.Join
ATX-13 | done | already landed in PR #1369 c71ca2cc9d67
ATX-14 | done | # ATX-14. Заменить файловый замок taskstore на Flock
ATX-15 | done | # ATX-15. Дождаться detached goroutine webhook в Server.Close
ATX-16 | done | # ATX-16. Разделить результат Write и исполнение job в store
ATX-17 | done | # ATX-17. Не держать commitMu на доставке подписчиков event bus
ATX-18 | done | # ATX-18. Снимать snapshot перед внешними вызовами в card_monitor.Poll
ATX-19 | done | # ATX-19. Добавить single-flight в cibridge.Refresh
ATX-20 | done | # ATX-20. Ограничить waitStartRegistry и всегда вызывать finish
ATX-21 | done | # ATX-21. Не держать горутину на вызов WatchOutcome до DB.Close
ATX-22 | done | # ATX-22. Убрать lost update в setEnabled и коллизию ID расписания
ATX-23 | done | # ATX-23. Вынести e2e из критического пути каждого PR
ATX-24 | done | # ATX-24. Общий кэш Go для джоб с setup-go
ATX-25 | done | already landed in PR #1374 2e7b2bfaac04
ATX-26 | done | Уже влито в origin/main через PR #1237, коммит f33d0751769f22632ff6f5eca47dbc0e855772fc ("ci: share Go setup across workflows"); git merge-base --is-ancestor f33d0751769f22632ff6f5eca47dbc0e855772fc origin/main дал код выхода 0. Общий composite action .github/actions/go-runner-setup/action.yml убирает повтор setup-go в ci.yml, docs.yml, gates-binary.yml и tests.yml. Дубль кода не нужен.
ATX-27 | done | # ATX-27. Не выдавать keep_working за informational-проверку
ATX-28 | done | # ATX-28. Считать FIX-попытки в Watch и включать escalate_human
ATX-29 | done | already landed in PR #1251 fe8219ae
ATX-30 | done | already landed in PR #1509 4cec1d40f6ed
ATX-31 | done | # ATX-31. Применять MaxRebases и делать попытку ребаза durable
ATX-32 | done | # ATX-32. Считать wake по подписчику и добавить выход из parked
ATX-33 | done | # ATX-33. Не будить orphaned подписчиков
ATX-34 | done | # ATX-34. Не терять wake при OPEN breaker и хранить счётчик отказов
ATX-35 | done | # ATX-35. Проверять ревизию в beacon_ack по равенству
ATX-36 | done | already landed in PR #1249 4406aea8
ATX-37 | done | # ATX-37. Резать wake-конверт по JSON-границе, а не по байтам
ATX-38 | done | # ATX-38. Сообщать о потере строк в beacon logs и поднимать ошибку сканера
ATX-39 | done | # ATX-39. Помечать обрезание recentWatches в снимке
ATX-40 | done | already landed in PR #1254 af34998b
ATX-41 | done | already landed in PR #1403 3366c7a8915a
ATX-42 | done | already landed in PR #1381 80d3b0cecdb2
ATX-43 | done | already landed in PR #1496 ca9763c2dd79
ATX-44 | done | already landed in PR #1396 19ca1996b55d
ATX-C01 | done | already landed in PR #1351 1090ef02cdc5
ATX-C02 | done | already landed in PR #1635 5c5bc180958b
ATX-C03 | done | already landed in PR #1597 d85bf9484ac9
ATX-C04 | done | already landed in PR #1597 d85bf9484ac9
ATX-C05 | done | Already landed in PR #1636 (commit 6594759e668f7edeef4a6577d3170c12ca26a7af): fix(gates): accept Владелец файлов in wiring cards. git merge-base --is-ancestor 6594759e668f origin/main exit 0.
ATX-C06 | done | already landed in PR #1597 d85bf9484ac9
ATX-C07 | done | Already landed in PR #1651 (commit 7dec396786ba6cfefece72bac4b7b1130336c637): fix(gates): align exception limits and validate tables. git merge-base --is-ancestor 7dec396786ba origin/main exit 0.
ATX-C08 | done | already landed in PR #1597 d85bf9484ac9
ATX-D01 | done | already landed in PR #1321 aed261234e1e
ATX-D02 | done | already landed in PR #1405 823d852b4569
ATX-D03 | done | already landed in PR #1506 57e75db66af4
ATX-D04 | done | already landed in PR #1607 0a3745c6bb4d
ATX-D05 | done | already landed in PR #1607 0a3745c6bb4d
ATX-D06 | done | already landed in PR #1622 5a72ede9882c
ATX-D07 | done | already landed in PR #1640 4a0d33b5f661
ATX-D08 | done | already landed in PR #1511 7657fed34087
ATX-M01 | done | already landed in PR #1638 42c96f97456e
ATX-M02 | done | already landed in PR #1623 eb2de93e6c2b
ATX-M03 | done | already landed in PR #1602 bda51c0fde01
ATX-M04 | done | already landed in PR #1617 d452e555db69
ATX-M05 | done | Already landed in PR #1649 (commit 3dee4ed2ca7886385c67856c6248982722cf9a3b): docs(runbook): deliver closed cards to main via docs-PR (ATX-M05). git merge-base --is-ancestor 3dee4ed2c origin/main exit 0.
ATX-M06 | done | already landed in PR #1469 0efa2344ba84
ATX-M07 | done | Already landed in PR #1650 (commit aab2f6ed82bea0adbdc4bc371b1e4731ed2d0c2a): fix(mcp): extend close idempotency key (ATX-M07). git merge-base --is-ancestor aab2f6ed8 origin/main exit 0.
ATX-M08 | done | already landed in PR #1469 0efa2344ba84
ATX-R01 | done | already landed in PR #1594 f53bacb50c03
ATX-R02 | done | Already landed in PR #1641 (commit 58a9e31704287cd43d5c3155b008ed078e1eab89): refactor(cmd): pass live watch deps explicitly (ATX-R02). git merge-base --is-ancestor 58a9e3170 origin/main exit 0.
ATX-R03 | done | already landed in PR #1603 680fc3a81df0
ATX-R04 | done | already landed in PR #1617 d452e555db69
ATX-R05 | done | already landed in PR #1188 1f3f55bfa949
ATX-R06 | done | already landed in PR #1637 a5e0a752b84a
ATX-R07 | done | already landed in PR #1639 e3179ca123cd
ATX-R08 | done | already landed in PR #874 c1ad8d49b50c
ATX-V01 | done | already landed in PR #1316 df88de4d4a1d
ATX-V02 | done | Уже влито в origin/main через PR #1646, коммит 7debcb0d (fix(cibridge): keep informational checks out of blocker incidents (ATX-V02)). git merge-base --is-ancestor 7debcb0d origin/main -> exit 0. Дубль кода не нужен.
ATX-V03 | done | already landed in PR #1643 cb70a45df173
ATX-V04 | done | already landed in PR #1509 4cec1d40f6ed
ATX-V05 | done | already landed in PR #1599 9be6c20b6bb4
ATX-V06 | done | already landed in PR #1415 3137a74c601d
ATX-V07 | done | already landed in PR #1617 d452e555db69
ATX-V08 | done | Already landed in PR #1648 (commit b8cd2c90e406e032a7c5e536823f64526eec0dc0): docs(atx-v08): define informational check in verdict contract. git merge-base --is-ancestor b8cd2c90e origin/main exit 0.
ATX-W01 | done | Already landed in PR #1642 (commit 4bfa7802508ff04254c6527dcdda4dda5cad8747): fix(store): keep obligations with a pending retry through GC (ATX-W01). git merge-base --is-ancestor 4bfa78025 origin/main exit 0.
ATX-W02 | done | Already landed in PR #1656 (commit 12d676de8fef31290131a8b9d5d730b141036d98): fix(store): recheck GC candidates inside the erase transaction. git merge-base --is-ancestor 12d676de8fef origin/main exit 0.
ATX-W03 | done | already landed in PR #1530 c293cf22bca7
ATX-W04 | done | already landed in PR #1535 4653592ac51c
ATX-W05 | done | already landed in PR #1530 c293cf22bca7
ATX-W06 | done | already landed in PR #1619 059bbcd4ad85
ATX-W07 | done | Already landed in PR #1653 (commit 7e1c96a13b85c071d415af1814c00fbcdbe03bde): guard watch close with revision and head check (ATX-W07). git merge-base --is-ancestor 7e1c96a13 origin/main exit 0.
ATX-W08 | done | already landed in PR #1582 6325f31b0b4b
AUD-01 | done | already landed in #1448 e724f0e
AUD-02 | done | Already landed in PR #1645 (commit 7b7c07ff492b058624d4d90ddeef214803a0d09c): fix(cmd): wake daemon through the shared opencode port (AUD-02). git merge-base --is-ancestor 7b7c07ff4 origin/main exit 0.
AUD-03 | done | already landed in #1473 55e8882
AUD-04 | done | already landed in #1470, #1469, #1436, #1612, #1608 d26bd3b, 0efa234, d0d9fc6, add059c, e31893b
AUD-05 | done | already landed in #1469 0efa234
AUD-07 | done | already landed in #1477 63891e1
AUD-08 | done | already landed in #1611 e0f70e5
AUD-09 | done | # AUD-09. Расширить remote-контракт до всех инструментов MCP
AUD-10 | done | # AUD-10. Актуализировать CycleInstructions и описание инструментов
AUD-12 | done | already landed in #1609 2b0249a
AUD-13 | done | already landed in PR #1277 65cb659c63b7
AUD-14 | done | already landed in #1428 f33f139
AUTOSP-01 | done | already landed in #1448 e724f0e
AUTOSP-02 | done | already landed in #1441 95c5bd4
AUTOSP-03 | done | already landed in #1435 63aada0
AUTOSP-04 | done | Already landed in PR #1659 (commit 02796b23bffdd6e20725668753bd53090067e962): feat(mcp): start one agent per reconcile pass when a slot is free. git merge-base --is-ancestor 02796b23bffd origin/main exit 0.
AUTOSP-05 | done | already landed in #1440 7866746
AUTOSP-06 | done | already landed in #1442 f9c481c
AUTOSP-07 | done | already landed in #1439 ca302a5
AUTOSP-08 | done | already landed in #1438 4c4714a
AUTOSP-09 | done | already landed in #1436 d0d9fc6
AUTOSP-10 | done | already landed in #1478 0ef4bf2
AUTOSP-11 | done | already landed in #1437 95843cb
AUTOSP-12 | done | already landed in #1447 d03c77f
BCX-01 | done | # BCX-01. Исключить docs-only recheck из сигнала implementation merged
BCX-02 | done | already landed in PR #1264 bf4a08ec
BCX-03 | done | # BCX-03. Закрыть 182 ссылки на несуществующие ADR
BCX-04 | done | already landed in PR #1392 9b23dbbc
BCX-05 | dropped | # BCX-05. Генерировать индекс ADR из файлов вместо ручного списка
BCX-06 | done | # BCX-06. Переименовать 66 файлов ADR с именем untitled
BCX-07 | done | # BCX-07. Сделать docs gate блокирующим после очистки exception таблицы
BCX-08 | done | already landed in PR #1262 e427c690
BCX-09 | done | # BCX-09. Заменить устаревшие оценки объёма в README
BCX-10 | done | already landed in PR #1268 b262ba39
BCX-11 | done | # BCX-11. Развести два контракта literals timeout
BCX-12 | done | # BCX-12. Собрать вердикты в один контракт и сделать app-зеркала производными
BCX-13 | done | # BCX-13. Решить судьбу кодов stop_uncertain и ACTOR_NOT_PERMITTED
BCX-14 | done | already landed in PR #1277 65cb659c
BCX-15 | done | # BCX-15. Объединить шесть определений кодов ошибок и две таблицы статусов
BCX-16 | done | already landed in PR #1276 684507e3
BCX-17 | done | already landed in PR #1405 823d852b
BCX-18 | done | already landed in PR #1415 3137a74c
BCX-19 | done | already landed in PR #1397 145c5bd7
BCX-20 | done | already landed in PR #1407 39d04627
BCX-21 | done | already landed in PR #1411 f5a17eb6
BCX-22 | done | already landed in PR #1284 006e00da
BCX-23 | done | # BCX-23. Подключить registry list/check/sync и gc к dispatch
BCX-24 | done | already landed in PR #1281 90b94ce5
BCX-25 | done | already_landed
BCX-26 | done | already landed in PR #1285 2ad5f5a6
BCX-27 | done | # BCX-27. Гарантировать JSON на выходе logs --json
BCX-28 | done | already landed in PR #1301 e437af7b
BCX-29 | done | # BCX-29. Централизовать ошибки CLI и убрать двойной префикс
BCX-30 | done | already landed in PR #1305 99e7e193
BCX-31 | done | Требование уже влито в origin/main. Дубль кода не создаём.
BCX-32 | done | # BCX-32. Привести пять определений DDL registry к одному каноническому
BCX-33 | done | # BCX-33. Кэшировать схему и убрать разные CAST для fencing_token
BCX-34 | done | already landed in PR #1298 2e19434c
BCX-35 | done | already landed in PR #1419 3a08fa96
BCX-36 | done | already landed in PR #1303 d900322e
BCX-37 | done | already landed in PR #1310 231ad3a8
BCX-38 | done | # BCX-38. Убрать двойной индекс журнала и добавить удержание
BCX-39 | done | # BCX-39. Настроить WAL checkpoint и ограничить рост файла
BCX-40 | done | # BCX-40. Перейти к инкрементальному обновлению индекса карточек
BCX-41 | done | # BCX-41. Выбрать один контракт MCP для main и зафиксировать решение
BCX-42 | done | # BCX-42. Подключить или явно закрыть t3_thread_read
BCX-43 | done | # BCX-43. Объявить поле reason в advertised схеме wait_result
BCX-44 | done | # BCX-44. Зафиксировать добавочные поля stats в ADR
BCX-45 | done | # BCX-45. Защитить MCP-контракт от домино-изменений домена
BCX-46 | done | already landed in PR #1299 4dafcf69
BCX-47 | done | # BCX-47. Описать поле executor в контракте watch_pr
BCX-48 | done | # BCX-48. Запретить случайную регистрацию claim и lease инструментов
BCX-49 | done | already landed in PR #1302 5dc4e1e8
BCX-50 | done | # BCX-50. Свери eval-контракт инструментов с сервером
BCX-51 | done | already landed in PR #1315 93497d6a
BCX-52 | done | already landed in PR #1322 50223cf0
BCX-53 | done | # BCX-53. Убрать гонку записи квитанции в beacon-bridge.js
BCX-54 | done | already landed in PR #1323 a95678fe
BCX-55 | done | already landed in PR #1324 397d87bd
BCX-56 | done | already landed in PR #1337 151fa987
BCX-57 | done | already landed in PR #1334 44f9e53d
BCX-58 | done | already landed in PR #1336 b990b518
BCX-59 | done | already landed in PR #1355 83436479
BCX-60 | done | already landed in PR #1319 b8c81e1f
BCX-61 | done | already landed in PR #1312 7089eae5
BCX-62 | done | # BCX-62. Привести ADR 0094, 0263 и AGENTS.md к одному контракту MCP
BCX-63 | done | already landed in PR #1328 3607970e
BCX-64 | done | already landed in PR #1331 b6cefb28
BCX-65 | done | # BCX-65. Развести дубли номеров ADR 0086, 0089, 0091, 0094, 0095
BCX-66 | done | # BCX-66. Реализовать или пересмотреть ADR 0213 о версионированном вопросе
BCX-67 | done | # BCX-67. Снять блокер ADR 0214 или уточнить статус решения
BCX-68 | done | already landed in PR #1332 a59acd23
BCX-69 | done | already landed in PR #1329 8da24238
BCX-70 | done | # BCX-70. Обновить ссылки на удалённые файлы в двенадцати ADR
BCX-71 | done | # BCX-71. Проверять адрес BEACON_GITHUB_API и связать его с основным клиентом
BCX-72 | done | # BCX-72. Задокументировать BEACON_CHANNEL_SENDERS и закрыть слэш-команды ingress
BCX-73 | done | # BCX-73. Проверять хост BEACON_TELEGRAM_API
BCX-74 | done | already landed in PR #1333 acce7fe9
BCX-75 | done | already landed in PR #1340 8d4ed897
BCX-76 | done | already landed in PR #1348 f7049b48
BCX-77 | done | already landed in PR #1349 2f4a2a47
BCX-78 | done | already landed in PR #1338 3540ff43
BCX-79 | done | already landed in PR #1343 88a830d9
BCX-80 | done | already landed in PR #1347 6a1c5d51
BCX-81 | done | already landed in PR #1339 05e0018b
BCX-82 | done | already landed in PR #1353 c35c7279
BS.1 | done | # BS.1. Single-instance lease и pidfile
BS.10 | done | # BS.10. ADR 0262-0264 документация ядра
BS.11 | done | # BS.11. Event log с монотонным seq
BS.12 | done | # BS.12. Outbox в транзакции команды
BS.13 | done | # BS.13. Receipt store по clientRequestId
BS.14 | done | # BS.14. Пул EffectWorker с lease и heartbeat
BS.15 | done | # BS.15. Retirement эффектов потерянного исполнителя
BS.16 | done | # BS.16. EventBus с доставкой после коммита
BS.17 | done | # BS.17. SSE реплей без разрыва
BS.18 | done | # BS.18. Снимки и pruned-through sequence
BS.19 | done | # BS.19. Долгий вызов как тикет с job id
BS.2 | done | # BS.2. HTTP listener loopback плюс bearer token
BS.20 | done | # BS.20. Capability scope и каналы ухода
BS.21 | done | # BS.21. Доменные ключи идемпотентности
BS.22 | done | # BS.22. Audit log актора и ingress
BS.23 | done | # BS.23. /internal/scheduler route group
BS.24 | done | # BS.24. Интервал, фиксированное время и webhook триггер
BS.25 | done | # BS.25. Loopback webhook receiver с HMAC
BS.26 | done | # BS.26. Журнал доставок с UNIQUE(delivery_id)
BS.27 | done | # BS.27. Маппинг webhook на Watch
BS.28 | done | # BS.28. Signature, filters и dedupe вебхука
BS.29 | done | # BS.29. Привязка фоновой задачи к чату
BS.3 | done | # BS.3. /healthz после готовности сервера
BS.30 | done | # BS.30. Polling fallback для вебхуков
BS.31 | done | # BS.31. Cron-проверки карточек и дедлайнов
BS.32 | done | # BS.32. schedule_task/list/delete через внутренний API
BS.33 | done | # BS.33. Персистентность расписаний
BS.34 | done | # BS.34. /api/v1 проекции вотчей
BS.35 | done | # BS.35. /api/v1 задачи и карточки
BS.36 | done | # BS.36. /api/v1 events SSE
BS.37 | done | # BS.37. /api/v1 wake и вопросы
BS.38 | done | # BS.38. TUI как тонкий HTTP-клиент
BS.39 | done | # BS.39. Control socket для агентов
BS.4 | done | # BS.4. /version и /status проекции
BS.40 | done | # BS.40. WS gateway как альтернатива SSE
BS.41 | done | # BS.41. Контракт семи инструментов без изменений
BS.42 | done | # BS.42. MCP stdio как тонкий клиент сервера
BS.43 | done | # BS.43. MCP HTTP endpoint /mcp
BS.44 | done | # BS.44. Совместимость CycleInstructions и dialect
BS.45 | done | # BS.45. /metrics в Prometheus формате
BS.46 | done | # BS.46. Счётчики активных задач, очереди и wake
BS.47 | done | # BS.47. OTel спаны по opt-in
BS.48 | done | # BS.48. Локальный NDJSON trace file
BS.49 | done | # BS.49. Атрибуты метрик низкой кардинальности
BS.5 | done | # BS.5. SQLite WAL, один writer, read pool
BS.50 | done | # BS.50. Аудит через API
BS.51 | done | # BS.51. Контроль footprint меньше 100 MB
BS.52 | done | # BS.52. Docs guard чисел архитектуры
BS.53 | dropped | # BS.53. Мониторинг карточек
BS.54 | done | # BS.54. Автоматическое закрытие карточек
BS.55 | dropped | # BS.55. Синхронизация backlog
BS.56 | done | # BS.56. Триаж dependabot PR
BS.57 | done | # BS.57. Пакетный мерж зависимостей с expectedHeadOid
BS.58 | done | # BS.58. Rebase pointer через daemon waker
BS.59 | dropped | # BS.59. Роутер авто-уведомлений
BS.6 | done | # BS.6. Endpoint writer в connection.json
BS.60 | dropped | # BS.60. Channel ingress с opt-in
BS.61 | dropped | # BS.61. Детерминированный channel reply
BS.62 | dropped | # BS.62. Конфиг merge.driver
BS.63 | done | # BS.63. Сценарий ночной смены как воркер
BS.64 | done | # BS.64. Очистка stale PR и вотчей
BS.65 | dropped | # BS.65. Напоминания о дедлайнах и snooze
BS.66 | dropped | # BS.66. Реестр сессий с generations
BS.67 | done | # BS.67. Реестр исполнителей с capabilities
BS.68 | dropped | # BS.68. Сервис вопросов и одобрений
BS.69 | dropped | # BS.69. Lineage дочерних thread
BS.7 | done | # BS.7. Graceful drain по SIGTERM
BS.70 | done | # BS.70. Архивация событий и квитанций
BS.71 | done | # BS.71. Проверка config drift
BS.72 | done | # BS.72. Восстановление после рестарта
BS.73 | done | # BS.73. Scenario harness с stub-исполнителями
BS.74 | done | # BS.74. Eval E01 подписи webhook
BS.75 | done | # BS.75. Eval E02 дедупликации доставки
BS.76 | done | # BS.76. Eval E03 защиты от loop
BS.77 | done | # BS.77. Безопасность loopback, токенов и payload
BS.78 | done | # BS.78. Миграция состояния старого Beacon
BS.79 | done | # BS.79. Глобальная установка в ~/.config/opencode
BS.8 | done | # BS.8. Config resolver и env overlay
BS.80 | dropped | # BS.80. Runbook и документация сервера
BS.9 | done | # BS.9. Project scope по resolved cwd и git root
BVF-01 | done | # BVF-01. Развести конфликты owner_files между пакетами аудитов в одной волне
BVF-02 | done | Требование уже в main. PR #1458, полный SHA 7e99886d4af4dbf65b8e8907d28b41972bd261df. git merge-base --is-ancestor 7e99886d origin/main вернул 0. Аудит в docs/research/batches/bcx-fix-report.md: missing=0, directory_refs=0, overlap_groups=0.
BVF-03 | done | Требование уже в main. PR #1458, полный SHA 7e99886d4af4dbf65b8e8907d28b41972bd261df. git merge-base --is-ancestor 7e99886d origin/main вернул 0. Аудит в docs/research/batches/bcx-fix-report.md:61: cards=82 missing=0 directory_refs=0 overlap_groups=0.
BVF-04 | done | # BVF-04. Проверять batch-пакеты в CI до их применения
CA-01 | done | Already landed in PR #1698 (commit 54196badf10a76e324ee295e8f7e63f662a68f2c): feat(session): add ranked repo map to child context. git merge-base --is-ancestor 54196badf10a origin/main exit 0.
CA-02 | done | Решение уже в main: PR #1782, merge commit 5d801aba, git merge-base --is-ancestor 5d801aba origin/main вернул 0. Бюджет карты реализован в internal/app/session/delegation.go:830 (MapBudgetFor) и :364 (WithMapBudget). Карточка в main осталась todo, закрываем с доказательством.
CA-03 | done | Already landed in PR #1772, commit be5390a245271f292e3d651b5a43a03bade740d4; git merge-base --is-ancestor exit 0 against origin/main. No duplicate code PR opened.
CA-04 | done | Fix already merged: PR #1777, merge commit e45bbeabcef6a598d8990fbf0d717b25e40845d7; git merge-base --is-ancestor e45bbeabcef6a598d8990fbf0d717b25e40845d7 origin/main exit 0. origin/main contains WatchAnchor.SessionCommits, RecordCheckpoint, CheckUndo, UndoCheckpoint in internal/infra/worktree/headguard.go and the checkpoint section in docs/templates/executor-task.md.
CA-05 | done | Fix already merged: PR #1778, merge commit 84515237b6fac4c030bebcda552bba00b1ababc2; git merge-base --is-ancestor 84515237b6fac4c030bebcda552bba00b1ababc2 origin/main exit 0. origin/main contains the executor-task:test-output section and the failureExcerpt fields in docs/templates/executor-task.md.
CA-06 | done | Уже влито в main через PR #1741, коммит ad8da4f64be3cb4788c7dc1e20ec0b5ea8dacf26 (feat(session): budgeted head/tail compaction for child history (CA-06)). git merge-base --is-ancestor ad8da4f6 origin/main -> exit 0. Дублирующий код PR не создаётся, только закрытие карточки.
CA-07 | done | Already in origin/main: commit 4b27cbe2c3107c904f2f513d0764d0b2e798a633, PR #1781. git merge-base --is-ancestor 4b27cbe2 origin/main returned 0. No open PR touches the owner files. Code duplication skipped.
CA-08 | done | Already landed in origin/main via PR #1789, full SHA 4697b35fd1b64ad9b674a70f6318a9ae74964046 (feat(batchapply): structured response for failed edits (CA-08)). git merge-base --is-ancestor 4697b35fd1b64ad9b674a70f6318a9ae74964046 origin/main exit 0. No open duplicate PR. Card status left as todo in main, so this PR carries only the card close.
CA-09 | done | Already landed in PR #1827, merge commit 4e8c96285ace37b9ed89cb187b9b8d1d50352d37; git merge-base --is-ancestor 4e8c9628 origin/main exit 0. Both OwnerFiles already carry the implementation: CachePoint/CachePlan/PrefixFingerprint/KeepaliveConfig in internal/app/session/delegation.go and cache:boundary markers in AGENTS.md. No duplicate code PR.
CA-10 | done | Уже влито в origin/main. Коммит dd8810a49836bdc972ade5aaef1fdf17eebac076 через PR #1783 (feat(session): attach mentioned files after confirmation (CA-10)), mergedAt 2026-11-22T14:53:33Z. git merge-base --is-ancestor dd8810a49836bdc972ade5aaef1fdf17eebac076 origin/main -> exit 0. Реализация присутствует в origin/main: ParseMentions/MatchMentionFiles/WithIgnoredMentions/WithReflected в internal/app/session/delegation.go и секция executor-task:mentions в docs/templates/executor-task.md. Дубль кода не создаётся.
CA-11 | done | Already landed in PR #1784, merge commit 805c777519c823fb34a87094997e7cb78fe4ba51. merge-base --is-ancestor against origin/main exit 0. Both OwnerFiles (docs/templates/executor-task.md, docs/research/OWNERFILE-DISCIPLINE.md) are carried in that PR; no duplicate code.
CA-13 | done | Already merged in PR #1749, merge commit 7b57c0ad01c8763a3298eaf09f7d3336e4eff5c7; git merge-base --is-ancestor 7b57c0ad origin/main exit 0. Code fix for internal/app/prcreate/cards.go and internal/app/prcreate/service.go already landed, no duplicate code PR.
CA-14 | done | already_landed
CA-15 | done | Уже влито в origin/main через PR #1786, коммит 0cb45a04fb44ab0900d68996b31aa12c776fe3b6. git merge-base --is-ancestor 0cb45a04 origin/main вернул 0. Дубль кода и PR не создаются.
CA-16 | done | PR #1764 влит в origin/main, merge commit badee108e467c4515d2d97126d35e84891db38b7, `git merge-base --is-ancestor badee108 origin/main` завершился с кодом 0. Дублирующий код не писался.
CA-17 | done | Уже влито в main: PR #1790, полный SHA f7d6c316594f706acd041fa886dd1878ae1cded8, git merge-base --is-ancestor exit 0. PR #1790 изменил оба OwnerFiles (internal/app/prcreate/cards.go и docs/templates/executor-task.md). Дубль кода не пишем.
CA-18 | done | Уже влито в main: коммит 5fd6d5c0f73e0379885b746542882558143a3f3b через PR #1794 (merged 2026-11-22T15:20:00Z). git merge-base --is-ancestor 5fd6d5c0 origin/main = exit 0. Коммит изменил ровно OwnerFiles карточки AGENTS.md и cmd/beacon/index.go. Дубль кода не создаётся.
CA-19 | done | Уже влито в origin/main: PR #1793, коммит c7ba4b115f86216e1e99f37ffc33429eb3199b25. git merge-base --is-ancestor SHA origin/main вернул 0. Дубль кода не пишем.
CA-20 | done | Уже влито в main через PR #1795, merge commit 72a745642a318c1d1540942421287580b0445f52. Проверка `git merge-base --is-ancestor 72a745642a318c1d1540942421287580b0445f52 origin/main` вернула exit 0. Реализация RefreshMode, MapRefreshKey и RankedMapCache в internal/app/session/delegation.go и раздел про --map-refresh в AGENTS.md присутствуют в origin/main. Дубль кода не создаётся.
CA-21 | done | Уже влито в main через PR #1930, коммит 34346fed520da4ddc9de68f069de76f0bd35159b. git merge-base --is-ancestor 34346fed origin/main вернул 0. Выбор модели по фазе присутствует в обоих OwnerFiles на origin/main: ModelFor и PhaseModels в internal/app/session/delegation.go, блок executor-task:phase-models в docs/templates/executor-task.md. Дубль кода не нужен.
CA-22 | done | Реализация уже в main: коммит ddd5dd0a831b43c6c4664a0a9ec0b88989832c74, PR #1798, merge-base --is-ancestor exit 0. Дубль кода не нужен.
CA-23 | done | Уже влито в origin/main: f6f1bb1400c69c52b7fc75d3e60efaf554a4aa7f (PR #1766, merged 2026-11-22T13:36:03Z). git merge-base --is-ancestor f6f1bb14 origin/main вернул 0. Файл internal/app/taskcli/state_filestore.go уже содержит retryTemporaryWrite с 5 попытками и задержками 0.1, 0.2, 0.4, 0.8. Дубль кода не нужен.
CA-24 | done | Реализация уже в main: PR #1889, коммит e700fd2ae8631c9316ccfce8af4ff8a1570534bc. Проверка git merge-base --is-ancestor e700fd2ae8631c9316ccfce8af4ff8a1570534bc origin/main дала exit 0. Подключение к failure excerpts сделано в PR #2150, коммит ab575c2f7c449ca127b237adbba10656aa17e01c. Дубль кода не нужен.
CB-01 | done | false premise: already landed in PR #1693, SHA 642a5ba583478ba7160964df71c3c3a5bc1f6fcf, internal/infra/taskstore/store.go:594-700
CB-02 | done | Код уже влит в origin/main: a99bddd31d98645f99dc80daf397a343007072cf (PR #1780, feat(taskstore): two-pass ID collision resolution on tree merge (CB-02)). git merge-base --is-ancestor a99bddd3 origin/main -> exit 0. Ложный claim TCG-13 без cardID снят; карточка закрыта как done с доказательством, дубль кода не писался.
CB-03 | done | already_landed. Код CB-03 влит в origin/main коммитом 8dd2d5eb4c160fea63a11c6e1371ad1eec37a64a через PR #1779 (https://github.com/refusedguy/beacon/pull/1779). git merge-base --is-ancestor 8dd2d5eb4c160fea63a11c6e1371ad1eec37a64a origin/main -> exit 0. Ложное закрытие mis-claimed by IFC-01 от 2026-11-23T06:00:25Z отменено: дубль кода не писался.
CB-04 | done | already landed: PR #1785 merged as 00ee7201ae89560dc07019ddcaad2b737ab0f105 (git merge-base --is-ancestor 00ee7201 origin/main exit 0); EdgeConditionalBlocks present in origin/main internal/infra/taskstore/edges.go; false in_progress from BP-16 claim without cardID, no code touched
CB-06 | done | Код CB-06 уже влит в main через PR #1787, коммит 9e1b6a2f40267b63aa42d84b6f218038b14be289; git merge-base --is-ancestor 9e1b6a2f origin/main -> exit 0. Коммит изменил ровно OwnerFiles cmd/beacon/task.go и cmd/beacon/task_test.go. Ложный claim TCG-08 без cardID в PR #2226, код не писался. Удержание снято, карточка закрыта с доказательством.
CBX-01 | done | Код карточки влит в main через PR #1742 https://github.com/refusedguy/beacon/pull/1742, merge commit bfa4ada4c3a9cfb75844e64f8d5a014efd326148. Проверка git merge-base --is-ancestor bfa4ada4c3a9cfb75844e64f8d5a014efd326148 origin/main вернула exit 0. Коммит изменил все три OwnerFiles: internal/app/taskcli/service.go, internal/infra/taskstore/edges.go, internal/app/taskcli/service_test.go. Ложный claim без cardID в открытом PR #2264 (TCG-06, только internal/infra/store/db_test.go) снят.
CBX-06 | done | Уже влито в PR #1743 (squash 3a3c303418410308e22894786dc2c44d4b478a33); git merge-base --is-ancestor exit 0. Код CBX-06 (куски записи с фиксацией, пауза, ChunkSize/ChunkPause, отчёт о кусках) уже в origin/main, дубль кода не нужен.
CBX-07 | done | Already landed in PR #1800, squash commit ab01923b863b805d278974e93d7eb6618f60cac6. Verified git merge-base --is-ancestor ab01923b origin/main exit 0. OwnerFiles internal/infra/batchapply/apply.go and apply_test.go already carry the stale-row prefilter, in-upsert updated_at recheck and force override in main. No duplicate code written.
CBX-08 | done | Реализация влита в origin/main коммитом 95fac608afe9b25534bc2a0dbbb77383df768f07 через PR #1837 (feat(batchapply): machine-readable import report with tie handling). Проверка git merge-base --is-ancestor 95fac608afe9b25534bc2a0dbbb77383df768f07 origin/main вернула exit 0. PR #1837 изменил ровно OwnerFiles: internal/infra/batchapply/apply.go и apply_test.go. В main присутствуют CreatedIDs, UpdatedIDs, StaleSkippedIDs, TieKeptLocalIDs, WrittenPaths, Changes и обработка равных меток. Дубль кода не создаём.
CBX-10 | done | Already landed in PR #1815 (merge commit 84fca286168941d00877f490ed1119dea0056067, fix commit e24310b00ae6e2cfd245f091400ed94819947b09). git merge-base --is-ancestor 84fca286 origin/main exit 0. Implementation present in origin/main: cmd/beacon/prcreate.go, internal/infra/github/repo_detect.go, internal/infra/github/repo_detect_test.go.
CBX-12 | done | Реализация уже в origin/main. Коммит b139921312fc23475840804f60aa284ed8e947e8 влит через PR #1762 'feat(gc): compact card history with ordered replay and refusal (CBX-12)'. git merge-base --is-ancestor b1399213 origin/main вернул exit 0. Дубль кода не создаётся.
CBX-15 | done | Уже реализовано в открытом PR #1868 (fix/cbx15, head c462cbc1b93cb83d99ffa24f6192a128e5874f4c); OwnerFiles заняты открытым PR #1918 CBX-05 (head d380d8d344e89eb09b110f8dc8d76890f449a7ec); в origin/main коммита CBX-15 нет (git log --grep=CBX-15 пусто). Дубль кода не пишу.
CC-01 | done | false premise: already landed in PR #1700, SHA e2f34a060579d452c1ca6ca7845560b78b8b7629, cmd/beacon/orchestrate.go:638-712
CC-02 | done | Fix already landed in origin/main via PR #1722 (merge commit eac6005ce1e267d377f6c5d44953de863106e46a). git merge-base --is-ancestor eac6005c origin/main exit 0. OwnerFiles marker.go/marker_test.go already contain atomic no-clobber owner bind with two-concurrent-bind and rebind tests. No duplicate code needed.
CC-03 | done | Fix already landed in origin/main via PR #1732, merge commit 463c05560fa154aeef5076dfc6f892553bc4db77 (feat(orchestrator): declarative approval policy with checked examples (CC-03)); `git merge-base --is-ancestor 463c0556 origin/main` exit 0. No open duplicate CC-03 PR. Closing card with evidence; docs-only close PR carries status to main.
CC-04 | done | already_landed
CC-05 | done | already_landed
CCx-01 | done | already_landed
CCx-02 | done | Already landed in origin/main via PR #1725, commit 73cb145e2803fdc25a7646f950538dcb3d933ac8 (git merge-base --is-ancestor origin/main exit 0); touches only OwnerFiles cmd/beacon/orchestrate.go and cmd/beacon/orchestrate_budget_test.go. No duplicate code needed.
CCx-03 | done | Already landed in PR #1723 (commit 7c48dccd9a458dd1eeceb8b15e1bc0e3c20f134b). git merge-base --is-ancestor 7c48dccd9a458dd1eeceb8b15e1bc0e3c20f134b origin/main exit 0. Owner files cmd/beacon/orchestrate.go and cmd/beacon/orchestrate_ready_test.go already carry the readiness-wait fix.
CCx-04 | done | Already landed in PR #1731 (squash commit 935201f757d4bca3f357ec4aaa31baafb63e4a66). git merge-base --is-ancestor 935201f7 origin/main exit 0. No duplicate code PR opened.
CCx-05 | done | Already landed in PR #1729 (feat(orchestrator): defer network approval with cancel proxy (CCx-05)), squash merge 8f02f459; git merge-base --is-ancestor 8f02f459 origin/main exit 0. No duplicate code PR; docs-only status PR follows.
CCx-06 | done | Already landed in PR #1833 (merge commit bf42922f47808f3bdbce4549c1f80d0150fa6bf7). git merge-base --is-ancestor bf42922f47808f3bdbce4549c1f80d0150fa6bf7 origin/main exit 0. SandboxPolicy with four modes, write/network checks and docs/evals/ccx06-sandbox-modes.md are present in origin/main. No duplicate code written.
CCx-07 | done | Реализация уже в main. Коммит c4ec7e74ec5d5bbc5c814e263f0b1cd46a455b14, PR #1876. Проверка git merge-base --is-ancestor c4ec7e74 origin/main вернула 0. В main есть SandboxPolicy.ProtectedNames, protectedPathReason и тест internal/app/orchestrator/protected_paths_test.go. Дубль кода не нужен.
CCx-08 | done | Implementation already landed in origin/main via commit 837b0d34 (PR #1830). git merge-base --is-ancestor 837b0d34 origin/main exit 0. No duplicate code written.
CCx-09 | done | Already landed in PR #1826: commit f1ad10f6 feat(session): reverse scan to compaction cutoff at fork (CCx-09), merge-base --is-ancestor f1ad10f6 origin/main exit 0. Implementation in cmd/beacon/attach.go, internal/app/session/registry.go, cmd/beacon/attach_fork_test.go; no duplicate code. Closing card status to match main.
CCx-10 | done | Уже влито в main через PR #1870. Коммит 6c68bd79aaf12c3160574bf343309f6091e52c10 (merge-коммит PR #1870) является предком origin/main, git merge-base --is-ancestor вернул 0. Проверка identity лежит в cmd/beacon/attach.go (поле Identity в attachSession, registryResumeSession) и internal/app/session/registry.go (ResumeIdentified, ErrSessionIdentityMismatch), тест в cmd/beacon/attach_identity_test.go. Дубль кода не создан.
CCx-11 | done | Уже влито в main через PR #1866. Merge commit fbf9de0e70ac6e301d868f9a55e804590e0bc276. git merge-base --is-ancestor fbf9de0e70ac6e301d868f9a55e804590e0bc276 origin/main вернул exit 0. Реализация уже в origin/main: MetadataJournalPath, JournalPath, WithJournalPath в internal/app/session/registry.go; ResumeJournal, ErrNoJournalPath, ErrJournalPathMissing в internal/infra/store/sessions.go; три теста в internal/infra/store/sessions_resume_test.go, включая случай 3 исходные строки плюс одна строка resume дают 4 строки в том же файле. Дубль кода не создан.
CCx-12 | done | Реализация уже влита в main: PR #1869, merge commit 00621a08dad30f713533c9b0700e85c529a76361. git merge-base --is-ancestor <SHA> origin/main -> exit 0. Дубль кода не создавался.
CCx-13 | done | CCx-13 уже реализован коммитом 552d9e0d (PR #1873) и влит в origin/main. merge-base --is-ancestor 552d9e0d origin/main вернул 0. В cmd/beacon/attach.go есть attachCandidateFits, savedAttachRecord и проверка пригодности кандидата в registryResumeSession. Два теста лежат в cmd/beacon/attach_candidate_test.go. Дубль кода не нужен.
CCx-14 | done | Already landed in origin/main via PR #1931, commit 1f9ad9dcabb73b03b8eaca7006251a76e886a734 (feat(orchestrator): read-only first shell call after resume (CCx-14) (#1931)). git merge-base --is-ancestor 1f9ad9dcabb73b03b8eaca7006251a76e886a734 origin/main exit 0. No duplicate code written.
CCx-15 | done | Implementation already landed in origin/main via PR #1952, commit 30ee1c73 (feat(session): restore requested workspace roots at resume (CCx-15) (#1952)). Verified git merge-base --is-ancestor 30ee1c73 origin/main exit 0. All three OwnerFiles changed in that commit: internal/app/session/registry.go, cmd/beacon/agents.go, internal/app/session/workspace_resume_test.go. No duplicate code written.
CCx-16 | done | Уже влито в main через PR #1816, полный SHA f7bae2f438367c1c34be2c3abc20404d93e57457; git merge-base --is-ancestor f7bae2f438367c1c34be2c3abc20404d93e57457 origin/main = 0. Коммит изменил все три OwnerFiles: internal/infra/store/atomic_write.go, internal/infra/store/schema_contract_test.go, docs/evals/ccx16-trace-schema.md. Дубли кода не делаем.
CCx-17 | done | Already landed in PR #1825 via commit 15885f397ab1e6fd60fb02d673acd8bd4f5de068 (fix(observe): bound check telemetry tags (CCx-17)); git merge-base --is-ancestor 15885f397ab1e6fd60fb02d673acd8bd4f5de068 origin/main exit 0. Fix present in OwnerFiles internal/infra/observe/otel.go and telemetry_contract_test.go. No duplicate code written.
CCx-18 | done | Уже влито в main через PR #1832, коммит 02bc6b7914bc58672b3f09485ebbf0488b817f2a. git merge-base --is-ancestor 02bc6b7914bc58672b3f09485ebbf0488b817f2a origin/main -> exit 0. Дубль кода не создан.
CCx-20 | done | Already landed in PR #1813, merge commit 0d433be935eda13eeceb5013c0a949472f3f0e25. git merge-base --is-ancestor against origin/main exits 0. The implementation is in internal/infra/worktree/list.go and internal/infra/worktree/backlink_test.go on origin/main, so no duplicate code was written.
CCx-21 | done | Landed in PR #1817, merge commit 8acc95fd8bbbea6fc19848dd1d4013d5f336e459. git merge-base --is-ancestor 8acc95fd... origin/main exits 0. internal/infra/worktree/marker.go already has MarkerVersion, DisallowUnknownFields, ErrMarkerVersion and marker_version_test.go exists in origin/main. No duplicate code written; card close fields shipped in a docs-only PR.
CCx-22 | done | Already landed in origin/main via commit 87f60b6b (PR #1823) "feat(mcp): model tool namespace and encrypted parameter markers (CCx-22)". git merge-base --is-ancestor 87f60b6b origin/main exit 0. OwnerFiles schema.go/namespace_test.go already contain ToolNamespace, NamespacedTool, SetToolNamespace, ToolNamespaceOf, MarkParameterEncrypted, ParameterEncrypted and SubstituteToolSchema with acceptance test; later schema.go commits 42b96d35 and e0eb1743 are on top. No duplicate code.
CCx-24 | done | Already landed in PR #1909 (merge commit 7d810af7, "feat(batchapply): classify sandbox write denials (CCx-24)"); git merge-base --is-ancestor 7d810af7 origin/main exit 0. Implementation is present in internal/infra/batchapply/apply.go and denial_test.go. No duplicate code written.
CD-01 | done | false premise: already landed in PR #1551, SHA 2193bf85f9d6ce8a3e1350cbd861e3978adbc9bc, internal/app/taskcli/service.go:55-112, :859
CD-02 | done | false premise: already landed in PR #1686, SHA 6bebd42fa1e93a1ba2f8fee38e93c496909e9b0d, internal/infra/worktree/resume_guard.go:46
CD-03 | done | already landed in PR #1720 (merge commit 7dd234bc90b6c5a4444208e70b1a99753d4114c4, git merge-base --is-ancestor origin/main exit 0); OwnerFiles merge_queue.go + merge_queue_test.go already in origin/main
CD-04 | done | already_landed
CD-05 | done | Already landed in origin/main via PR #1822, merge commit 7f62aa8387242d2d78111fb99c784f4bd2a9655d. git merge-base --is-ancestor 7f62aa8387242d2d78111fb99c784f4bd2a9655d origin/main exits 0. internal/infra/hookrun/modes.go and modes_test.go are present in origin/main, so no duplicate code is written.
CD-06 | done | Реализация уже влита в origin/main через PR #1814 (merge commit 9a03f64a588df64ceea8f9ac25bb5bed8b442eea, git merge-base --is-ancestor exit 0). Дубль кода не создаётся, в PR переносится только статус карточки с доказательством.
CD-08 | done | CD-08 уже влит в origin/main через PR #1834, merge commit 81dc0be3fda79b996280549ea33a12048287efb9. git merge-base --is-ancestor 81dc0be3fda79b996280549ea33a12048287efb9 origin/main вернул exit 0. Реализация internal/app/taskcli/hook_actions.go присутствует в main, дубль кода не создаётся.
CD-09 | done | Already landed in origin/main via PR #1836, merge commit 3308facb5db0907c4065dd9b88225187fe232e0e. git merge-base --is-ancestor 3308facb5db0907c4065dd9b88225187fe232e0e origin/main exits 0. CollectBranchCandidates and its tests are present in main, so no duplicate code was written.
CD-10 | done | CD-10 уже влит в origin/main через PR #1835, merge commit 9e44ab6bdce1db748dcd1f02d9c02ae01ddcdf19. git merge-base --is-ancestor 9e44ab6b origin/main вернул 0. Реализация в internal/app/session/worktree_slug.go и worktree_slug_test.go присутствует в main. Дубль кода не создаётся.
CD-11 | done | Already landed in PR #1848 (merge commit 012024556bba59a8a969fa74e4773798f0336a44, PR head 2129dc90853785b0b05a137f8bb8818aecf716d2). git merge-base --is-ancestor 012024556bba59a8a969fa74e4773798f0336a44 origin/main exit 0; implementation present at origin/main:internal/infra/worktree/worktree_meta.go. No duplicate code written.
CD-12 | done | Реализация уже влита в main через PR #1850 (commit d5724b512536a7877bae0ed5c438c513a1ee0074, merge-base --is-ancestor exit 0). Файлы internal/app/session/agent_observation.go и agent_observation_test.go присутствуют в origin/main. Дубль кода не создаётся.
CD-13 | done | Реализация уже в main: SHA a1a9c250b7926e9ee3da952232609ad66d5eb9ed (PR #1840), `git merge-base --is-ancestor a1a9c250 origin/main` exit 0. Файлы internal/app/session/agent_launch_catalog.go и agent_launch_catalog_test.go присутствуют в origin/main. Дублирующий код не создаём.
CD-14 | done | Уже влито в main через PR #1851, коммит ba3a5d4f95cb5df6504a9bf7a72fdfef0e2c341b (feat(session): add private prompt-file handoff with fallback (CD-14)). git merge-base --is-ancestor ba3a5d4f95cb5df6504a9bf7a72fdfef0e2c341b origin/main даёт exit 0. Реализация в internal/app/session/prompt_file.go и prompt_file_test.go уже в main, дубль кода не нужен.
CD-15 | done | Уже влито в main через PR #1847, коммит 077a531a6d6510ec08cd2c82fd0c66e88b8ce57e. Проверка git merge-base --is-ancestor 077a531a origin/main вернула 0. Реализация и тесты merge_preflight есть в origin/main. Дубль кода не создаётся. Этот PR несёт только закрытие карточки.
CD-16 | done | Уже влито в main через PR #1843, коммит 91d4cef4 (feat(orchestrator): detect merge conflicts before touching the tree). git merge-base --is-ancestor 91d4cef4 origin/main вернул exit 0. conflict_dryrun.go присутствует в origin/main. Дубль кода не создан.
CD-17 | done | Требование уже влито в main через PR #1842. Дубль кода не создан. Доказательство: коммит 5e33112188aecac07ed431bc622bb09187d80fb2 (feat(orchestrator): classify empty merge queue elements (#1842)); `git merge-base --is-ancestor 5e33112188aecac07ed431bc622bb09187d80fb2 origin/main` вернул exit 0; PR https://github.com/refusedguy/beacon/pull/1842 в состоянии MERGED с 2026-11-22T15:41:02Z; реализация в internal/app/orchestrator/merge_skip.go:45-53 (ClassifyMergeSkip) и :92-105 (BuildMergeSkipReport), тесты в merge_skip_test.go. MCP task_close и task_claim отклонили карточку как не найденную в store, поэтому статус зафиксирован в этом docs-only PR с полным доказательством.
CD-18 | done | Already landed in PR #1846 as commit 8c0050efd6f31cae886c6a2fc96f46f0b47870c5. git merge-base --is-ancestor 8c0050ef origin/main exits 0. The implementation in internal/app/session/inference_bound.go and inference_bound_test.go is already on main, so no duplicate code is written.
CD-19 | done | already_landed
CLEAN-01 | done | # CLEAN-01. Пересчитать счётчики roadmap по факту статусов карточек
CLEAN-02 | done | # CLEAN-02. Удалить пять неимпортируемых серверных пакетов
CLEAN-03 | done | # CLEAN-03. Удалить мёртвые порты mcp.Bridge, Notifier и ControlPlaneStore
CLEAN-04 | done | # CLEAN-04. Убрать семь устаревших исключений reachability и пересобрать бинарник гейтов
CLEAN-05 | done | already landed in PR #1264 bf4a08ec and PR #1335 f9a600e7
CLEAN-06 | done | # CLEAN-06. Решить судьбу архива dropped-карточек и старых worktree
CLEAN-07 | done | # CLEAN-07. Починить структуру пяти ADR
CLEAN-08 | done | # CLEAN-08. Зафиксировать решение о закоммиченном бинарнике гейтов
CLEAN-09 | done | # CLEAN-09. Закрыть RS-OBS-02 и удалить локальный OTEL sink
CLEAN-10 | done | already landed in PR #1154 d4f7b326 and PR #1172 866d11ba
CLEAN-11 | done | # CLEAN-11. Удалить три не подключённых пакета app
CLEAN-12 | done | # CLEAN-12. Удалить internal/tui после закрытия WIRING-493
CLEAN-13 | done | # CLEAN-13. Удалить internal/app/tasks после закрытия WIRING-487 и WIRING-489
CLEAN-14 | done | # CLEAN-14. Удалить невызываемые wake-символы по ADR 0263
CLEAN-15 | done | # CLEAN-15. Подключить или удалить registryWakeAllowed
CLEAN-16 | done | # CLEAN-16. Проверить список REACH-UNWIRED перед очисткой
CM-01 | done | Already landed in PR #1687 (commit 8cceeb1ec6ed9f58801d55ea1e3de514a4f43f59): docs(agents): add red-team reproduction gate before hypothesis. git merge-base --is-ancestor 8cceeb1ec6ed origin/main exit 0.
CM-02 | done | Already landed in PR #1711 (commit 9d9614a852abf3f1409f5c5f718645eccb5cef63): docs(templates): add minimal repro and ranked hypotheses to executor template. git merge-base --is-ancestor 9d9614a852ab origin/main exit 0.
CM-03 | done | Ядро карточки уже в main: PR #1774, merge commit 1239e604960d40cab27c9bd2087fdbbcf7b02966. git merge-base --is-ancestor 1239e604 origin/main вернул 0. docs/design/module-depth.md уже лежит в origin/main с тремя критериями и негативным контролем. Дубль документа не создаём.
CM-04 | done | Решение уже влито в main: PR #1775, коммит d9be2ed9138ba1a11d646e0093acfacb174c3368, merge-base --is-ancestor exit 0. Правила вертикальных срезов и expand-contract уже в docs/templates/orchestrator-task.md. Дубль кода не пишется.
CM-05 | done | Уже в origin/main: PR #1776 MERGED 2026-11-22T14:57:50Z, merge SHA c9860750df30245927cd5cccb3fbe4ceccf59b12, `git merge-base --is-ancestor c9860750 origin/main` exit 0. Правка GLOSSARY.md уже в main, дубль кода не создаётся.
CMx-01 | done | Решение уже влито в origin/main: PR #1746, squash-коммит ebd98317. git merge-base --is-ancestor ebd98317 origin/main вернул 0. Файл docs/design/review-axes.md уже содержит обе оси, все двенадцать запахов и запрет переранжирования. Дубль кода не создан.
CMx-02 | done | Premise-check: требование уже влито в origin/main через PR #1841, merge commit 251371eae71bc17f25f49c8b99f5236f6ea637fb (docs(templates): add PR body risk record template (CMx-02)). docs/templates/pr-body.md присутствует в origin/main. git merge-base --is-ancestor 251371ea origin/main -> exit 0. gh pr view 1841: state MERGED, mergedAt 2026-11-22T15:35:22Z. Дубль кода не создаём; отдельный docs-PR несёт только статус карточки и доказательство.
CMx-03 | done | Уже влито через PR #1844, merge commit 984e7aa7c652630a04cd0d784ab3657536869de8. docs/design/flow-map.md присутствует в origin/main (45 строк). git merge-base --is-ancestor 984e7aa7 origin/main -> exit 0. Дубль кода не создан, PR несёт только статус карточки и доказательство.
CMx-04 | done | Already landed in origin/main via PR #1838, full SHA 953187b0080a24d0d8e1ff20c50a3c990e9ee0e3 (docs(design): add design-it-twice parallel interface technique (CMx-04)); git merge-base --is-ancestor 953187b0080a24d0d8e1ff20c50a3c990e9ee0e3 origin/main exit 0; docs/design/design-it-twice.md present in origin/main. No duplicate code written.
CMx-05 | done | already_landed
CMx-06 | done | Already landed in PR #1839 (merge commit 543163501be85e135ca5fb851f5fa38ade62a38b). git merge-base --is-ancestor 543163501be85e135ca5fb851f5fa38ade62a38b origin/main exit 0. docs/design/architecture-scan.md is present in origin/main and satisfies the card: two entry paths, git log --oneline, five friction kinds, CM-03 pointer. No duplicate code written.
CMx-07 | done | Фикс уже в origin/main: squash-коммит 0bcc97035803920d31c2ed624ae810bdaa442c19 (PR #1845), git merge-base --is-ancestor 0bcc9703 origin/main -> exit 0; docs/templates/architecture-report.md присутствует в origin/main. Дубль кода не нужен. В PR переносится только статус карточки с доказательством.
CMx-08 | done | Фикс уже в origin/main: docs/design/integration-branch.md залит PR #1854 коммитом 852628abee2f09f0d0c75deb4e5af625ab65e1cf, git merge-base --is-ancestor 852628abee2f09f0d0c75deb4e5af625ab65e1cf origin/main вернул 0. Дубль кода не создаём.
CMx-09 | done | Already landed in PR #1855, merge commit fad36274ffeae3efc5642b177d5596c11233fb5e (docs(design): triage states and claim check (CMx-09)). docs/design/triage-states.md is present in origin/main; git merge-base --is-ancestor fad36274ffeae3efc5642b177d5596c11233fb5e origin/main exits 0. No duplicate code written.
CMx-10 | done | Уже влито в origin/main через PR #1853 (merge commit dea78a96, исходный коммит a3328e6c). `git merge-base --is-ancestor dea78a96 origin/main` вернул код выхода 0. docs/design/decision-map.md уже присутствует на main и содержит все шесть элементов из проверки карточки. PR-дубль кода не открывается по ветке 6b.
CMx-11 | done | Already merged via PR #1852, commit 091a00f2f78988754f14417451b4bbecbfa08c0d (docs(runbooks): add retro runbook turning mechanical errors into checks (CMx-11)). git merge-base --is-ancestor 091a00f2f78988754f14417451b4bbecbfa08c0d origin/main exit 0; docs/runbooks/retro.md present in origin/main. No duplicate code written.
CMx-12 | done | Fix already landed in main via PR #1849 (commit eda4eaf6590cbce3df05174131676573c427fd66, docs(runbooks): add human wizard runbook (CMx-12)); git merge-base --is-ancestor eda4eaf6590cbce3df05174131676573c427fd66 origin/main exit 0. Runbook docs/runbooks/human-wizard.md present in main with threshold, four stage fields and five script requirements. No duplicate runbook edit.
CMx-13 | done | Fix already landed in origin/main via PR #1856 (commit 8034ac8e 'docs(runbooks): add handoff runbook for cross-agent context transfer (CMx-13) (#1856)'). docs/runbooks/handoff.md exists in main; git merge-base --is-ancestor 8034ac8e origin/main exit 0. No duplicate code PR; this closes the stale todo card.
CMx-14 | done | Fix already merged: commit 74b32115009f100066ec5131470c740470bd7664 via PR #1857 adds docs/runbooks/design-tree.md; git merge-base --is-ancestor 74b32115 origin/main exit 0; file present in origin/main. No duplicate code.
CMx-15 | done | Шаблон уже влит в PR #1754, коммит 186172ba279c33812cf998588991b2c83b521d19. Проверка git merge-base --is-ancestor 186172ba279c33812cf998588991b2c83b521d19 origin/main вернула код 0. Дубль кода не нужен.
CMx-16 | done | Уже влито в origin/main через PR #1860 (merge commit 9f7df68ccc070fea6a19e9f7e10f3ac760acfa8f, docs(design): add agent doc editing rules (CMx-16)). git merge-base --is-ancestor 9f7df68ccc070fea6a19e9f7e10f3ac760acfa8f origin/main дал exit 0. OwnerFile docs/design/agent-docs-rules.md присутствует в origin/main. Дубль кода не создаётся.
CMx-17 | done | Уже влито в origin/main коммитом ecf36ac3d5e7ef011153293827dcc6920ef1a41d через PR #1859. docs/design/guardrail-smoke.md присутствует в main, merge-base --is-ancestor вернул 0. Дубль кода не создаётся.
CMx-18 | done | already_landed
CMx-19 | done | Already landed in PR #1755 (docs(design): capture prototype as primary source (CMx-19)); merge commit 482b2dca10213fdb1615461602b797bc2c34aea6 is ancestor of origin/main (git merge-base --is-ancestor exit 0); docs/design/prototype-capture.md already on origin/main and satisfies verification. No duplicate code written.
CMx-20 | done | Решение уже влито в origin/main через PR #1760, коммит 3c4c4c3ab8e43b9e109f115feb2f6c1479292ade. Проверка git merge-base --is-ancestor 3c4c4c3ab8e43b9e109f115feb2f6c1479292ade origin/main вернула 0. Дублирующий код не писал, PR переносит только закрытие карточки.
CMx-21 | done | Fix already merged: commit e718298f88938944fa1b1f9ac303adc408b7579e (PR #1758 "docs(templates): add spec template with testing decisions (CMx-21)"). git merge-base --is-ancestor e718298f origin/main exited 0. docs/templates/spec.md already on origin/main; no duplicate code PR needed.
CMx-22 | done | Fix already landed in PR #1757, merge commit 50a6f324fcf953e259c6b50706ff9d0528a1175e; git merge-base --is-ancestor 50a6f324 origin/main exit 0. Runbook docs/runbooks/rephrase.md present in origin/main. No duplicate code written.
CO-01 | done | Fix already landed in origin/main via PR #1694, merge commit 74b04448859f422f89a33f1a99539fe46d33d3cd; git merge-base --is-ancestor 74b04448 origin/main exit 0; PR #1694 touched exactly the 4 OwnerFiles (904 insertions). No duplicate code PR opened.
CO-02 | done | Реализация CO-02 влита в origin/main коммитом 13d5776d87c6c388c64e9d75451ce89524863b9c через PR #1864. git merge-base --is-ancestor 13d5776d origin/main вернул 0. Все три OwnerFiles есть в main: AllowAutofix, тесты пяти причин удержания и runbook. Дубль кода не нужен.
CO-04 | done | Уже влито в main через PR #1863, коммит f6b053d6528812f8d4e806760b2408443ea821f2. git merge-base --is-ancestor f6b053d6 origin/main = 0. Реализация в internal/app/orchestrator/idle_pool.go и idle_pool_test.go присутствует в origin/main. Дубль кода не нужен.
CP-01 | done | already_landed
CP-02 | done | already_landed
CP-03 | done | already_landed
CP-04 | done | already_landed
CP-05 | done | already_landed
CP-06 | done | already_landed
CP-07 | done | already_landed
CP-08 | done | Уже влито в origin/main. Коммит 5c4e604495739699f1938a1ded3f25b91898d778, PR #1862 docs(night-shift): CP-08 tracker adapter contract. Проверка git merge-base --is-ancestor 5c4e604495739699f1938a1ded3f25b91898d778 origin/main вернула exit 0. OwnerFile comment-rules.md уже содержит правку контракта поверх изменений CP-04 (#2010) и CP-07 (#2040). Дубль кода не создаём.
CP-09 | done | Уже влито в origin/main через PR #1861, merge commit a99d57303fab3e3f90a3e36fddb34436e14174fd. git merge-base --is-ancestor a99d57303fab3e3f90a3e36fddb34436e14174fd origin/main дал exit 0. Все три правки CP-09 присутствуют в origin/main: шаг 6 в .opencode/skills/night-shift/SKILL.md, исключение «вопрос к человеку» в .opencode/skills/red-verdict-triage/SKILL.md, раздел «Консультация перед завершением хода» в AGENTS.md с четырьмя артефактами состояния. Правила CP-05 из PR #2021 сохранены. Дубль кода не создаётся.
CP-10 | done | Уже влито в origin/main: PR #1899, коммит a77203632920de0868df336aeab88bf6f6b1b68c 'docs(templates): incremental memory rules in orchestrator task (CP-10)'. git merge-base --is-ancestor a7720363 origin/main возвращает exit 0. Раздел 'Инкрементальный проход по правилам памяти' с полем 'Правила памяти', индексом транскриптов по времени изменения, тремя условиями триггера, дедупликацией, лимитом 12 пунктов и ответом 'No high-signal memory updates.' уже находится в docs/templates/orchestrator-task.md. Проверка карточки проходит. Дубль кода в OwnerFiles не пишу.
CP-11 | done | Реализация CP-11 уже влита в origin/main коммитом 0ca264bb78ea9ead68843fc1c8a0a77b32fb616c через PR #1871 (docs(adapters): CP-11 correspondence table and one-line skip rule). git merge-base --is-ancestor 0ca264bb78ea9ead68843fc1c8a0a77b32fb616c origin/main вернул exit 0. Правило skip-step зафиксировано в docs/templates/executor-task.md:443-456, таблица соответствий в README.md. Дубль кода не пишем.
CP-12 | done | Уже влито в origin/main: PR #1928, коммит 24b92b94c0a42ddae71bb7296e3b8043808187ea 'docs(worktree): read-only audit and pinned chat rule (CP-12) (#1928)'. git merge-base --is-ancestor 24b92b94c0a42ddae71bb7296e3b8043808187ea origin/main возвращает exit 0. Раздел 'Аудит перед чисткой' в AGENTS.md, поле 'Закреплённые worktree' и раздел 'Аудит worktree перед чисткой' в docs/templates/orchestrator-task.md и раздел 9 'Аудит перед чисткой' в docs/research/research-worktree-gc.md уже содержат аудит только на чтение по git worktree list --porcelain, приоритет закреплённого чата над меткой safe и запрет удалять wip:N без решения владельца. Дубль документации в OwnerFiles не пишу.
CP-13 | done | OwnerFiles заняты чужими открытыми PR: AGENTS.md правят PR #1949 (CP-17, head b344dfa870ea03e6dc17b44f2fa24bf235c8ae43) и PR #1951 (PSKX-14, head f22be13830dc6d35f49b48ab79f6699ee734a9c4); docs/templates/orchestrator-task.md закреплён за PSKX-25/CP-12 (ветки origin/fix/pskx25 и origin/fix/cp12). Дубль кода не написан. Premise: git log origin/main --grep=CP-13 -i пуст, CP-13 в origin/main не влит. Закрытие по правилу дубль или чужой PR без повторного изменения.
CP-14 | done | Уже влито через PR #1904, коммит 5437c5899765e97984e5e8d7656240963a328321; git merge-base --is-ancestor 5437c589 origin/main -> exit 0. Секция «Планировщик и воркеры» с шестью правилами и таблицей из пяти узлов присутствует в AGENTS.md на main. Дубль контента не создаётся.
CP-15 | done | Уже влито через PR #1923. Сквош-коммит c83039d1d8ffeaa7baa06d8598821b6563358467 добавил в AGENTS.md строку о переносе выводов depends-on в бриф downstream. git merge-base --is-ancestor c83039d1d8ffeaa7baa06d8598821b6563358467 origin/main вернул 0. Дубль кода не создан.
CP-16 | done | already_landed
CP-17 | done | Уже влито в PR #1949, коммит 3c2fb376937205153c22e61db98939c36084d2cb. git merge-base --is-ancestor 3c2fb376 origin/main: exit 0. Секции «Аудит подавлений не заменяет unslop» в AGENTS.md и «Аудит подавлений» в docs/templates/executor-task.md присутствуют в origin/main. Дубль кода не создан.
CP-18 | done | already_landed
CP-19 | done | OwnerFiles заняты: docs/templates/executor-task.md правит открытый PR #1957 (CP-16, head a219e1ae3b2eacc7399a3d4302b54300ce212be6); AGENTS.md закреплён за агентами PSKX-14/PSKX-28/CP-17. Дубль кода не написан. Premise: git log origin/main --grep=CP-19 -i пуст, CP-19 в origin/main не влит. Закрытие по правилу чужой открытый PR без повторного изменения.
CP-20 | done | already_landed
CP-21 | done | already_landed
CR-01 | done | already_landed
CR-04 | done | Already merged into origin/main via PR #1888 (merge commit 686af432a17cbba121ad9fe06c9d85dfda94c0d3, head 5fec984a95cf1888ed26b3a50c2a860ffcc4a1cb). git merge-base --is-ancestor 686af432a17cbba121ad9fe06c9d85dfda94c0d3 origin/main exit 0. Outcome, OutcomeNotAttempted and priority marker present in internal/app/tasks/night_shift.go. No duplicate code.
CS-01 | done | false premise: already landed in PR #1681, SHA c00635e41735c200ec05ed271af6afd4afddde9c, .opencode/plugins/beacon-project/index.js:90-194
CS-03 | done | Already landed in PR #1877 (merge commit ece843e9b1f8d8e889378df6fec6dddc4039a2e9, merged 2026-11-22T16:35:22Z). git merge-base --is-ancestor ece843e9 origin/main exit 0. Feature present in origin/main: cmd/beacon/spawn.go:174 AgentDefinition struct, cmd/beacon/mcp.go:2365 agent_id. No duplicate code written.
CS-04 | done | Уже влито в origin/main через PR #1733, коммит e0b4cf81afd20d302159b38d4410403632e25c2e. Проверка git merge-base --is-ancestor e0b4cf81afd20d302159b38d4410403632e25c2e origin/main вернула 0. Дубль кода не создаётся.
CSx-01 | done | Уже влито в origin/main: PR #1734, коммит 3e472df64263cdebc287a37b2dc1944eec69363b; git merge-base --is-ancestor 3e472df6 origin/main exit 0. Код дублировать не нужно, PR несёт только статус карточки и доказательство.
CSx-02 | done | Already landed in origin/main via PR #1738, merge commit 171825deded39dd3c72c1c4b9cb4c223bae16f18; git merge-base --is-ancestor 171825de origin/main exit 0. No duplicate code.
CSx-09 | done | Already landed in PR #1761 (merge commit 330dd95a4cbf93d56982dfb1dc1e1146c1b84e05); git merge-base --is-ancestor 330dd95a origin/main exit 0. Fix touches both OwnerFiles: internal/infra/store/events_test.go and internal/infra/store/sessions.go. No duplicate code PR; this PR carries only the card close with evidence.
CSx-11 | done | Уже влито в main через PR #1747, commit 5f0ce57e71e1760cf077f186bba15d7016fcc6d9 ('feat(spawn): mark client-composed prompts for verbatim delivery (CSx-11)'). git merge-base --is-ancestor 5f0ce57e71e1760cf077f186bba15d7016fcc6d9 origin/main дав exit 0. Дубль кода не создаём: PR несёт только закрытие карточки с доказательством.
CV-09 | done | Already merged into origin/main via PR #1874, commit ae36aa0b105ed23d48fdeb9d8ce163e7d55cf52c (git merge-base --is-ancestor exit 0). Implementation in internal/infra/mcp/tool_get_result.go and tool_get_result_test.go is present on main.
CV-15 | done | already_landed
DC-09 | done | # DC-09. Зафиксировать решение по почти мёртвым символам, удаление не предлагать
FTE-01 | done | Already landed: PR #1753, commit 355ba72a feat(mcp): wire ADR 0314 ports in composition root, merged 2026-11-22T14:36:28Z. git merge-base --is-ancestor 355ba72a origin/main exit 0. Diff touched only cmd/beacon/mcp.go and cmd/beacon/mcp_test.go. No duplicate code PR.
FTE-03 | done | Дубль. Открытый PR #1906 (feat(stats): expose shared CI gate snapshot (FTE-03)), ветка fix/fte03, headSHA 44558df18d101e6e8df7d762cb0a1f515fb1a0fd. PR #1906 уже меняет ровно OwnerFiles карточки: internal/infra/mcp/tool_stats.go (+96) и internal/infra/mcp/tool_stats_test.go (+78), плюс docs/tasks/WIRING-1906.md. Решение FTE-03 уже сдано в репозиторий, дубль кода не создаём. Дополнительно: коммит с решением ещё не в origin/main, поэтому merge-base --is-ancestor не применяется; закрытие по факту существующего открытого PR с тем же изменением.
FTE-04 | done | Уже влито в origin/main: коммит 79e70595 (PR #1900) добавил docs/tasks/WIRING-1460.md, WIRING-1582.md, WIRING-1608.md, WIRING-1612.md. git merge-base --is-ancestor 79e70595 origin/main дал exit 0. Все четыре карточки материализованы с заполненными OwnerFiles и разделом Причина. Дубль кода не создаём.
FTE-05 | done | Уже влито в origin/main: PR #1763 (feat(deferred): add beacon deferred done subcommand), коммит 43da266fc01364cb56dbf1e21b485d749181d89a. git merge-base --is-ancestor 43da266f origin/main -> exit 0. Подкоманда done и тесты уже в cmd/beacon/deferred.go и cmd/beacon/deferred_test.go (TestDeferredDoneCLIExitCodes, TestDeferredDoneDispatchJSON). Дубль кода не создаём.
FTE-06 | done | Уже влито в origin/main через PR #1765, merge commit 37618c0a725f36f6460d4791db1ad84d0580d101; git merge-base --is-ancestor 37618c0a725f36f6460d4791db1ad84d0580d101 origin/main вернул код 0. PR меняет ровно OwnerFiles. Код не дублирую.
FTE-07 | done | карточка уже закрыта влитым PR #1907. Дубль кода не нужен.
FTE-08 | done | already_landed
FTE-09 | done | Code already in origin/main: commit 0a04c1ace5a5ce52b1c33cb7edcb6dc068ece9ff, PR #1767 (MERGED 2026-11-22T13:45:05Z), touching cmd/beacon/doctor.go and cmd/beacon/doctor_test.go. git merge-base --is-ancestor 0a04c1ac origin/main exit 0. No duplicate code PR; this PR carries only the card close.
FTE-10 | done | Already landed in origin/main via PR #1796 (merge commit 76fd8da9, head 46d2efad). git merge-base --is-ancestor 76fd8da9 origin/main exit 0. PR modified cmd/beacon/orchestrate.go and internal/app/orchestrator/orchestrator.go with addressable needsRebase (watchID/revision/pr) plus tests. No duplicate code; docs PR carries only the card status close.
FTE-11 | done | Уже влито в origin/main: PR #1801 (fix/fte-11), коммит 75ab4c30c7b7401bdcf23f4d7b833f82323bb202 fix(gc-worktree): apply grace window before removal (FTE-11). merge-base --is-ancestor 75ab4c30 origin/main = 0. Дубли кода нет.
FTE-12 | done | Already landed in main: commit 5a17773d9c5fce8ee1e84fc01ff9d034375982bf (PR #1810) added cmd/beacon/trace.go and cmd/beacon/trace_test.go with 429 lines. git merge-base --is-ancestor 5a17773d origin/main returned exit 0. No duplicate code needed; this PR carries only the card status.
FTE-13 | done | Already landed in PR #1905, merge commit 38d60a6743f28324edd5c75237fd85a692f3f89f; git merge-base --is-ancestor of that SHA against origin/main exit 0. Implementation present in all three OwnerFiles (orphan_wake_total, orphan field, WakeDeliveredOrphan, OrphanWake). No duplicate code.
FTE-14 | done | Premise: FTE-14 already landed in main via PR #1920, merge commit 4d264c8a (feat(observe): add provider_call_total counter with adapter/method/outcome labels). git merge-base --is-ancestor 4d264c8a origin/main exit 0. provider_call_total with adapter/method/outcome labels present in origin/main at internal/infra/observe/{observe.go,metrics.go,coverage_test.go}. No duplicate code written.
FTE-15 | done | Уже влито в main через PR #1943 (SHA 17269244 feat(observe): add retry_open_duration_seconds histogram (FTE-15)); git merge-base --is-ancestor 17269244 origin/main -> exit 0. Гистограмма retry_open_duration_seconds зарегистрирована в internal/infra/observe/metrics.go (HistogramNames) и записывается в observe.go (RetryOpen); тест корзин/sum/count есть в metrics_test.go. Дублирующий код PR не открывается.
FTE-16 | done | FTE-16 already landed in merged PR #1946, commit 830461c5d164e1da045e3a897cb70aba975f5d24; git merge-base --is-ancestor 830461c5d164e1da045e3a897cb70aba975f5d24 origin/main exit 0. Duplicate code PR #1950 closed. Card status travels in a docs-only PR.
FTE-17 | done | already_landed
HD-001 | dropped | # HD-001. Разделить операционный гид и соглашения
HD-002 | dropped | # HD-002. Quick state с исправлениями
HD-003 | dropped | # HD-003. Always-active правила
HD-004 | dropped | # HD-004. Конвенция коммитов
HD-005 | dropped | # HD-005. Feature branch и worktree
HD-006 | done | # HD-006. Поиск до создания
HD-007 | done | # HD-007. Сгенерированные артефакты
HD-008 | dropped | # HD-008. Бриф суб агента
HD-009 | done | # HD-009. Проверка доказательств issue
HD-010 | done | # HD-010. Измерение до выбора
HD-011 | done | # HD-011. Порядок fetch и worktree
HD-012 | done | # HD-012. Короткий отчёт на русском
HD-013 | done | # HD-013. Карта жизненного цикла
HD-014 | done | # HD-014. Режим исследования
HD-015 | done | # HD-015. Быстрый и полный план
HD-016 | done | # HD-016. Второй проход плана
HD-017 | dropped | # HD-017. Прогресс между сессиями
HD-018 | done | # HD-018. Verify до commit
HD-019 | done | # HD-019. Проверка завершённости задач
HD-020 | done | # HD-020. Проверка дрейфа плана
HD-021 | dropped | # HD-021. Conventional commit
HD-022 | done | # HD-022. Связь feat/fix с milestone
HD-023 | done | # HD-023. Отдельный rules gate
HD-024 | done | # HD-024. Три категории артефактов
HD-025 | dropped | # HD-025. Красный тест до фикса
HD-026 | dropped | # HD-026. Минимальный regression check
HD-027 | dropped | # HD-027. Два режима исправления
HD-028 | done | # HD-028. Логирование при исправлении
HD-029 | done | # HD-029. Порог уверенности
HD-030 | dropped | # HD-030. Классификация запроса
HD-031 | done | # HD-031. INSUFFICIENT INFORMATION
HD-032 | dropped | # HD-032. Три проверки читателя
HD-033 | dropped | # HD-033. Project-specific override
HD-034 | dropped | # HD-034. Патч только при проверяемости
HD-035 | dropped | # HD-035. Повтор regression check
HD-036 | done | # HD-036. Ответ на русском с фактами
HD-037 | done | # HD-037. Формат gate result
HD-038 | done | # HD-038. Три статуса
HD-039 | done | # HD-039. Два входа вердикта
HD-040 | done | # HD-040. Блокирующие находки
HD-041 | done | # HD-041. Стабильный id находки
HD-042 | done | # HD-042. Следующий шаг по статусу
HD-043 | done | # HD-043. Валидатор находок
HD-044 | dropped | # HD-044. Read-only context gate
HD-045 | done | # HD-045. Читаемый отчёт первым
HD-046 | done | # HD-046. Строгий JSON
HD-047 | done | # HD-047. Мапирование severity
HD-048 | done | # HD-048. Posледний fence
HD-049 | dropped | # HD-049. Файл проектных правил
HD-050 | dropped | # HD-050. Автозагрузка правил
HD-051 | done | # HD-051. Контракт проверки правил
HD-052 | done | # HD-052. Конфиг путей
HD-053 | done | # HD-053. Язык ответов и артефактов
HD-054 | done | # HD-054. Fallback без конфига
HD-055 | done | # HD-055. Именование артефактов
HD-056 | dropped | # HD-056. Read-only проверка
HD-057 | done | # HD-057. Проверка по git ref
HD-058 | done | # HD-058. Правила как context gate
HD-059 | done | # HD-059. Обновление правил
HD-060 | done | # HD-060. Предупреждение о несовпадении
HD-061 | done | # HD-061. Фазы цикла качества
HD-062 | done | # HD-062. Параллельная подготовка проверок
HD-063 | done | # HD-063. Пороги A и B
HD-064 | done | # HD-064. Явное подтверждение лимита
HD-065 | done | # HD-065. Единственный источник лимита
HD-066 | done | # HD-066. Хеш до Refine
HD-067 | done | # HD-067. Самоулучшение правил
HD-068 | done | # HD-068. Приоритет локального контекста
HD-069 | done | # HD-069. Пакет скилла
HD-070 | done | # HD-070. Сканирование внешних скиллов
HD-071 | done | # HD-071. Lock файл скиллов
HD-072 | done | # HD-072. Остановка по лимиту
HD-073 | done | # HD-073. Конвейер карточек
HD-074 | done | # HD-074. Гейт мержа с условиями
HD-075 | done | # HD-075. Красный PR агенту
HD-076 | done | # HD-076. Rerun только для флейков
HD-077 | done | # HD-077. Перезапуск после смерти
HD-078 | done | # HD-078. Pacing по файлам
HD-079 | done | # HD-079. Крупные изменения последовательно
HD-080 | done | # HD-080. Антиспам guard
HD-081 | done | # HD-081. Spec axis
HD-082 | done | # HD-082. Rebase самому
HD-083 | done | # HD-083. Чинить красное самому
HD-084 | done | # HD-084. Отчёт смены
HD-085 | done | # HD-085. Отдельный docs gate
HD-086 | done | # HD-086. Проверка ссылок и якорей
HD-087 | done | # HD-087. Self-test документа
HD-088 | done | # HD-088. Владелец факта
HD-089 | done | # HD-089. Генерация индекса
HD-090 | done | # HD-090. Сгенерированные артефакты
HD-091 | done | # HD-091. Архив аудита
HD-092 | done | # HD-092. Ограда file:line
HD-093 | done | # HD-093. Обновление в том же PR
HD-094 | done | # HD-094. Не обходить paths-ignore
HD-095 | done | # HD-095. Локальная проверка перед push
HD-096 | dropped | # HD-096. Порядок сбора чисел
HD-097 | done | # HD-097. Контекст перед чтением
HD-098 | done | # HD-098. Сигнатуры вместо файла
HD-099 | done | # HD-099. Точные вызывающие
HD-100 | done | # HD-100. Исчерпывающий поиск
HD-101 | done | # HD-101. Открытие по диапазону
HD-102 | dropped | # HD-102. Обновление графа
HD-103 | dropped | # HD-103. Поиск с явным путём
HD-104 | done | # HD-104. Ответ при пустом индексе
HD-105 | done | # HD-105. Инвентарь скиллов
HD-106 | done | # HD-106. Функциональные аналоги
HD-107 | done | # HD-107. Gate result skill
HD-108 | dropped | # HD-108. Rules gate skill
HD-109 | dropped | # HD-109. Regression-first skill
HD-110 | done | # HD-110. Project skills каталог
HD-111 | done | # HD-111. Night shift аналог
HD-112 | dropped | # HD-112. Conventional commit gate
HD-113 | dropped | # HD-113. Подготовка внешних скиллов
HD-114 | done | # HD-114. Порядок переноса
HD-115 | dropped | # HD-115. Always-load список
HD-116 | dropped | # HD-116. Обновление AGENTS.md
HD-117 | dropped | # HD-117. Сводный ADR о переносе workflow
HD-118 | dropped | # HD-118. Один владелец на правило
HD-119 | done | # HD-119. Проверка целостности правил
HD-120 | done | # HD-120. Обучение агентов новым навыкам
HD-121 | done | # HD-121. Проверка отчёта на стиль
HD-122 | done | # HD-122. Счётчики и финальный формат
IDX-01 | done | # IDX-01. Схема SQLite индекса карточек и импорт
IDX-02 | done | # IDX-02. Типизированный граф и запрет циклов
IDX-03 | done | # IDX-03. Команда сборки индекса и pre-commit
IDX-04 | done | # IDX-04. CI проверка сгенерированного индекса
IDX-05 | done | # IDX-05. MCP инструмент beacon_task_claim
IDX-06 | done | # IDX-06. Схема и регистрация claim инструмента
IDX-07 | done | # IDX-07. Связь карточки с Watch и PR
IDX-08 | done | # IDX-08. Автоматический перевод карточки в done при мерже
IDX-09 | done | # IDX-09. Маркер graphReady в инструментах
IDX-10 | done | # IDX-10. Локальный TUI beacon board
IDX-11 | done | # IDX-11. Вывод производных документов из git
IDX-12 | done | # IDX-12. GC worktree с защитой живых агентов
IDX-13 | done | # IDX-13. Метрика dual-read и условие выхода
IDX-14 | done | # IDX-14. Обновление AGENTS.md, README и шаблонов
IDX-15 | done | # IDX-15. Автоматический GC worktree при терминальном merged
INTG-01 | done | # INTG-01. Зарегистрировать serverRunner в диспетчере команд
INTG-02 | done | # INTG-02. Сделать реестр генераций долговечным и совместимым с портами
INTG-03 | done | # INTG-03. Добавить CRUD расписаний в SQLite и убрать schedules.json
MEM-01 | done | already landed in PR #1462 9e5d3b91
MEM-02 | done | already landed in PR #1588 5b1f470d
MEM-03 | done | already landed in PR #1455 254267c2
MEM-04 | done | already landed in PR #1602 bda51c0f
MEM-05 | done | already landed in PR #1444 b1c64761
MEM-06 | done | already landed in PR #1465 5597372f
MEM-07 | dropped | # MEM-07. Не писать заново автоуборку чистых worktree
MEM-08 | done | already landed in PR #1451 8fc00ad6
MSK-01 | done | already_landed
PF-01 | done | Already landed in PR #1684 (commit 796145cbfc3b38f6d183bc02bae11a161841798c): feat(store): measure SQLite write lock wait. git merge-base --is-ancestor 796145cbfc3b origin/main exit 0.
PF-02 | done | Фикс уже влит в main: PR #1682, коммит 3d263e5e5ffb63c26dd2baee947c23322c98d3fc, git merge-base --is-ancestor 3d263e5e origin/main вернул 0. Дубль кода не пишем.
PF-03 | done | Already landed in PR #1702 (commit 65379980ba2d090c442fe442cbd82329cd15a610): feat(metrics): measure worktree GC duration. git merge-base --is-ancestor 65379980ba2d origin/main exit 0.
PF-04 | done | Уже влито в origin/main: c1edbb79d305a1e86cfb5cf567f8509a2c75cc6e (PR #1719, feat(metrics): report index rebuild duration (PF-04)). Проверка git merge-base --is-ancestor c1edbb79d305a1e86cfb5cf567f8509a2c75cc6e origin/main вернула exit 0. Коммит меняет те же OwnerFiles: internal/infra/taskstore/index.go, cmd/beacon/index.go, docs/perf/README.md. Открытых PR на OwnerFiles нет. Дубль кода не создаётся.
PF-05 | done | Already landed in PR #1710 (commit 5d599a6ef3a7200591fe2f7e9fac1892834a6647): docs(perf): record poll and merge histogram baselines. git merge-base --is-ancestor 5d599a6ef3a7 origin/main exit 0.
PF-06 | done | already_landed
PROTO-01 | done | # PROTO-01. Закрепить одиннадцать advertised-имен и проверить remote-карту
PROTO-02 | done | # PROTO-02. Ввести типизированную SSE-обёртку для событий Watch
PROTO-03 | done | # PROTO-03. Смонтировать аутентифицированный /mcp в один HTTP-сервер
PROTO-04 | done | # PROTO-04. Зафиксировать разделение каналов в шаблонах агентов
PROTO-05 | done | # PROTO-05. Eval на дрейф контракта и стоимость tools/list
PROTO-06 | dropped | # PROTO-06. Не вносить A2A, UTCP и тяжёлый Code Mode в Go-ядро
PROTO-07 | done | # PROTO-07. Поднять graphReady до типизированного сигнала
PROTO-08 | done | # PROTO-08. Привязать registryListSource в composition root или убрать команду
PSK-01 | done | Already landed in PR #1701 (commit 59a72fb984f2ce7a667248764fba71a523c50de7): docs(agents): add optional candidate protocol for disputed design. git merge-base --is-ancestor 59a72fb984f2 origin/main exit 0.
PSK-02 | done | already_landed
PSK-03 | done | Already landed in main via PR #1908, commit 7c4c7e98 (docs(design): формальная модель Watch с проверкой TLC). `git merge-base --is-ancestor 7c4c7e98 origin/main` exit 0. main contains all three OwnerFiles with invariants TypeOK NoDoubleMerge NoSilentVerdictChange BlockLiftedOnlyAfterResolve and control patches double-merge/silent-verdict/early-unblock. Duplicate PR #1915 closed after rebase add/add conflict showed the files already in main.
PSKX-01 | done | Уже влито в origin/main: PR #1739, коммит ec580c2cdc0daf497c2099059257e9f4ae06b597; git merge-base --is-ancestor ec580c2cdc0daf497c2099059257e9f4ae06b597 origin/main -> exit 0. OwnerFiles docs/templates/executor-task.md и AGENTS.md уже содержат раздел «Форма проекта до кода» и отсылку (PSKX-01) в AGENTS.md:139. Дублирующий код не пишем и дублирующий code-PR не открываем.
PSKX-02 | done | Уже в main: PR #1773, merge commit 63de1829168450a32e47aba359066091c3da5cfa; git merge-base --is-ancestor 63de1829 origin/main exit 0. Секция «Порог правила» есть в AGENTS.md:24. Дублирующий код не создаётся.
PSKX-03 | done | Фикс уже в origin/main: коммит 1972a8570b4ac264a43c56c22a3ce7a2cc9a16a3 из влитого PR #1802 (ветка fix/pskx-03, squash docs(verification): семь вопросов к измерению производительности (PSKX-03)). git merge-base --is-ancestor 1972a857 origin/main даёт exit 0. Оба OwnerFiles уже содержат правку: AGENTS.md отсылает к семи вопросам, docs/runbooks/verification.md содержит раздел Семь вопросов к измерению производительности. Дубль кода не нужен.
PSKX-04 | done | already_landed
PSKX-05 | done | Уже влито в origin/main: PR #1740, squash-коммит da1d710c54302a8e20e6a012b20d06628f1265f6; git merge-base --is-ancestor da1d710c54302a8e20e6a012b20d06628f1265f6 origin/main -> exit 0. docs/runbooks/verification.md уже содержит раздел «Реестр проверок» с колонкой «Commit синхронизации». Дубль кода не создаётся.
PSKX-06 | done | Implementation already in origin/main. Rule "Карта возможностей и полнота прогона" added to docs/e2e/README.md by merged PR #1744, commit 938b266a6063b61872eab1d9e04306884e87a269. git merge-base --is-ancestor 938b266a origin/main exit=0. No duplicate code written.
PSKX-07 | done | Fix already merged into origin/main via PR #1745 (merge commit 731cd50a38205ee510a108845344ec69b48b4eb5); git merge-base --is-ancestor 731cd50a origin/main exit 0. No open duplicate PR. Closing card, status change to reach main via docs PR.
PSKX-08 | done | Уже влито в origin/main: коммит ab6c234a, PR #1791, merge-base --is-ancestor exit 0. Раздел «Объяснение подсистемы» присутствует в docs/templates/executor-task.md. Дубль кода и PR не создаются.
PSKX-09 | done | already_landed
PSKX-10 | done | Premise 6b: fix already merged. PR #1797, commit 33e43fc621fdcdca6825dd956488a727d04d93a4; git merge-base --is-ancestor 33e43fc621fdcdca6825dd956488a727d04d93a4 origin/main exit 0. No duplicate code PR; this close carries card status only.
PSKX-11 | done | Fix already landed in origin/main through PR #1809, commit 35545a0afe33f6d875be6a3338964491274a4b8e, docs(verification): четыре исхода провала проверки (PSKX-11). git merge-base --is-ancestor 35545a0a origin/main exits 0. docs/runbooks/verification.md carries the four-outcome table. No duplicate code PR; this close records that the work shipped.
PSKX-12 | done | Already merged into origin/main via PR #1898, merge commit 29c2e64514792ae9eeeb4a91fdb13032409be96b. git merge-base --is-ancestor 29c2e64514792ae9eeeb4a91fdb13032409be96b origin/main exit 0. docs/e2e/README.md already carries the 'Аудит прогона' section with all three outcomes and live-run invariants; no duplicate code change.
PSKX-13 | done | already_landed
PSKX-14 | done | Already landed in PR #1951 (merge commit d9d5c871d86879078f7f30666ec327917f3f968d); AGENTS.md section present in origin/main line 242; git merge-base --is-ancestor exit 0.
PSKX-15 | done | already_landed
PSKX-16 | done | PSKX-16 уже влит в main через PR #1956 (merge commit c1e7dec78f9161af100e3b9f1bb9970fb8bf31ba). `git merge-base --is-ancestor c1e7dec7 origin/main` exit 0. Секция «Углубление задачи перед выдачей» уже присутствует в docs/templates/orchestrator-task.md на origin/main (строки 136-152, содержит «углублённая формулировка» и «сырой материал»). Дублирование изменения документации не требуется.
PSKX-17 | done | already_landed
PSKX-18 | done | already_landed
PSKX-19 | done | already_landed
PSKX-20 | done | already_landed
PSKX-21 | done | already_landed
PSKX-22 | done | already_landed
PSKX-23 | done | already_landed
PSKX-24 | done | Фикс уже влит в main через PR #1933, squash-коммит 67c710898afc81138bb4deb0c57451c88bf21f79. git merge-base --is-ancestor 67c710898afc81138bb4deb0c57451c88bf21f79 origin/main вернул 0. Раздел «Вторая инструкция» с источником PSKX-24 уже есть в AGENTS.md на origin/main. Дубль кода и дубль code-PR не создаются. Этот PR переносит только закрытие карточки.
PSKX-25 | done | Already landed in PR #1941, commit 6236262f25e86696740fc51bbe69b09b80678e33; git merge-base --is-ancestor 6236262f origin/main exit 0; sections present in current main (README.md:360, docs/templates/orchestrator-task.md:118). No duplicate code.
PSKX-26 | done | Already landed in origin/main via PR #1935, commit 809debc697022bb9b8aeecd919bd0a18d0a7ab9c; git merge-base --is-ancestor exit 0. No duplicate code PR written.
PSKX-27 | done | already_landed
PSKX-28 | done | Already landed in origin/main: full SHA 9a0038c7 docs(agents): PSKX-28 minimal diff, one decision per source (#1942); git merge-base --is-ancestor 9a0038c7 origin/main exit 0; AGENTS.md change present, no duplicate code PR.
PSKX-29 | done | already_landed
PSKX-30 | done | already_landed
PSKX-31 | done | already_landed
PSKX-32 | done | Уже влито в origin/main: коммит 4f1bfddb docs(verification): add two-edit test criterion (PSKX-32) (#1902), PR #1902. git merge-base --is-ancestor 4f1bfddb origin/main вернул exit 0. OwnerFile docs/runbooks/verification.md уже содержит критерий теста «Две правки». Дубль кода не создаём.
Q-A-01 | done | # Q-A-01. Автостарт и диагностика поллера внутри beacon mcp
Q-A-02 | done | # Q-A-02. Убийца сирот opencode serve и зависших процессов
Q-A-03 | done | # Q-A-03. Детект устаревшей базы stale_base вместо keep_working
Q-A-04 | done | # Q-A-04. Единая правда о восьми инструментах и валидация batch-apply
Q-B-01 | done | # Q-B-01. Сквозная корреляция в логах и живые метрики
Q-B-02 | done | # Q-B-02. Барьер singleflight без runtime.Gosched
Q-B-03 | done | # Q-B-03. Восстановление dedupe индекса и Conflict против Duplicate
Q-B-04 | done | # Q-B-04. Вторая линия защиты wake и токены слотов
Q-C-01 | done | # Q-C-01. Кэшированный failureExcerpt в beacon_get_result
Q-C-02 | done | # Q-C-02. Завершение открытых карточек RS
Q-C-03 | done | # Q-C-03. Производительность и устранение самописцев
Q-D-01 | done | # Q-D-01. Финальный аудит оставшихся todo карточек
Q-D-02 | done | # Q-D-02. beacon init и пилот на соседнем репозитории
Q-D-03 | done | # Q-D-03. OpenAPI 3.1 и канон ACP и Telegram
RS-CMP-01 | done | # RS-CMP-01. Проверка мержа против свежего main
RS-CMP-02 | done | # RS-CMP-02. Токен слота по сигналу завершения
RS-CMP-03 | done | # RS-CMP-03. Явные ступени доставки wake
RS-CMP-04 | done | # RS-CMP-04. Безопасность авто-мержа по обязательным проверкам
RS-CMP-05 | done | # RS-CMP-05. Классификация восстановления снапшота
RS-CMP-06 | done | # RS-CMP-06. Типированный human gate вместо needs_input
RS-EVAL-01 | done | # RS-EVAL-01. Заменить runtime.Gosched на барьер joiningBatchGroup
RS-EVAL-02 | done | # RS-EVAL-02. pass^5 для стохастических eval и карантин с сроком
RS-EVAL-03 | done | # RS-EVAL-03. Именованные таймауты и smoke тест старта evalserver
RS-EVAL-04 | done | # RS-EVAL-04. Property based тесты контракта beacon-gates
RS-EVAL-05 | done | # RS-EVAL-05. Разделение Red и Broken на уровне CI
RS-EVAL-06 | done | # RS-EVAL-06. CI-only probes для сетевого пропуска R01
RS-MCP-01 | done | # RS-MCP-01. Контрактный тест стабильности tools/list
RS-MCP-02 | done | # RS-MCP-02. CI-гейт качества описаний инструментов
RS-MCP-03 | done | # RS-MCP-03. Лимиты размера ответа в инструментах чтения
RS-MCP-04 | done | # RS-MCP-04. Идемпотентность и состояние outcome_unknown
RS-MCP-05 | done | # RS-MCP-05. Динамическая версия в описании инструмента
RS-MCP-06 | done | # RS-MCP-06. Срок жизни watchID как непрозрачного handle
RS-MCP-07 | done | # RS-MCP-07. Замер стоимости перехода на официальный Go SDK
RS-OBS-01 | done | # RS-OBS-01. Корреляционные поля в структурированных логах цикла
RS-OBS-02 | done | # RS-OBS-02. Имена спанов по GenAI конвенциям в локальном экспортере
RS-OBS-03 | done | # RS-OBS-03. Метрики цикла и доставки в beacon metrics
RS-OBS-04 | done | # RS-OBS-04. Политика NO_CONTENT для телеметрии
RS-WK-01 | done | # RS-WK-01. Per-adapter circuit breaker для доставки wake
RS-WK-02 | done | # RS-WK-02. Явный идентификатор уведомления в beacon-event
RS-WK-03 | done | # RS-WK-03. Восстановление dedupe индекса из журнала
RS-WK-04 | done | # RS-WK-04. Уже обработанная доставка как успешный ответ
RS-WK-05 | done | # RS-WK-05. Валидация и ограничение replay курсора
RS-WK-06 | done | # RS-WK-06. Негативный контроль против дедупликации по времени
RS2-CMP-01 | done | # RS2-CMP-01. Перепроверка мержа против свежего main
RS2-CMP-03 | done | # RS2-CMP-03. Явные ступени доставки wake в обязательстве
RS2-EVAL-01 | done | # RS2-EVAL-01. Заменить runtime.Gosched на барьер в singleflight
RS2-EVAL-03 | done | # RS2-EVAL-03. Именованные таймауты и smoke тест evalserver
RS2-EVAL-05 | done | # RS2-EVAL-05. Отдельная обязательная проверка для Broken
RS2-MCP-06 | done | # RS2-MCP-06. Срок жизни watchID как handle
RS2-OBS-01 | done | # RS2-OBS-01. Корреляционные поля в записи цикла
RS2-OBS-03 | done | # RS2-OBS-03. Метка исхода доставки в метриках
RS2-WK-03 | done | # RS2-WK-03. Восстановление dedupe индекса из журнала
RS2-WK-04 | done | # RS2-WK-04. Различить Duplicate и Conflict в приёмнике
S1.001 | dropped | # S1.001. Entry point и жизненный цикл сервера
S1.002 | dropped | # S1.002. Разрешение конфигурации и каталогов
S1.003 | dropped | # S1.003. SQLite WAL и бегунец миграций
S1.004 | dropped | # S1.004. Базовая схема домена
S1.005 | dropped | # S1.005. Идентичность и типы домена
S1.006 | dropped | # S1.006. REST роутер и контракт ошибок
S1.007 | dropped | # S1.007. Внедрение сервисов в request context
S1.008 | dropped | # S1.008. Bearer token и проверка loopback
S1.009 | dropped | # S1.009. Graceful shutdown и дренаж
S1.010 | dropped | # S1.010. Проекция статуса сервера
S2.001 | dropped | # S2.001. EventLog с монотонным seq
S2.002 | dropped | # S2.002. Транзакционный notify envelope
S2.003 | dropped | # S2.003. Доставка подписчикам после коммита
S2.004 | dropped | # S2.004. Защита от отравленного события
S2.005 | done | # S2.005. SSE реплей и Last-Event-ID
S2.006 | dropped | # S2.006. Снимки для устаревших курсоров
S2.007 | dropped | # S2.007. Обслуживание журнала
S2.008 | dropped | # S2.008. Версия схемы события
S2.009 | dropped | # S2.009. Outbox и EffectWorker
S2.010 | dropped | # S2.010. Квитанции по clientRequestId
S2.011 | dropped | # S2.011. Защита терминального состояния
S2.012 | dropped | # S2.012. Разделение turn и финализации
S3.001 | dropped | # S3.001. Модель исполнителя
S3.002 | dropped | # S3.002. Регистрация и heartbeat
S3.003 | dropped | # S3.003. Интерфейс ExecutorAdapter
S3.004 | dropped | # S3.004. Адаптер OpenCode
S3.005 | done | # S3.005. Адаптер ACP
S3.006 | dropped | # S3.006. Адаптер локального CLI
S3.007 | dropped | # S3.007. Stub исполнитель для тестов
S3.008 | dropped | # S3.008. Переговоры о capabilities
S3.009 | dropped | # S3.009. Надзор за арендой executor
S3.010 | dropped | # S3.010. Выдача и отзыв токенов
S3.011 | dropped | # S3.011. Бэкпрешер и глубина очереди
S3.012 | dropped | # S3.012. Здоровье executors в статусе
S4.001 | dropped | # S4.001. Модель сессии и проекта
S4.002 | dropped | # S4.002. Run и отдельная финализация
S4.003 | dropped | # S4.003. Операторский чат
S4.004 | dropped | # S4.004. Lineage дочерних thread
S4.005 | dropped | # S4.005. Режимы отправки auto, queue, steer, restart
S4.006 | dropped | # S4.006. Guard на ручной ввод
S4.007 | dropped | # S4.007. Очередь follow-up
S4.008 | dropped | # S4.008. Долговечный wait
S4.009 | dropped | # S4.009. Wait прерывается при нужде в человеке
S4.010 | dropped | # S4.010. Вопросы и одобрения в чате
S4.011 | dropped | # S4.011. Отмена и прерывание передачи вниз
S4.012 | dropped | # S4.012. Чтение thread по курсору
S4.013 | dropped | # S4.013. Ссылки на thread
S4.014 | dropped | # S4.014. Происхождение сообщений
S4.015 | dropped | # S4.015. Snooze и pin
S4.016 | dropped | # S4.016. Делегирование с узким контекстом
S4.017 | dropped | # S4.017. Идемпотентная финализация delegation
S4.018 | dropped | # S4.018. Фоновые задачи удерживают thread
S4.019 | dropped | # S4.019. Границы между проектами
S4.020 | dropped | # S4.020. Изоляция worktree
S5.001 | dropped | # S5.001. Идентификация проекта
S5.002 | dropped | # S5.002. Реестр workspace
S5.003 | dropped | # S5.003. First-wins флаги workspace
S5.004 | dropped | # S5.004. Время жизни workspace
S5.005 | dropped | # S5.005. Проектный config overlay
S5.006 | dropped | # S5.006. Каталог состояния Beacon
S5.007 | dropped | # S5.007.  Миграция состояния старого Beacon
S5.008 | dropped | # S5.008. Git checkpoints проекта
S5.009 | dropped | # S5.009. Общий кэш зависимостей worktree
S5.010 | dropped | # S5.010. Проектные навыки и context overlay
S6.001 | dropped | # S6.001. REST проекты
S6.002 | dropped | # S6.002. REST сессии
S6.003 | dropped | # S6.003. REST история и сообщения
S6.004 | dropped | # S6.004. REST executor registration
S6.005 | dropped | # S6.005. REST wake и follow
S6.006 | dropped | # S6.006. REST вопросы и одобрения
S6.007 | dropped | # S6.007. SSE gateway
S6.008 | done | # S6.008. REST health, version, metrics, audit
S6.009 | dropped | # S6.009. MCP server поверх доменных сервисов
S6.010 | dropped | # S6.010. MCP инструменты expect и result
S6.011 | dropped | # S6.011. MCP инструменты сессий
S6.012 | dropped | # S6.012. MCP инструменты executor
S6.013 | dropped | # S6.013. MCP мост watch bridge
S6.014 | dropped | # S6.014. MCP планировщик
S6.015 | dropped | # S6.015. MCP вопросы и одобрения
S6.016 | dropped | # S6.016. MCP статус проекта
S6.017 | dropped | # S6.017. Версия wire и legacy поля
S6.018 | dropped | # S6.018. HTTP клиент TUI
S6.019 | dropped | # S6.019. TUI views
S6.020 | dropped | # S6.020. Control socket TUI
S6.021 | dropped | # S6.021. Channel ingress
S6.022 | dropped | # S6.022. Проверка channel payload
S6.023 | dropped | # S6.023. Маршрутизация channel
S6.024 | dropped | # S6.024. Channel reply routing
S6.025 | dropped | # S6.025. Webhook задачи
S6.026 | dropped | # S6.026. CLI как тонкий клиент
S6.027 | dropped | # S6.027. Двусторонние каналы
S7.001 | dropped | # S7.001. Восстановление после рестарта
S7.002 | dropped | # S7.002. Retry и retirement эффектов
S7.003 | dropped | # S7.003. Единичный инстанс
S7.004 | dropped | # S7.004. Метрики
S7.005 | dropped | # S7.005. Trace id и структурированные логи
S7.006 | dropped | # S7.006. Audit log
S7.007 | dropped | # S7.007. Документ архитектуры с проверяемыми числами
S7.008 | dropped | # S7.008. Guard сгенерированных файлов
S7.009 | dropped | # S7.009. Сценарный harness
S7.010 | dropped | # S7.010. Eval wake и follow
S7.011 | dropped | # S7.011. Seeded regression пары
S7.012 | dropped | # S7.012. Property tests
S7.013 | dropped | # S7.013. Нагрузочный тест 15+ executors
S7.014 | dropped | # S7.014. Бюджет footprint
S7.015 | dropped | # S7.015. План миграции с MCP-only на сервер
S7.016 | dropped | # S7.016. Установка фонового сервера
S7.017 | dropped | # S7.017. Удалённый доступ
S7.018 | dropped | # S7.018. Отчёт о статусе миграции в чат
SB-01 | done | Already landed in PR #1704 (commit 18caa7f31a6b9815ccfff40540492d0436a3812f): feat(domain): reject external checks that ran no tests. git merge-base --is-ancestor 18caa7f31a6b origin/main exit 0.
SB-02 | done | Already landed in origin/main via PR #1706, commit 8ac9665f73770001e76a2155f2116009154be27a (feat(domain): separate test exit code from wrapper cleanup). `git merge-base --is-ancestor 8ac9665f origin/main` exit 0. No open SB-02 PR. No duplicate code written.
SB-03 | done | SB-03 уже влита в origin/main через PR #1910, merge commit 280bc11d87e170eea90732e9c3f26a3d764f05af. git merge-base --is-ancestor 280bc11d87e170eea90732e9c3f26a3d764f05af origin/main вернул 0. Оба OwnerFiles изменены в этом коммите. Открывается docs-only PR со статусом и доказательством, дубль кода не создаётся.
SB-04 | done | Реализация уже влита в main: коммит 80d638197683b1ccc25385adb7c0487ca5cddc5c через PR #1925. git merge-base --is-ancestor 80d63819 origin/main вернул exit 0. Коммит меняет только internal/testsystem/harness.go. Дубль кода не пишем, PR несёт статус карточки и доказательство.
SB-05 | done | already_landed
SB-06 | done | already landed: PR #1805 (https://github.com/refusedguy/beacon/pull/1805), merge commit fa98fedb4facb9e146175491a21cc9114d4b57d8; git merge-base --is-ancestor fa98fedb4facb9e146175491a21cc9114d4b57d8 origin/main exit 0
SB-08 | done | Реализация уже в main. Коммит 85199270c1409d05d0862a6cc2182f51602e131a вошёл через PR #1932. Проверка git merge-base --is-ancestor 85199270 origin/main вернула код 0. В internal/domain/verdict.go уже есть ExternalPartial, PARTIAL и Progress. Дубль кода не нужен.
SB-10 | done | already_landed
SB-12 | done | Already landed in PR #1756, merge commit 083b68a771f99dc3953ea9d87400d4a71b8ececb. merge-base --is-ancestor 083b68a7 origin/main exit 0; disjoint outcome report and schema version 2 present in tool_stats.go/schema.go. No duplicate code PR.
SB-14 | done | already_landed
SB-15 | done | already_landed
SB-16 | done | already_landed
SB-17 | done | already_landed
SB-18 | done | already_landed
SL-01 | done | Already landed in PR #1697 (commit af611829a0ec32440de666ec950295039ea74842): feat(slots): configurable 12-17 budget visible in workload. git merge-base --is-ancestor af611829a0ec origin/main exit 0.
SL-02 | done | already_landed
SL-04 | done | Уже влито в main через PR #1954 коммитом 386e991e313d27f7e19b0a26bcbae0b0eb063a6a; merge-base --is-ancestor 386e991e313d27f7e19b0a26bcbae0b0eb063a6a origin/main вернул 0. Реализация в cmd/beacon/spawn.go и spawn_test.go присутствует в origin/main, дубль кода не нужен.
SL-08 | done | already_landed
ST-02 | done | Already landed in main via PR #1726, commit 2f463d3af6e39faf909eeb1943d81bf89d0dd246 (fix(gates): detect shallow history in cardindex (ST-02)). PR #1726 state MERGED at 2026-11-22T13:15:24Z; its diff contains both OwnerFiles tools/beacon-gates/cardindex/check.go and docs/research/research-stuck-pr-taxonomy.md. git merge-base --is-ancestor 2f463d3af6e39faf909eeb1943d81bf89d0dd246 origin/main exit 0. No duplicate code written; card left todo on main, so this PR carries only the card close.
ST-03 | done | already_landed
ST-04 | done | Already landed in PR #1712 (commit f4fc32fa5587962e5efba375bcfe44f395fa9073): fix(orchestrator): retry transient merge failures with bounded budget. git merge-base --is-ancestor f4fc32fa5587 origin/main exit 0.
ST-05 | done | False premise, superseded by IDX-11 (PR #1622, 5a72ede9882c5882f989f0f89021ecc228b66ec4). That commit untracked and gitignored docs/ROADMAP_STATUS.md and made the cardindex gate treat a missing index as a no-op (tools/beacon-gates/cardindex/check.go:84-89). tools/roadmap-sync/main.go updateRoadmap already updates the three count lines in a local copy via --write and skips a missing file. ST-05 acceptance requires committing a stale ROADMAP_STATUS.md, which reverses IDX-11. No duplicate code written.
ST-06 | done | Уже влито в main через PR #1934, merge commit 94a1f8f3a36446ade807adc06d63ab7b73d6993c. Проверка git merge-base --is-ancestor 94a1f8f3a36446ade807adc06d63ab7b73d6993c origin/main вернула код 0. Реализация cycle-start head check лежит в ownerFiles в main. Дубль кода не создаётся.
ST-07 | done | false premise: orchestrator already bound and diagnosed, PR #1690 docs only, see inspect.go:282, orchestrator.go:141
T0.1 | done | # T0.1. Go каркас и замер RSS на 15/30/60 горутинах
T0.2 | done | # T0.2. Цена GraphQL батча с merge-полями
T0.3 | done | # T0.3. Спайк пробуждения: S1 против S2 на V2 2.3.0
T0.4 | done | # T0.4. Спайт SQLite modernc: миграции, один writer и импорт квитанции
T0.5 | done | # T0.5. Бинарник beacon-gates с тремя гейтами и self-test
T0.6 | done | # T0.6. Бинарник beacon с шестью подкомандами
T0.7 | done | # T0.7. Бинарник beacon task: list, new, take
T1.1 | done | # T1.1. Модуль domain: типы Watch, Obligation, Subscriber, Verdict
T1.10 | done | # T1.10. Импорт квитанций из receipts.jsonl в базу
T1.11 | done | # T1.11. Архивация закрытых Watch и контроль роста базы
T1.12 | done | # T1.12. Redaction на записи и версия схемы в строках журнала
T1.2 | done | # T1.2. Машина состояний Watch и переходы
T1.3 | done | # T1.3. Revision, ключ обязательства и идемпотентность watchID+revision+subscriberID
T1.4 | done | # T1.4. Fan-out подписчиков на один PR
T1.5 | done | # T1.5. Движок вердиктов как чистая функция
T1.6 | done | # T1.6. Property-тесты на 10 невариантов
T1.7 | done | # T1.7. Слой store на SQLite modernc: схема, миграции user_version, один writer
T1.8 | done | # T1.8. Атомарная запись вердикта, обязательства и wake-intent
T1.9 | done | # T1.9. persist_error и loadErrors в beacon_stats
T2.1 | done | # T2.1. MCP-сервер на go-sdk и реестр семи инструментов
T2.10 | done | # T2.10. GitHub REST клиент на go-github: ETag, 304 и пинок mergeable UNKNOWN
T2.11 | done | # T2.11. GitHub GraphQL клиент на отдельном http.Client
T2.12 | done | # T2.12. Общий счётчик поинтов GitHub
T2.13 | done | # T2.13. Tier-1 опрос каждые 10 или 30 секунд и бюджет-контроллер
T2.14 | done | # T2.14. Tier-2: contexts(first:50), фрагменты, isRequired и пагинация
T2.15 | done | # T2.15. Кэш по headSHA плюс дайджест suite с инвалидацией при re-run
T2.16 | done | # T2.16. Фикстуры особых случаев GitHub
T2.17 | done | # T2.17. Нормализация ошибок GitHub: 200-with-errors, REST 403/429 и secondary backoff
T2.18 | done | # T2.18. Два моста на один watch_pr
T2.19 | done | # T2.19. Статус и курсор вместо истории
T2.2 | done | # T2.2. Output schemas и единый формат ошибки
T2.20 | done | # T2.20. Честная доставка follow-up
T2.21 | done | # T2.21. Артефакт: ветка, PR или файл в worktree
T2.22 | done | # T2.22. Идемпотентность по clientRequestId
T2.23 | done | # T2.23. Квитанция как записанное намерение
T2.24 | done | # T2.24. Прерывание ожидания когда нужен человек
T2.3 | done | # T2.3. Чтение _meta ai.opencode/sessionID
T2.4 | done | # T2.4. beacon_watch_pr: идемпотентная регистрация и фан-аут
T2.5 | done | # T2.5. beacon_wait_result: ограниченный long-poll
T2.6 | done | # T2.6. beacon_get_result и beacon_ack: readAt и claimedAt
T2.7 | done | # T2.7. beacon_list_running и beacon_stats как чистое чтение
T2.8 | done | # T2.8. beacon_cancel_watch: destructive отмена
T2.9 | done | # T2.9. Контрактный тест семи инструментов и байт-бюджет tools/list
T3.1 | done | # T3.1. Плагин beacon-bridge.js одним файлом
T3.10 | done | # T3.10. Эскалация rung 1 и rung 2, одна запись в ESCALATIONS.md на эпизод
T3.100 | done | # T3.100. Проверка квот до отправки задачи
T3.101 | done | # T3.101. Exactly-once уведомление о конце задачи
T3.102 | done | # T3.102. Событийный job_wait с фильтром и idempotency
T3.103 | done | # T3.103. Порог синхронного вызова 30/120 секунд
T3.104 | done | # T3.104. beacon why цепочка блокировок
T3.105 | done | # T3.105. Подключить beacon why к dispatch
T3.106 | done | # T3.106. Wait возвращает причину останова
T3.107 | done | # T3.107. must_not_redo и must_preserve в handoff
T3.108 | done | # T3.108. boot id для проверки живости владельца
T3.109 | done | # T3.109. Heartbeat возвращает false при потере аренды
T3.11 | done | # T3.11. beacon agents: список агентов opencode
T3.110 | done | # T3.110. Ремонтопригодная ошибка с recoveryHint
T3.111 | done | # T3.111. Строгий парсер вердикта 0/3/4
T3.112 | done | # T3.112. Вердикт текущего запуска проверки
T3.113 | done | # T3.113. Handle фоновой задачи wait output notify
T3.114 | done | # T3.114. Команда gc для просроченных аренд
T3.115 | done | # T3.115. Дешёвый watcher вместо опроса моделью
T3.116 | done | # T3.116. Fence token и enqueue в одной транзакции
T3.117 | done | # T3.117. Вердикт из двух условий: величина и значимость
T3.118 | done | # T3.118. Аренда с коротким продлением без сокращения срока
T3.119 | done | # T3.119. Журнал задачи: запрос, исход, отчёт
T3.12 | done | # T3.12. beacon watch: таблица вотчей и сессий
T3.120 | done | # T3.120. Завершение без evidence отвергается
T3.121 | done | # T3.121. Транзакционная сверка токена и deadline
T3.122 | done | # T3.122. Контракт tasks/get с TTL и уведомлениями
T3.123 | done | # T3.123. WAL перед ответом принято
T3.124 | done | # T3.124. Детекция дубля промпта и operationId
T3.125 | done | # T3.125. Батчинг событий 200мс в одно пробуждение
T3.126 | done | # T3.126. wait after SEQ в CLI
T3.127 | done | # T3.127. Телеметрия ожидания в stats
T3.128 | done | # T3.128. Двухуровневая шина сигнал и журнал с курсором
T3.129 | done | # T3.129. Адресный doorbell пробуждение
T3.13 | done | # T3.13. beacon attach: переход в сессию
T3.130 | done | # T3.130. Проверка утверждений по git и CI
T3.131 | done | # T3.131. Approve reject и resume агента
T3.132 | done | # T3.132. Именованные очереди с atomic continuation
T3.133 | done | # T3.133. Маршрутизация к агенту просящему внимания
T3.134 | done | # T3.134. agent_acp_wait вместо polling
T3.135 | done | # T3.135. Guarded heartbeat по изменению и гейту
T3.136 | done | # T3.136. Зелёный но заблокирован отдельный вердикт
T3.137 | done | # T3.137. fix-checks отдельная задача на том же worktree
T3.138 | done | # T3.138. FIFO с паузой resume cancel crash-safe
T3.139 | done | # T3.139. Verified completion явной командой приёмки
T3.14 | done | # T3.14. Очередь и события в одной транзакции
T3.140 | done | # T3.140. Исправить beacon agents: источник агентов из session.list
T3.141 | done | # T3.141. beacon watch --live через SSE /api/event
T3.142 | done | # T3.142. beacon spawn: создать сессию с parentID и agent
T3.143 | done | # T3.143. beacon deliver: WriteToolResult по вердикту с честным delivery
T3.144 | done | # T3.144. Тонкий HTTP-клиент к service.json с basic auth
T3.145 | done | # T3.145. Dispatch и usage для spawn deliver --live
T3.146 | done | # T3.146. e2e: полный MCP-цикл на живом opencode
T3.147 | done | # T3.147. eval: двухфазный локальный CI гейт
T3.148 | done | # T3.148. Парсить model в сессиях opencode как объект или строку
T3.149 | done | # T3.149. Подключить beacon deliver к dispatch
T3.15 | done | # T3.15. Общий кэш зависимостей для worktree
T3.150 | done | # T3.150. POST-методы в opencode client (CreateSession, Prompt)
T3.151 | done | # T3.151. Fallback-спавн opencode serve при отсутствии service.json
T3.152 | done | # T3.152. Подключить реальный POST-клиент к spawn и deliver
T3.153 | done | # T3.153. Адресный wake по @mention с NOTIFY хосту
T3.154 | done | # T3.154. Wake как ссылка на Task без текста события
T3.155 | done | # T3.155. Батчевая запись durable queue в SQLite
T3.156 | done | # T3.156. Измеримая регрессия verdict через eval
T3.157 | done | # T3.157. Подтверждение continued после handoff
T3.158 | done | # T3.158. Мультирепо задача с отдельным worktree
T3.159 | done | # T3.159. Постоянная инструкция для агентов по Beacon MCP
T3.16 | done | # T3.16. Комментарий из diff как форма steer
T3.160 | done | # T3.160. Проверенный вердикт по порядку событий хода
T3.161 | done | # T3.161. Пересечение правок по функции между открытыми ветками
T3.162 | done | # T3.162. Обязательное правило с полномочием человека
T3.163 | done | # T3.163. Факт завершения хода с защитой от повторного входа
T3.164 | done | # T3.164. Отложенное решение пробуждения с жёстким закрытием
T3.165 | done | # T3.165. Адресная инъекция wake один раз за сессию
T3.166 | done | # T3.166. Граф зависимостей задач с вычисляемым статусом
T3.167 | done | # T3.167. Параллельная оркестрация через ветки графа задач
T3.168 | done | # T3.168. Проекция состояния агентов для инструментов чтения
T3.169 | done | # T3.169. Одна точка публикации журнала состояния
T3.17 | done | # T3.17. Подключить CLI-команды к dispatch
T3.170 | done | # T3.170. Ack после durable записи журнала
T3.171 | done | # T3.171. Честные исходы восстановления и один recapture
T3.172 | done | # T3.172. Типизированное состояние Watch с чтением по ветке и по времени
T3.173 | done | # T3.173. Единый гейт переходов на границе MCP с серверным актором
T3.174 | done | # T3.174. Раннее предупреждение о пересечении веток и preview перед слиянием
T3.175 | done | # T3.175. Тест нормализации статусов gh CI
T3.176 | done | # T3.176. Каркас V2-плагина OpenCode с границей проекта
T3.177 | done | # T3.177. Регистрация MCP-сервера Beacon через ctx.mcp.transform
T3.178 | dropped | # T3.178. Проектный контекст в сессиях через инструкции и хуки
T3.179 | done | # T3.179. Unit и eval проверка доставки wake
T3.18 | done | # T3.18. Рабочий wake в плагине репозитория
T3.180 | done | # T3.180. Адаптер ACP для доставки wake
T3.181 | done | # T3.181. Адаптер OpenCode для доставки wake
T3.182 | done | # T3.182. Вызов wake-адаптера из CI-моста по терминальному вердикту
T3.183 | done | # T3.183. Project-scoped указатель wake
T3.19 | done | # T3.19. seq и счётчики missing/duplicates для потока событий
T3.2 | done | # T3.2. Канал S2: чтение events.jsonl с курсором offset+epoch
T3.20 | done | # T3.20. Совместимость с тремя диалектами MCP Tasks
T3.200 | done | # T3.200. Прямой wake сессии через POST session.prompt
T3.201 | done | # T3.201. Побуждение отложенной задачи через PATCH inbox
T3.202 | done | # T3.202. Зафиксировать правила steer и queue и поведение interrupt
T3.203 | done | # T3.203. Побуждение через CLI opencode api
T3.204 | done | # T3.204. Негативный контроль для теста wake
T3.205 | done | # T3.205. Сравнить wake-механизмы Pi, Kilo Code и Cursor
T3.206 | done | # T3.206. Ввести deliveryId и журнал доставок
T3.207 | done | # T3.207. Разделить приём wake и вердикт через 202
T3.208 | done | # T3.208. Events как подсказка, состояние как источник истины
T3.209 | done | # T3.209. Lease obligation с release и fail
T3.21 | done | # T3.21. needs_input состояние задачи с ответом submit(continue_of)
T3.210 | done | # T3.210. Интерфейс WakeAdapter и контракт указателя
T3.211 | done | # T3.211. Три реестра: AdapterRegistry, SessionRegistry, DeliveryRegistry
T3.212 | dropped | # T3.212. Убрать поля untrusted из buildPromptText
T3.213 | dropped | # T3.213. SpoolSource для локальных плагинов
T3.214 | dropped | # T3.214. McpPullSource и два MCP-инструмента
T3.215 | dropped | # T3.215. Адаптер OpenCode HTTP
T3.216 | dropped | # T3.216. Адаптер OpenCode плагин и живучесть локации
T3.217 | done | # T3.217. Адаптер Pi RPC
T3.218 | dropped | # T3.218. Адаптер Kilo локальный и облачный
T3.219 | dropped | # T3.219. Адаптер Cursor cloud
T3.22 | done | # T3.22. Цепочка фолбэка агентов при 429 для CLI-менеджера
T3.220 | dropped | # T3.220. Квитанции и лестница ADR 0017
T3.221 | done | # T3.221. eval-набор evals/wake для W01-W13
T3.222 | done | # T3.222. Канонический ключ _meta для сессии
T3.223 | done | # T3.223. Смягчение выгрузки локации OpenCode
T3.224 | done | # T3.224. Дедупликация доставки в Cursor Cloud Agents
T3.225 | done | # T3.225. Сквозной прогон watch, CI, указатель, wake, прочтение
T3.226 | dropped | # T3.226. Свести модели делегирования T3 Code и Crush в одну таблицу
T3.227 | done | # T3.227. eval-покрытие непроверенных живых сценариев W05, W06, W11, W12
T3.228 | done | # T3.228. eval-покрытие частично проверенных сценариев W01, W07, W09
T3.229 | dropped | # T3.229. Самообслуживание оркестратора: реальный Watch и читаный verdict
T3.23 | done | # T3.23. RejectDuplicate и idempotency_key для идемпотентного старта
T3.230 | done | # T3.230. Пробуждение сессии оркестратора по настоящему watchID
T3.231 | done | # T3.231. Политика Watch и авто-мерж по verdict=ready
T3.232 | done | # T3.232. waitFor, waitForMain и идемпотентность мержа
T3.233 | done | # T3.233. Конфликт ребейза, классификация красного и реестр адаптеров
T3.234 | done | # T3.234. Отдельный MCP entry для blocking wait
T3.235 | done | # T3.235. Scope-токены и privilege ceiling для adapter
T3.236 | done | # T3.236. Scoped subscription и ограничение размера ответа
T3.237 | done | # T3.237. Reconcile обязательствов после рестарта процесса
T3.238 | done | # T3.238. Единый контракт wake-бэкендов: webhook, schedule, channel, child, relay
T3.239 | done | # T3.239. Anti-polling инструкция и статус без ожидания
T3.24 | done | # T3.24. human gate с возвратом на конкретный шаг
T3.240 | done | # T3.240. Зафиксировать инвентарь фич конкурентов как чек-лист Beacon
T3.241 | done | # T3.241. Адрес исполнителя
T3.242 | done | # T3.242. Per-target delivery lease
T3.243 | done | # T3.243. Типы защиты ввода
T3.244 | done | # T3.244. Draft-aware доставка
T3.245 | done | # T3.245. Двухшаговая вставка
T3.246 | done | # T3.246. Проверка рендера в pane
T3.247 | done | # T3.247. Проверка input box
T3.248 | done | # T3.248. Broadcast по адресам
T3.249 | done | # T3.249. Transcript rotation
T3.25 | done | # T3.25. Структурированная передача между агентами
T3.250 | done | # T3.250. Поиск по истории
T3.251 | dropped | # T3.251. Ask evidence pack
T3.252 | dropped | # T3.252. Устойчивый чат рига
T3.253 | dropped | # T3.253. Hook bridge для ввода
T3.254 | dropped | # T3.254. Bearer middleware
T3.255 | dropped | # T3.255. Per-rig delivery drain
T3.256 | dropped | # T3.256. Контракт RuntimeAdapter
T3.257 | dropped | # T3.257. Адаптер Claude Code
T3.258 | dropped | # T3.258. Адаптер Codex
T3.259 | dropped | # T3.259. Pi и OMP runner
T3.26 | done | # T3.26. Подтверждение пробуждения отдельно от доставки
T3.260 | dropped | # T3.260. Terminal Sentinel
T3.261 | dropped | # T3.261. Stub runner
T3.262 | dropped | # T3.262. Реестр адаптеров
T3.263 | dropped | # T3.263. Resume honesty probe
T3.264 | dropped | # T3.264. Resume metadata refresher
T3.265 | dropped | # T3.265. Native identity verdicts
T3.266 | dropped | # T3.266. Discover bind adopt
T3.267 | dropped | # T3.267. Whoami резолвер
T3.268 | dropped | # T3.268. tmux metadata ключи
T3.269 | dropped | # T3.269. Host registry
T3.27 | done | # T3.27. Атомарный claim одной задачи одним владельцем
T3.270 | dropped | # T3.270. Gateway humans и организации
T3.271 | dropped | # T3.271. MCP сервер stdio и HTTP
T3.272 | dropped | # T3.272. Access class инструментов
T3.273 | dropped | # T3.273. Карта инструмент к маршруту
T3.274 | done | # T3.274. Member workflow инструкция
T3.275 | dropped | # T3.275. Grant service
T3.276 | dropped | # T3.276. Hosted members
T3.277 | done | # T3.277. ACP run
T3.278 | done | # T3.278. ACP exec connector
T3.279 | done | # T3.279. ACP serve
T3.28 | done | # T3.28. Дедупликация GraphQL-батча по head SHA
T3.280 | dropped | # T3.280. A2A рецепты
T3.281 | dropped | # T3.281. Slack gateway
T3.282 | dropped | # T3.282. Gateway relay
T3.283 | done | # T3.283. Уведомления ntfy и webhook
T3.284 | done | # T3.284. Core app human answer
T3.285 | done | # T3.285. ACP registry
T3.286 | dropped | # T3.286. RigSpec pod-aware
T3.287 | dropped | # T3.287. AgentSpec
T3.288 | dropped | # T3.288. Startup layering
T3.289 | dropped | # T3.289. Startup proof
T3.29 | done | # T3.29. Конфликт как структурированный JSON-ответ
T3.290 | dropped | # T3.290. Projection planner
T3.291 | dropped | # T3.291. Permission posture
T3.292 | dropped | # T3.292. Non-interruptive mode
T3.293 | dropped | # T3.293. Kernel operational default
T3.294 | dropped | # T3.294. Team permission default
T3.295 | dropped | # T3.295. Settings twins
T3.296 | dropped | # T3.296. Env overrides
T3.297 | dropped | # T3.297. Instance layout initializer
T3.298 | dropped | # T3.298. Workspace primitive
T3.299 | dropped | # T3.299. Workspace catalog
T3.3 | done | # T3.3. Пробуждение сессии через ctx.session.prompt с delivery steer
T3.30 | done | # T3.30. Дешёвая песочница через git clone --reference
T3.300 | dropped | # T3.300. Missions и slices
T3.301 | dropped | # T3.301. Bundle create и integrity
T3.302 | done | # T3.302. Configuration ID и digest
T3.303 | dropped | # T3.303. Маршрутизация содержимого bundle
T3.304 | dropped | # T3.304. GitHub folder link
T3.305 | dropped | # T3.305. Behaviour view bundle
T3.306 | dropped | # T3.306. Install audit
T3.307 | dropped | # T3.307. Context packs library
T3.308 | dropped | # T3.308. Situation profiles
T3.309 | dropped | # T3.309. System World
T3.31 | done | # T3.31. Рубрика результата отдельно от завершения
T3.310 | dropped | # T3.310. Skill loadouts
T3.311 | dropped | # T3.311. Plugin discovery и vendoring
T3.312 | dropped | # T3.312. Agent images
T3.313 | dropped | # T3.313. Управляемая компакция Claude
T3.314 | dropped | # T3.314. Pi нативная компакция
T3.315 | dropped | # T3.315. Review freeze
T3.316 | dropped | # T3.316. Snapshot capture
T3.317 | dropped | # T3.317. Restore orchestrator
T3.318 | dropped | # T3.318. Pre-restore validation
T3.319 | dropped | # T3.319. Restore-check probe
T3.32 | done | # T3.32. Аутентификация одобрения
T3.320 | dropped | # T3.320. Restore packet
T3.321 | dropped | # T3.321. Seat continue
T3.322 | dropped | # T3.322. Seat handover
T3.323 | dropped | # T3.323. Handover recover
T3.324 | dropped | # T3.324. Watchdog jobs
T3.325 | dropped | # T3.325. Workflow runtime
T3.326 | dropped | # T3.326. TUI ViewState
T3.327 | dropped | # T3.327. Control socket TUI
T3.328 | dropped | # T3.328. TUI daemon client
T3.329 | dropped | # T3.329. TUI event stream
T3.33 | done | # T3.33. Метка версии агента рядом с одобрением
T3.330 | dropped | # T3.330. Web UI shell
T3.331 | dropped | # T3.331. Web UI routes
T3.332 | dropped | # T3.332. Drawer и viewer система
T3.333 | dropped | # T3.333. For You feed verbs
T3.334 | dropped | # T3.334. Mission Control views
T3.335 | dropped | # T3.335. Telemetry read surface
T3.336 | dropped | # T3.336. Health summary
T3.337 | dropped | # T3.337. Review и fleet composition
T3.338 | dropped | # T3.338. Event bus и SSE
T3.339 | dropped | # T3.339. Version skew
T3.34 | done | # T3.34. Дедупликация вебхуков по event.id
T3.340 | dropped | # T3.340. UI enabled flag
T3.341 | done | # T3.341. Сценарный harness
T3.342 | done | # T3.342. Seeded regression пары
T3.343 | dropped | # T3.343. Docs guard
T3.344 | done | # T3.344. Internal leak scanner
T3.345 | dropped | # T3.345. Skill edge digests
T3.346 | done | # T3.346. Migration parity
T3.347 | dropped | # T3.347. Package assembly check
T3.348 | done | # T3.348. CLI daemon contract
T3.349 | dropped | # T3.349. Settings parity test
T3.35 | done | # T3.35. Формат инструкций смены SKILL.md
T3.350 | done | # T3.350. Performance comparison
T3.351 | dropped | # T3.351. Stream classifier worker
T3.352 | dropped | # T3.352. Семантика edge
T3.353 | dropped | # T3.353. Rosters
T3.354 | dropped | # T3.354. Community harness presets
T3.355 | dropped | # T3.355. Compose services
T3.356 | dropped | # T3.356. Терминальные провайдеры
T3.357 | dropped | # T3.357. Process limits
T3.358 | dropped | # T3.358. Portability report
T3.359 | dropped | # T3.359. Межхостовая очередь
T3.36 | done | # T3.36. Управление авто-вызовом скилла
T3.360 | done | # T3.360. Гигиена секретов в окружении
T3.361 | dropped | # T3.361. ADR 0117. Конфиг CI-гейта на уровне проекта
T3.362 | dropped | # T3.362. Схема конфига и JSON Schema
T3.363 | dropped | # T3.363. Дискавери конфига по предкам
T3.364 | dropped | # T3.364. Разбор на границе, дефолты и валидация
T3.365 | dropped | # T3.365. Слияние файлов и приоритеты
T3.366 | dropped | # T3.366. Сопоставление конфига с доменными типами
T3.367 | dropped | # T3.367. Project-scoped снимок и хеш политики
T3.368 | dropped | # T3.368. Обязательные и игнорируемые проверки в мосте
T3.369 | dropped | # T3.369. Политика флейков и таймаут ожидания
T3.37 | done | # T3.37.  Progressive disclosure справочников
T3.370 | dropped | # T3.370. Политика мержа из конфига при регистрации Watch
T3.371 | dropped | # T3.371. Реестр адаптеров CI и провайдеров
T3.372 | dropped | # T3.372. Команды beacon config show и validate
T3.373 | dropped | # T3.373. Evals W19, W20 и W21 для конфиг-гейта
T3.374 | dropped | # T3.374. Документация и эталонный конфиг проекта
T3.375 | done | # T3.375. Журнал webhook-доставок с ключом event_id
T3.376 | done | # T3.376. Loopback receiver с HMAC-SHA256 и лимитом 25 MB
T3.377 | done | # T3.377. Маппинг события на Watch по repo, PR и headSHA
T3.378 | done | # T3.378. workflow_run как дополнительный триггер
T3.379 | done | # T3.379. Домен и store: blocked-состояние Watch
T3.38 | done | # T3.38. Сабагент с узкими правами для исследования
T3.380 | done | # T3.380. CI-мост: пропуск blocked Watch
T3.381 | done | # T3.381. Оркестратор: запуск FIX-агента по blocked wake
T3.382 | done | # T3.382. Evals B01-B04 для blocked watch
T3.383 | done | # T3.383. CLI beacon workload: сводка бэклога оркестратора
T3.384 | done | # T3.384. Авто-подписка оркестратора на активные Watch
T3.385 | done | # T3.385. Evals L01-L04 для сводки workload
T3.386 | done | # T3.386. Интеграция workload и авто-подписки в daemon
T3.387 | done | # T3.387. Доменная сущность Orchestrator
T3.388 | done | # T3.388. Workload оркестратора и проектная изоляция
T3.389 | done | # T3.389. ADR 0157: оркестратор как доменная сущность
T3.39 | done | # T3.39. Детерминированная проверка перед комментарием
T3.390 | done | # T3.390. Единое решение Decide для blocked и waitFor
T3.391 | done | # T3.391. Цикл Run на едином решении Decide
T3.392 | done | # T3.392. Проекция blocked-поля в Watch оркестратора
T3.393 | done | # T3.393. OTLP-адаптер поверх Sink и SetTracer
T3.394 | done | # T3.394. Спаны внутри daemon, cibridge.Refresh и wake-доставки
T3.395 | done | # T3.395. Перестать называть любой retry rebase в наблюдаемости
T3.396 | done | # T3.396. Убрать захардкоженный текст нужен rebase из daemonWaker
T3.397 | done | # T3.397. Передавать verdict в VerdictRef wake-указателя
T3.398 | done | # T3.398. Передавать obligationID в wake демона
T3.399 | done | # T3.399. Добавить headSHA и baseRef в wake демона
T3.4 | done | # T3.4. Экранирование недоверенных данных в тексте prompt
T3.40 | done | # T3.40. CI подхватывает все JS-тесты скиллов
T3.400 | done | # T3.400. Добавить mainHeadSHA в wake
T3.401 | done | # T3.401. Добавить CANCELLED в failureConclusions
T3.402 | done | # T3.402. Добавить CANCELLED в isFailedCheck
T3.403 | done | # T3.403. Добавить machine-readable класс блокировки в ack
T3.404 | done | # T3.404. Добавить в executor-task раздел про красный вердикт
T3.405 | done | # T3.405. Добавить в AGENTS.md пример blocked:not_mine
T3.406 | done | # T3.406. Переписать red-verdict-triage под MCP-инструмент
T3.407 | done | # T3.407. Добавить в skill пример с revision и blockReason
T3.408 | done | # T3.408. Объяснить, что plain ack не меняет вердикт
T3.409 | done | # T3.409. Запретить waiting после retry без классификации
T3.41 | done | # T3.41. Hot-reconnect MCP клиента после рестарта
T3.410 | done | # T3.410. Объяснить, что blocked не означает готовность PR
T3.411 | dropped | # T3.411. Подключить T211DeliveryRegistry к T210Deliver
T3.412 | done | # T3.412. Проверять LadderStopped перед wakeOne
T3.413 | done | # T3.413. Проверять claimedAt в daemonWaker.Wake
T3.414 | done | # T3.414. Исключить две параллельные доставки
T3.415 | done | # T3.415. Durable ключ доставки вместо in-memory карт
T3.416 | done | # T3.416. Не будить при Attempt больше единицы без новой ревизии
T3.417 | done | # T3.417. Различать wake_delivered по инициатору
T3.418 | done | # T3.418. Закрывать старый Watch при смене headSHA
T3.419 | done | # T3.419. Добавлять orchestrator-подписчика при регистрации
T3.42 | done | # T3.42. Байт-стабильный ответ wait_result
T3.420 | dropped | # T3.420. Сделать NotifyOrchestrator доступным или удалить из skill
T3.421 | done | # T3.421. Проверить остановку демона без orchestrator-подписчика
T3.422 | done | # T3.422. Назначать FIX-агента по wake от handleBlocked
T3.423 | done | # T3.423. Перенести blockWatch в domain EventBlock
T3.424 | dropped | # T3.424. Добавить VerdictBlocked в validVerdict
T3.425 | done | # T3.425. Записывать BlockInfo через ack
T3.426 | done | # T3.426. Проверить пропуск опроса после T3.425
T3.427 | done | # T3.427. Перевести eval B01-B04 с модели на production
T3.428 | done | # T3.428. Согласовать blocked:resolved и unblock
T3.429 | dropped | # T3.429. Сохранять stderr gh в ошибке провайдера
T3.43 | done | # T3.43. Append-only журнал задач
T3.430 | dropped | # T3.430. Добавить таймаут на PRState
T3.431 | done | # T3.431. Синхронизировать MCP-тик и демон
T3.432 | done | # T3.432. Увеличить интервал опроса или перейти на webhook
T3.433 | dropped | # T3.433. Разделить метрики по типу gh-вызова
T3.434 | dropped | # T3.434. Обработать альтернативный текст отсутствия проверок
T3.435 | dropped | # T3.435. Добавить backoff после transient failure
T3.436 | dropped | # T3.436. Не опрашивать notifying Watch с тем же headSHA
T3.437 | done | # T3.437. Добавить decisionHint в wake_requested
T3.438 | done | # T3.438. Считать повторные wake на один ключ
T3.439 | done | # T3.439. Логировать имена failedChecks в verdict_published
T3.44 | done | # T3.44. Живость сессии по артефактам и PID
T3.440 | done | # T3.440. Добавить blocked в beacon_stats
T3.441 | done | # T3.441. Разделить метрики wake по инициатору
T3.442 | done | # T3.442. Eval: retry wake содержит verdict и идентификаторы
T3.443 | done | # T3.443. Eval: plain ack не останавливает демон, blocked останавливает
T3.444 | done | # T3.444. Eval: один ключ доставляется один раз после рестарта
T3.445 | done | # T3.445. Eval: CANCELLED даёт retry и непустые failedChecks
T3.446 | done | # T3.446. Eval: шаблон исполнителя содержит blocked:not_mine
T3.447 | done | # T3.447. Eval: skill не зовёт недоступный инструмент
T3.448 | done | # T3.448. Шаблоны: три состояния executor и тонкий оркестратор
T3.449 | done | # T3.449. Индекс ADR: добавить новую модель executor
T3.45 | done | # T3.45. ack возвращает ревизию задачи
T3.450 | done | # T3.450. Один descent за карточку
T3.451 | done | # T3.451. Аудируемое удаление закрытых обязательств
T3.453 | done | # T3.453. Коннекторный shutdown drain
T3.455 | done | # T3.455. Typed attention codes
T3.457 | done | # T3.457. Перештамповка владельца при возобновлении blocked
T3.46 | done | # T3.46. Конфликтное планирование до запуска агентов
T3.47 | done | # T3.47. Подбор агента по возможностям
T3.48 | done | # T3.48. Возврат wait_result по событию не по таймауту
T3.49 | done | # T3.49. Warning о совместном gate
T3.5 | done | # T3.5. Fail-open плагина: ошибка адаптера не ломает сессию
T3.50 | done | # T3.50. Failure contract для hook адаптера
T3.51 | done | # T3.51. Fencing token при аренде задачи
T3.52 | done | # T3.52. Пороги лимитов плана 75 и 90 процентов
T3.53 | done | # T3.53. Инвалидация lease после краша
T3.54 | done | # T3.54. Backpressure по квоте GitHub API
T3.55 | done | # T3.55. Ранжирование воркеров по истории
T3.56 | done | # T3.56. Гейт независимого ревью
T3.57 | done | # T3.57. Кворум из завершённых ревью
T3.58 | done | # T3.58. Доставка в идущий turn с fallback
T3.59 | done | # T3.59. Не оставлять stub при ошибке схемы
T3.6 | done | # T3.6. Квитанции в receipts/ через open wx, одна на событие и попытку
T3.60 | done | # T3.60. Парковка провала без слепого ретрая
T3.61 | done | # T3.61. CI healing с почасовым потолком
T3.62 | done | # T3.62. Кросс-платформенный CI-адаптер
T3.63 | done | # T3.63. Реестр портов без коллизий
T3.64 | done | # T3.64. Честный indeterminate результата
T3.65 | done | # T3.65. Durable inbox с at-least-once
T3.66 | done | # T3.66. Отмена вызова вместе с деревом задач
T3.67 | done | # T3.67. Формула вердикта вкомпилирована
T3.68 | done | # T3.68. Вердикт CI в events ref
T3.69 | done | # T3.69. Тёплая песочница после выхода агента
T3.7 | done | # T3.7. Шаблон задания исполнителя: сдал PR, завершил ход, ждёт вердикта
T3.70 | done | # T3.70. Verified-only результат
T3.71 | done | # T3.71. Advisory file reservations
T3.72 | done | # T3.72. Подключить ShipAllowed к вердикту
T3.73 | done | # T3.73. Подключить SharedGate к ожиданию
T3.74 | done | # T3.74. Удалить неиспользуемые compatibility-алиасы
T3.75 | done | # T3.75. Проводка фактов Ship в адаптерах
T3.76 | done | # T3.76. Подключить SharedCIWait к long-poll
T3.77 | done | # T3.77. Eval на сквозной вердикт ready
T3.78 | done | # T3.78. Подключить ShipFacts к реальному Evaluate
T3.79 | done | # T3.79. WatchOutcome для 15 процессов
T3.8 | done | # T3.8. Шаблон задания оркестратора: не мержить без get_result verdict ready
T3.80 | done | # T3.80. wake_between против thundering herd
T3.81 | done | # T3.81. Precheck init_event_timeout
T3.82 | done | # T3.82. Причина precheck в wait_result и журнале
T3.83 | done | # T3.83. Внедрить SharedCIWait в прод-путь
T3.84 | done | # T3.84. Подключить precheck причину к WaitResultService
T3.85 | done | # T3.85. Дескриптор задачи с дедлайном во вложенных вызовах
T3.86 | done | # T3.86. Трёхсигнальная детекция смерти
T3.87 | done | # T3.87. Адресация уведомления к worktree
T3.88 | done | # T3.88. Completion gate с proof
T3.89 | done | # T3.89. Замороженный crash-test
T3.9 | done | # T3.9. Лестница пробуждения в базе: ветвление по исходу доставки
T3.90 | done | # T3.90. Идемпотентный replay сценария
T3.91 | done | # T3.91. Проводка всех WaitResult сеттеров в прод
T3.92 | done | # T3.92. Чекпоинт шагов ожидания с resume
T3.93 | done | # T3.93. Восемь состояний доставки
T3.94 | done | # T3.94. STALE-колонка в beacon agents
T3.95 | done | # T3.95. Возобновление задачи по session_id в attach
T3.96 | done | # T3.96. Подписанный непрозрачный handle задачи
T3.97 | done | # T3.97. Аренда worktree с TTL и fencing
T3.98 | done | # T3.98. Внешний вердикт с доказательствами
T3.99 | done | # T3.99. Bounded handoff journal с recovery
T4.1 | done | # T4.1. Хаос тест: двойная доставка даёт один эффект, indeterminate не ретраится
T4.2 | done | # T4.2. Перф тест 15/30/60 Watch: VmHWM, p95, поинты в час
T4.3 | done | # T4.3. G1 shadow: Beacon наблюдает, никто не будится
T4.4 | done | # T4.4. G2 canary: владелец применяет конфиг сам
T4.5 | done | # T4.5. G3: пятьдесят процентов агентов на вердиктном цикле
T4.6 | done | # T4.6. G4: сто процентов агентов и ноль gh watch в ps
T4.7 | done | # T4.7. Учебный откат и возврат
T5.1 | done | # T5.1. ADR: разделение авторского конфига и состояния реестра
T5.10 | done | # T5.10. ADR: сверка после рестарта
T5.11 | done | # T5.11. Схема beacon.agent-registry/v1
T5.12 | done | # T5.12. Валидатор конфига реестра
T5.13 | done | # T5.13. Канонический digest конфигурации
T5.14 | done | # T5.14. Поиск конфига по project key
T5.15 | done | # T5.15. Генератор configuration ID
T5.16 | done | # T5.16. Presets и recommended alias
T5.17 | done | # T5.17. Валидация блока limits
T5.18 | done | # T5.18. Защита конфига от секретов и размера
T5.19 | done | # T5.19. Миграция версии схемы конфига
T5.2 | done | # T5.2. ADR: configuration ID и псевдонимы
T5.20 | done | # T5.20. Словарь ролей, runtime и capabilities
T5.21 | done | # T5.21. Миграция 0005 таблиц registry
T5.22 | done | # T5.22. Идемпотентный upsert registry
T5.23 | done | # T5.23. Построение capability index
T5.24 | done | # T5.24. Двухсторонняя проверка индекса
T5.25 | done | # T5.25. Связка Watch и registry
T5.26 | done | # T5.26. Backfill существующих Watch
T5.27 | done | # T5.27. Атомарная запись конфига
T5.28 | done | # T5.28. Изоляция запросов по проекту
T5.29 | done | # T5.29. Registry в SnapshotReader
T5.3 | done | # T5.3. ADR: регистрация исполнителя вместе с Watch
T5.30 | done | # T5.30. Обнаружение stale записей
T5.31 | done | # T5.31. Дескриптор executor в watch_pr
T5.32 | done | # T5.32. Расширение notify metadata
T5.33 | done | # T5.33. Привязка sessionID из _meta
T5.34 | dropped | # T5.34. Регистрация до фразы waiting
T5.35 | dropped | # T5.35. Повторная регистрация без эффекта
T5.36 | done | # T5.36. config_id в Watch и ответе
T5.37 | dropped | # T5.37. Перепривязка при смене head SHA
T5.38 | dropped | # T5.38. Непрозрачные имена адаптеров
T5.39 | dropped | # T5.39. Честный unsupported адаптер
T5.4 | done | # T5.4. ADR: производный capability index
T5.40 | dropped | # T5.40. Контракт семи инструментов
T5.41 | dropped | # T5.41. Подбор по capabilities через port
T5.42 | dropped | # T5.42. Явный target как fallback
T5.43 | dropped | # T5.43. Разрешение коллизий capabilities
T5.44 | dropped | # T5.44. Рассылка по capability и wake_between
T5.45 | done | # T5.45. Квитанция wake привязана к entry
T5.46 | dropped | # T5.46. delivered, claimed и read раздельно
T5.47 | dropped | # T5.47. Готовность из вердикта
T5.48 | dropped | # T5.48. Worktree как адрес получателя
T5.49 | done | # T5.49. Heartbeat и stale в wake
T5.5 | done | # T5.5. ADR: состояния registry versus вердикты
T5.50 | dropped | # T5.50. Fencing token на мутациях
T5.51 | done | # T5.51. Проверенный stop registry
T5.52 | done | # T5.52. Неопределённый исход stop
T5.53 | done | # T5.53. Команда registry list
T5.54 | done | # T5.54. Команда registry check
T5.55 | done | # T5.55. Команда registry sync
T5.56 | done | # T5.56. Команда registry stop
T5.57 | dropped | # T5.57. spawn регистрирует исполнителя
T5.58 | done | # T5.58. Колонки JOIN в watch --live
T5.59 | done | # T5.59. Источники агентов из сервера
T5.6 | done | # T5.6. ADR: проверенный останов исполнителя
T5.60 | done | # T5.60. Счётчики registry в beacon_stats
T5.61 | done | # T5.61. E2E регистрация на живом сервисе
T5.62 | done | # T5.62. Документация команды и ADR
T5.63 | done | # T5.63. Gate схемы реестра
T5.64 | done | # T5.64. Предупреждения по порогам лимитов
T5.65 | done | # T5.65. Роль подписчика fixer в watch_pr
T5.66 | done | # T5.66. Пропустить fixer через остальные wake-проверки
T5.67 | done | # T5.67. Отдельный счётчик фиксов по роли fixer
T5.7 | done | # T5.7. ADR: фоновый сервис и узкие порты
T5.8 | done | # T5.8. ADR: лимиты реестра
T5.9 | done | # T5.9. ADR: семь инструментов ядра
TG-01 | done | Уже влито: PR #1646, merge 7debcb0d5699097c58187ce79bfb3224cf76f3b0 (git merge-base --is-ancestor <SHA> origin/main exit 0). Тесты block-path: internal/infra/cibridge/blocked_informational_test.go:44,103,159; фильтр: verdict_applier.go:740. Владелец закрыл дубль PR #1688 как повтор #1646: https://github.com/refusedguy/beacon/pull/1688#issuecomment-7079485980. Дубль кода не создан.
TG-02 | done | Already landed in PR #1689 (commit 3e2cd2f971deb7e76a337c955aaca6ffa906a100): test(worktree): cover gc collection rules. git merge-base --is-ancestor 3e2cd2f971de origin/main exit 0.
TG-03 | done | already_landed
TG-04 | done | already_landed
TG-05 | done | Already landed in origin/main via PR #1922. Commit f538260a67736ba2a5243ee0edf2cb3a53ee5286 "test(hookrun): cover Run stdin, streams and exit code (TG-05) (#1922)". git merge-base --is-ancestor f538260a origin/main exit 0. internal/infra/hookrun/hookrun_test.go is present in origin/main and covers stdin, empty stdin, stdout/stderr, exit code and RunContext. No duplicate code written.
TG-06 | done | Ветка 6b по указанию оркестратора: единственный OwnerFiles internal/app/taskcli/service_test.go занят открытым PR #1918 (fix/cbx05b, head d380d8d344e89eb09b110f8dc8d76890f449a7ec, state open). Проверка git log origin/main --oneline --grep=TG-06 -i пуста, коммита TG-06 в main нет. Дубль кода и конфликтующий code-PR не открываю, docs-only PR несёт только статус карточки и доказательство. Тесты трёх функций ещё не написаны, повторная выдача нужна после закрытия #1918.
TG-07 | done | already_landed
TUI-01 | done | already landed in PR #1590 63dc3d52
TUI-02 | done | already landed in PR #1446 ce5df17b
TUI-03 | done | already landed in PR #1453 70f11147
TUI-04 | done | already landed in PR #1593 a16a28f3
TUI-05 | done | already landed in PR #1454 01816965
TUI-06 | done | already landed in PR #1111 9becefcca008
TUI-07A | done | already landed in PR #1610 27e43997
TUI-07B | done | already landed in PR #1476 536fb184
TUI-07C | dropped | # TUI-07C. Тест текстового fallback без TTY
TUI-08 | done | already landed in PR #1452 4284487a
TUI-09 | dropped | # TUI-09. Третий HTTP-клиент для TUI
TUI-10 | dropped | # TUI-10. Клавиши spawn и kill на human-decide
UI-01 | dropped | # UI-01. Чтение state SQLite только на чтение
UI-02 | dropped | # UI-02. Проверка версии схемы state
UI-03 | dropped | # UI-03. Локальный control socket для действий
UI-04 | dropped | # UI-04. Подписка на изменения или опрос
UI-05 | dropped | # UI-05. Общая модель данных UI
UI-06 | dropped | # UI-06. Переподключение и смена проекта
UI-07 | dropped | # UI-07. Экран Watch list
UI-08 | dropped | # UI-08. Экран Watch detail
UI-09 | dropped | # UI-09. Экран Stats
UI-10 | dropped | # UI-10. Экран Event log
UI-11 | dropped | # UI-11. Экран сессий и адаптеров
UI-12 | dropped | # UI-12. Экран аренды worktree
UI-13 | dropped | # UI-13. Экран карточек задач
UI-14 | dropped | # UI-14. Экран диагностики
UI-15 | dropped | # UI-15. Действие ack обязательства
UI-16 | dropped | # UI-16. Отмена Watch
UI-17 | dropped | # UI-17. Claim и release обязательства
UI-18 | dropped | # UI-18. Управление арендой worktree
UI-19 | dropped | # UI-19. Повторная доставка wake
UI-20 | dropped | # UI-20. Открытие внешней ссылки
UI-21 | dropped | # UI-21. Нечеткий поиск
UI-22 | dropped | # UI-22. Фильтры
UI-23 | dropped | # UI-23. Сортировка
UI-24 | dropped | # UI-24. Командная палитра и справка
UI-25 | dropped | # UI-25. Тема и цветовой профиль
UI-26 | dropped | # UI-26. Мышь, ресайз и узкий терминал
UI-27 | dropped | # UI-27. Оптимистичное обновление и откат
UI-28 | dropped | # UI-28. Режим offline
UI-29 | dropped | # UI-29. Нулевой этап: MCP-native обзор
UI-30 | dropped | # UI-30. Нулевой этап: CLI с fzf
UI-31 | dropped | # UI-31. Прототипное сравнение Bubble Tea и tview
UI-32 | dropped | # UI-32. SSH-транспорт на wish
UI-33 | dropped | # UI-33. CI-гейт по размеру бинарника
UI-34 | dropped | # UI-34. Web-spike по отдельному решению
VCI-01 | done | already_landed
VCI-05 | done | Already landed in PR #1519 (commit 676bcae601dadac9b7f7a31d95d88928a6b3543f, pin REST merge to the verified head SHA), PR #1531 (commit 5042149eb77c7cfb0327db4050cc1c0b643c7cc0, verify merge confirmation in PRClient.MergePR) and PR #1581 (commit 6086c260c0fe13cc8e70ed70f0b0f1828c2d5324, keep full gh stderr in merge errors). `git merge-base --is-ancestor <SHA> origin/main` exit 0 for all three. Открытого PR с тем же исправлением нет: `gh pr list --state open --search 'VCI-05 OR head_mismatch OR "verified head SHA" in:title'` вернул пустой список.
VCI-06 | done | already_landed
VCI-08 | done | already landed in PR #1883 and PR #1914, merge-base --is-ancestor exit 0.
VCI-09 | done | already_landed
VCI-10 | done | already_landed
WG-01 | done | PR #1521 влит в origin/main, merge commit 3c8e20f649858ba2e804a06e3ca987198cb136fd, git merge-base --is-ancestor exit 0. worktree_scope.go и тесты в main.
WG-02 | done | PR #1529 влит в origin/main, merge commit e359091f486f60369397282652119ac36f3a2d02, git merge-base --is-ancestor exit 0. headguard.go и тесты в main.
WG-05 | done | already landed in PR #1545 53f0aba1
WIRING-1106 | done | # WIRING-1106. Bind worktree GC port in bridge composition root
WIRING-1582 | done | Уже влито в main. PR #1906, коммит f48b44b30344f6019cf9847c2e73854acb1c866f (feat(stats): expose shared CI gate snapshot (FTE-03)), добавил в internal/infra/mcp/tool_stats.go порт SharedGateSnapshotReader, метод SetSharedGateSnapshotReader, публикацию CIWaitSnapshot и тест. Проверка git merge-base --is-ancestor f48b44b3 origin/main вернула 0. Привязку адаптера в композитном корне ведёт отдельная карточка WIRING-1906, её OwnerFiles включают server.go и cmd/beacon/mcp.go. Повторного кода и PR с кодом не открываю.
WIRING-1608 | done | Уже влито в origin/main, дубль кода не создаём. Источник PR #1608 мерджен (e31893b7153cea5e39b344823d858596ea3ba2ad). Серверная часть проводки влита PR #1660 (merge SHA 2d34645b54f45d84dba3a595350f17f8658759da, internal/infra/mcp/server.go): attachTaskClaim принимает Dependencies и связывает GraphIndexReader. Часть composition root и тесты влиты PR #1753 (merge SHA 355ba72a236c67d9ecce0504c6ea2f1c296bb3fa, ровно OwnerFiles cmd/beacon/mcp.go и cmd/beacon/mcp_test.go): buildMCPDependencies связывает GraphIndexReader, RegistryStatsReader и GenerationLedger, mcp_test.go проверяет все три порта. git merge-base --is-ancestor для обоих SHA дал exit 0. Открытого PR с этой же проводкой нет.
WIRING-1680 | done | already_landed
WIRING-1686 | done | already_landed
WIRING-1822 | done | already_landed
WIRING-1889 | done | already_landed
WIRING-485 | done | # WIRING-485. Подключить notify router к доставке событий
WIRING-486 | done | # WIRING-486. Подключить channel ingress к серверу и обработчику команд
WIRING-487 | done | # WIRING-487. Подключить card monitor к расписанию и хранилищу карточек
WIRING-488 | done | # WIRING-488. Подключить реестр сессий к приложению
WIRING-489 | done | # WIRING-489. Подключить напоминания о дедлайнах
WIRING-490 | done | # WIRING-490. Подключить lineage дочерних thread к серверу
WIRING-887 | done | # WIRING-887. Подключить beacon gc к dispatch CLI
WIRING-915 | done | # WIRING-915. Подключить интерактивный beacon board к dispatch
WIRING-919 | done | # WIRING-919. Подключить beacon gc и beacon init к dispatch CLI
WIRING-926 | done | # WIRING-926. Привязать CardLinks port в composition root
WIRING-931 | done | # WIRING-931. Подключить beacon index к dispatch CLI
