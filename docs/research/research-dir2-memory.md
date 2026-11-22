# Исследование направления 2: память и процессы

Дата: 2026-11-22. Режим только чтение кода. Go-код не писался. `go test`, `go build`, `beacon gates`, `batch-apply` локально не запускались. Ссылки сверены с `origin/main` на коммите `1f1d0e47`.

Число 20-30 МБ RSS это цель владельца, а не измерение. Локальных замеров RSS нет. Карточки батча требуют замер в CI до правки и после правки при одном и том же числе Watch. Ни один eval ниже не подменяет измерение оценкой.

## Границы: что уже доставлено и не попадает в батч

| Механизм | Подтверждение | Почему не дублируем |
| --- | --- | --- |
| Удаление сессии через HTTP | `internal/infra/opencode/client.go:372`, `Client.DeleteSession`, PR 1462 / `9e5d3b91` (MEM-01) | Метод есть. Новые карточки только вызывают его в новых местах. |
| Удаление сессий закрытых Watch | `cmd/beacon/gc.go:109`, `gcSessionPlan`; `:139`, `gcSessionsForGC`; `:407`, `gcSessionDelete`, PR 1444 / `b1c64761` (MEM-05) | Путь закрытого Watch уже работает. Разрыв только в строках реестра и в сессиях без Watch. |
| TTL-reaper карточек | `internal/app/taskcli/service.go:734`, `CardLeaseTTL`; `:794`, `ReapStaleCards`, PR 1465 / `5597372f` (MEM-06) | Карточки `in_progress` уже возвращаются в `todo`. Реaper сессий реестра другой объект. |
| Delivery и resume в Prompt | `internal/infra/wake/deliver.go:394`, `:937`, `:1839`, `CombinedOutput`; `internal/infra/opencode/client.go:417`, `PromptOptions`; `:425`, `PromptWithOptions` | Схема запроса и stderr уже исправлены. Остался только CLI-вызов в daemon-роуте. |
| Уборка чистых worktree | `cmd/beacon/mcp.go:1956`, `WithWorktreeGC`; `cmd/beacon/gc.go:476`, `RegisterGCWorktree` | Подключена в цикле. Новая карточка не создаётся. |
| Архив, WAL и footprint | `internal/infra/store/archive.go:198`, `ArchiveRun`; `internal/infra/store/db.go:248`, `Checkpoint`; `internal/infra/server/metrics.go:406`, `FootprintCheck`. Вызовов вне тестов нет | Эти вызовы уже описаны в `docs/research/audit-02-memory.md`, MEM-04 и AUD-12. Новый батч их не переписывает. |
| Плановый GC сирот | `cmd/beacon/gc.go:253`, `runGCScheduled`; `:281`, флаг `--interval`; `docs/tasks/BCX-81.md` (todo, P0) | Внешний cron или timer уже заведен в бэклоге. Карточка MM-04 меняет только остановку потомков. |
| Адаптер daemon wake | `cmd/beacon/daemon_wake_adapter.go:46`, вызов `T202API.Prompt`; `docs/tasks/AUD-02.md` (todo, P0) | AUD-02 владеет этим файлом. MM-05 меняет маршрут в `cmd/beacon/daemon.go` и не трогает адаптер. |
| LSP на каждый worktree | `opencode --version` даёт `v2.3.2`. Документация OpenCode v2: конфиг `lsp` сохраняется, но v2 не запускает language servers и не даёт LSP-диагностику. В репозитории нет кода LSP | Premise о расходе памяти на LSP для установленной версии ложна. Карточки нет. |

Источник по LSP: https://opencode.ai/docs/migrate-v1.md и https://github.com/anomalyco/opencode/blob/dev/packages/core/src/file-mutation.ts, получены через Context7 2026-11-22.

Ещё один факт из API OpenCode: `DELETE /api/session/{id}` удаляет сессию и все дочерние сессии (`session.remove`, https://opencode.ai/docs/api). Поле `ParentID` уже лежит в `internal/infra/opencode/client.go:144`, но `gcSessionPlan` в `cmd/beacon/gc.go:109` сравнивает только точные id. MM-02 защищает предков живых подписчиков.

## Таблица разрывов

| Проблема | Код, file:line и символ | Конкретный разрыв | Решение | Эффект по RAM | Приоритет |
| --- | --- | --- | --- | --- | --- |
| Строки реестра сессий остаются открытыми | `internal/infra/store/sessions.go:28`, `SessionStatusClosed`; `:64`, `SaveSession`; `:188`, `Touch`; `internal/app/session/registry.go:28`, `SessionStatusClosed`; :34`, `SessionStore`; `:124`, `ResumeByTask`; `internal/infra/store/snapshots.go:206`, проверка `SessionStatusClosed` | Статус `closed` объявлен в двух слоях и читается классификатором восстановления, но нет ни одного писателя. `SessionStore` содержит только `SaveSession`, `GetSession`, `Touch`. Удаление HTTP-сессии в GC не меняет строку в `beacon.db`. `ResumeByTask` может вернуть мёртвый id | MM-01: добавить `Close` и `StaleSessions` в store, расширить порт и адаптер, добавить `CloseSession` и `ReapStaleSessions` в реестр. `FindSessionByTask` пропускает закрытые строки | Меньше строк в `beacon.db`. Меньше повторных промптов по мёртвому id. Число 20-30 МБ не замер, а цель | P3 |
| Сессии без живого Watch не имеют срока жизни | `cmd/beacon/gc.go:109`, `gcSessionPlan`; `:139`, `gcSessionsForGC`; `:407`, цикл `gcSessionDelete`; `cmd/beacon/spawn.go:111`, `reg.Start` | План GC строится только из `RecentClosedWatches` и `LiveWatches`. Сессия, созданная `spawn` без Watch, не попадает ни в один из списков и живёт в SQLite OpenCode неограниченно | MM-02: добавить в план просроченные строки реестра из MM-01. Защитить id живых подписчиков, их предков по `ParentID` и текущую сессию. Закрывать строку реестра только после успешного `DELETE` | Ограничение числа живых сессий за проход через существующий `gcSessionLimit = 200`, :42. Меньше SQLite-страниц в OpenCode | P3 |
| Переиспользование сервера без проверки готовности | `internal/infra/opencode/server.go:155`, `healthReady`; `:171`, `waitHealthy`; `:280`, `ProcessAlive`; `:365`, `EnsureServer`; `:369-371`, ветка reuse | `healthReady` вызывается только после спавна. Ветка reuse возвращает `Config` по живому PID. Замерзший сервер или переиспользованный PID даёт мертвый URL, а следующий вызов снова ищет сервер | MM-03: перед возвратом из ветки reuse делать ограниченный health probe. При отказе вызывать уже существующий `ClearServeState` и идти в spawn | Меньше churn процессов. Один живой serve вместо цепочки неудачных переиспользований | P3 |
| Уборка убивает лидера, потомки остаются | `internal/infra/opencode/server.go:103`, `startServeProcess`; `:118`, `SysProcAttr{Pdeathsig}`; `:290`, `TerminateServe`; `:294`, `syscall.Kill(pid, SIGTERM)`; `:310`, `ListServeProcesses` | Pdeathsig действует на прямого ребёнка. `TerminateServe` шлёт сигнал только лидеру. Сканер `/proc` видит только процессы с аргументом `serve`. Сторонние stdio-дети остаются после SIGTERM лидеру | MM-04: дать спавненному serve отдельную группу процессов через `Setpgid` и завершать группу в `TerminateServe` с откатом на PID | Меньше висящих потомков после GC. LSP как источник памяти для v2.3.2 исключён, причина указана в разделе границ | P4 |
| daemon платит процессом за каждый verdict | `cmd/beacon/daemon.go:705`, `daemonWakeRouteWithGate(wake.PromptTargetDeliverer(&wake.T202API{}), gate)`; `internal/infra/wake/deliver.go:590`, `PromptTargetDeliverer`; `:933`, `T202API.run` с `exec.CommandContext`; `cmd/beacon/opencode.go:151`, `newReadOnlyOpencodePort`; `internal/infra/opencode/client.go:425`, `PromptWithOptions` | `PromptTargetDeliverer` принимает только `*T202API` и каждый wake поднимает `opencode api`. Готовый HTTP-клиент с `delivery` и `resume` в daemon-роуте не используется | MM-05: построить одного клиента из регистрации сервиса и передать `TargetDeliverer`-замыкание поверх `PromptWithOptions`. CLI оставить как явный fallback при отсутствии регистрации | Минус один Node.js-процесс на verdict wake. Адаптер из AUD-02 не трогается | P3 |

## Карточки и фазы

| Карточка | Фаза | OwnerFiles | Зависимости | Пересечения в фазе |
| --- | --- | --- | --- | --- |
| MM-01 | P3 | `internal/infra/store/sessions.go`, `internal/infra/store/sessions_test.go`, `internal/app/session/registry.go`, `internal/app/session/registry_test.go`, `cmd/beacon/session_store_adapter.go` | нет | Нет todo-карточек с этими файлами ни в одной фазе. |
| MM-02 | P3 | `cmd/beacon/gc.go`, `cmd/beacon/gc_test.go` | MM-01 | `BCX-81` и `MEM-05` владеют `gc.go` в P0 и P1. В P3 владельцев нет. |
| MM-03 | P3 | `internal/infra/opencode/server.go`, `internal/infra/opencode/server_test.go` | нет | `BCX-81` владеет `server.go` в P0. В P3 владельцев нет. |
| MM-04 | P4 | `internal/infra/opencode/server.go` | MM-03 | MM-03 заканчивается в P3, поэтому в P4 один владелец. |
| MM-05 | P3 | `cmd/beacon/daemon.go`, `cmd/beacon/daemon_touch_test.go` | AUD-02, ATX-04 | `ATX-04` владеет теми же файлами в P1, поэтому фазы разделены. `daemon_wake_adapter.go` не входит в OwnerFiles. |

Слои соблюдены по `tools/beacon-gates/gate/config/layers.json`. Store остаётся в `internal/infra`, реестр в `internal/app`, адаптер и команды в `cmd/beacon`. Новых импортов между соседними infra-пакетами нет. `cmd/beacon` не открывает HTTP напрямую и использует готовый клиент `internal/infra/opencode`.

## Измерение до выбора

AGENTS.md требует измерения до выбора. Здесь нет ни одного локального go-замера. В поле `baseline` карточек лежит счётчик по коду, полученный чтением исходников: число писателей статуса `closed`, число кандидатов GC без Watch, число health-проверок на reuse, число сигналов terminate, число CLI-шпильков в daemon-роуте. Эти числа фиксируют разрыв, но не RSS.

Цель владельца 20-30 МБ RSS на один долгоживущий процесс остаётся целью. Замер снимается в CI на реальной нагрузке с известным числом Watch до и после каждой правки. Если замер недоступен, карточка не считается доказанной.

## Итог

Батч добавляет пять разрывов, которых нет в `docs/research/audit-02-memory.md` и в карточках MEM-01..08, AUD-11/12, BCX-79/75. Два разрыва касаются SQLite-реестра сессий, два lifespan процесса serve и один стоимости wake в демоне. LSP не получает карточки: установленный OpenCode v2.3.2 не запускает language servers.
