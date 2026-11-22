# Аудит 02. Жёсткая диета по ОЗУ

Дата: 2026-11-22. Режим только чтение. Код, тесты и конфиги не менялись. `go test`, `go build`, `beacon gates` и `tools.beacon.*` не запускались. Поиск шёл через `rg` с явными каталогами. Все ссылки на код сверены с `origin/main` на коммите `f7049b48`.

Источник фактов: код на `origin/main` и `docs/research/cleanup-plan.md` как описание уборки шлака. Отчёты с замерами процессов есть в ветке `fix/c18-a17`, но отсутствуют в `origin/main`. Числа из них поэтому не используются здесь как измеренные. Оценки эффекта ниже качественные.

## Что измерено до выбора

AGENTS.md требует измерения до выбора подхода. Доступные замеры владельца и прошлых аудитов:

- Владелец сообщил о порядке двух десятков процессов `opencode serve` за два часа и о сиротах после смерти родителя. Прямого замера в этом дереве нет.
- бюджет 100 MB RSS при 15 и более активных Watch. Источник: `internal/infra/server/metrics.go:57-58`, константы `FootprintWatchThreshold` и `FootprintLimitBytes`, ссылка на ADR 0191 и BS.51.
- 30 минут для аренды файлов без подтверждения. Источник: `internal/app/taskcli/service.go:1904`, константа `LeaseTTL`. Это аренда glob, а не срок жизни карточки.
- Явного checkpoint и настроек `wal_autocheckpoint` в коде нет, а готовый `DB.Checkpoint` не вызывается. Источник: `internal/infra/store/db.go:243-248` и отсутствие вызовов вне тестов.

Прямого замера RSS в гигабайтах в репозитории нет. Ниже оценки RAM даны как ожидаемое направление, а не как измеренная дельта. Карточки, которые предпишут подход, обязаны снять замер до реализации и записать поле `baseline`.

## Таблица находок

| Проблема | file:line и символ на `origin/main` | Целевое решение | Оценка эффекта: RAM / токены / надёжность |
| --- | --- | --- | --- |
| Сессии OpenCode создаются и не удаляются. `beacon spawn` вызывает `CreateSession`, затем сохраняет id в реестр. HTTP-клиент имеет `CreateSession` и `Prompt`, но не имеет метода удаления | `cmd/beacon/spawn.go:194`, `spawner.CreateSession`; `cmd/beacon/spawn.go:95`, `recordSpawnSession`; `internal/infra/opencode/client.go:342`, `Client.CreateSession`; `internal/infra/opencode/client.go:370`, `Client.Prompt` | Добавить `Client.DeleteSession` с `DELETE /api/session/:id`; вызывать его из GC по закрытым Watch | RAM: растёт число живых сессий в SQLite OpenCode, удаление останавливает рост. Токены: прямого эффекта нет. Надёжность: меньше мёртвых сессий в wake-пути |
| Общий headless serve уже реализован частично, но удаление сессий не привязано к закрытию Watch | `internal/infra/opencode/server.go:365`, `EnsureServer`; `:118`, `Pdeathsig`; `:237`, `WriteServeState`; `:43`, `serveStateFile` | Не писать второй сервер. Добавить проход GC по закрытым Watch и удалить их сессии через новый порт | RAM: минус один процесс на каждый старт MCP уже достигнут PR 887. Надёжность: полный цикл «создал сессию, закрыл Watch, удалил сессию» |
| `beacon watch` и `beacon agents` читают сессии через `opencode api`, то есть поднимают отдельный CLI-процесс на команду | `cmd/beacon/watch.go:234`, `c.run(..., "opencode", ["api","session.list"])`; `cmd/beacon/agents.go:184`, `app.agents`; `cmd/beacon/agents.go:187`, `session.list` | Перевести оба вызова на уже существующий `newReadOnlyOpencodePort` из `cmd/beacon/opencode.go:151` и методы `Client.Sessions` и `Client.ListAgents` | RAM: минус один CLI-процесс на команду. Токены: меньше повторного чтения registration-файла. Надёжность: один формат ответа вместо CLI-парсинга |
| HTTP-путь wake теряет `delivery` и `resume`. `PromptFunc` не принимает `resume`, замыкание в mcp игнорирует `delivery`, а `Client.Prompt` шлёт только `text` | `internal/infra/wake/deliver.go:36`, тип `PromptFunc`; `:297`, вычисление `delivery, resume`; `:310` и `:312`, выбор режима; `:334`, вызов без `resume`; `internal/infra/opencode/client.go:370-372`, body только с `Text`; `cmd/beacon/mcp.go:1722-1724`, замыкание `prompt` | Карточка AUTOSP-02 из audit-01 уже расширяет `Client.Prompt` полями `delivery` и `resume`. MEM-01 удаляет только сессии, MEM-03 добавляет `resume` в `PromptFunc` и `CombinedOutput`, MEM-04 правит замыкание в `opencodeWakePorts` | RAM: нейтрально. Токены: нейтрально. Надёжность: свободная сессия действительно продолжается, занятая получает `queue` |
| CLI-fallback и T202API используют `.Output()`. stderr теряется, поэтому диагноз ошибки разбора и ответа 404 становится гаданием | `internal/infra/wake/deliver.go:370`, `deliverCLI`; `:906`, `T202API.run`; `:1808`, runner адаптера; `:911` и `:918`, `T202API.Prompt` | Заменить на `.CombinedOutput()` по образцу `internal/infra/worktree/gc.go:25`, `CommandRunner` | RAM: нейтрально. Токены: меньше повторных вызовов при разборе ошибки. Надёжность: текст ошибки доходит до журнала |
| `store.ArchiveRun` готов и ждёт вызова. Закрытые Watch остаются в активной таблице | `internal/infra/store/archive.go:198`, `DB.ArchiveRun`; вызовов вне тестов нет | Вызвать `cfg.db.ArchiveRun(ctx, time.Now())` в `ReconcilePass` до `bridge.Refresh` | RAM: меньше payload в памяти при полных чтениях `ActiveWatches`. Токены: нейтрально. Надёжность: расписание архива переживает рестарт |
| `store.Checkpoint` не вызывается. WAL растёт, длинные чтения удерживают старые кадры | `internal/infra/store/db.go:248`, `DB.Checkpoint`; `internal/infra/store/db.go:243`, комментарий о passive checkpoint; вызовов вне тестов нет | Вызвать `cfg.db.Checkpoint(ctx)` после успешного `ArchiveRun` в том же проходе | RAM: меньше размера WAL и страниц в page cache. Токены: нейтрально. Надёжность: предсказуемый размер файла БД |
| `FootprintCheck` готов, но не подключён к циклу. Превышение бюджета никто не печатает | `internal/infra/server/metrics.go:406`, `FootprintCheck`; `:57-58`, пороги; `cmd/beacon/mcp.go:1546`, `pollerMetrics`; вызовов `FootprintCheck` вне тестов нет | Вызвать в `ReconcilePass` с числом активных Watch и `server.ProcessRSSBytes`; писать предупреждение через уже принятый sink | RAM: прямого снижения нет, но превышение становится видимым. Токены: нейтрально. Надёжность: бюджет ADR 0191 перестаёт быть текстом |
| TTL-reaper для карточек `in_progress` отсутствует. `LeaseTTL` ограничивает только glob-аренды | `internal/app/taskcli/service.go:1904`, `LeaseTTL`; `internal/app/taskcli/service.go:483`, `Take`; счётчика по живым PR/Watch нет | Добавить `ReapStaleCards` в taskcli и вызвать его из цикла; сбрасывать в `todo` только карточки без живого Watch и процесса | RAM: меньше висящих воркеров. Токены: меньше повторных промптов. Надёжность: слот оркестратора освобождается |
| Автоуборка чистых worktree уже подключена и не требует новой карточки | `internal/infra/worktree/gc.go:176`, `Collect`; `cmd/beacon/mcp.go:1883`, `bridgeWorktreeGC`; `cmd/beacon/mcp.go:1940`, `WithWorktreeGC` | Не дублировать. В батче это карточка `MEM-07` со статусом `dropped` | RAM: эффект уже доставлен текущим циклом. Токены: нейтрально. Надёжность: защита по LiveWatches уже есть |

## Что не подлежит дублированию

Эти механизмы уже лежат в коде, поэтому карточки батча их не переписывают:

- `newReadOnlyOpencodePort` в `cmd/beacon/opencode.go:151`. Новый HTTP-порт для watch и agents не создаётся, используется этот.
- `FileTaskClaimStore` в `cmd/beacon/mcp.go:746` и `FileTaskCloseStore` в `cmd/beacon/mcp.go:753`. Выдача карточек остаётся в MCP.
- `card_links` в `internal/infra/store/card_links.go:21`. Reaper не создаёт вторую связь карточки и Watch.
- Поддержка ATX-R01 в `internal/infra/taskstore/store.go:100`. Идентификаторы MEM-01 и далее проходят тот же парсер.
- `internal/infra/wake/acp.go`, `internal/tui/app.go`, `internal/tui/board.go`. Транспорт ACP и TUI не трогаются.
- `FootprintCheck` в `internal/infra/server/metrics.go:406`. Карточка только вызывает его, не меняет реализацию.

## Разбивка на карточки

| Карточка | Направление | OwnerFiles | Зависимости | Волна |
| --- | --- | --- | --- | --- |
| MEM-01 | а | `internal/infra/opencode/client.go`, `internal/infra/opencode/client_test.go` | AUTOSP-02 | P0 |
| MEM-02 | г | `cmd/beacon/watch.go`, `cmd/beacon/agents.go` | AUTOSP-08, TUI-03 | P0 |
| MEM-03 | г | `internal/infra/wake/deliver.go`, `internal/infra/wake/deliver_test.go` | нет | P0 |
| MEM-04 | а, в | `cmd/beacon/mcp.go`, `cmd/beacon/daemon.go` | AUTOSP-04, AUTOSP-11, TUI-03, TUI-04, PROTO-03 | P0 |
| MEM-05 | а | `cmd/beacon/gc.go`, `cmd/beacon/gc_test.go` | нет | P1 |
| MEM-06 | б | `internal/app/taskcli/service.go`, `internal/app/taskcli/service_test.go` | AUTOSP-06 | P1 |
| MEM-07 | б | `internal/infra/worktree/gc.go` | нет | P0, `dropped` |
| MEM-08 | docs | `docs/runbooks/observability.md` | нет | P1 |

## Пересечение с другими батчами

Батч `docs/research/audit-01-batch.json` уже лежит в `origin/main`. Его карточки AUTOSP владеют некоторыми файлами из таблицы выше. Чтобы в одной волне не было двух владельцев файла, MEM-карточки получили зависимости:

- MEM-01 ждёт AUTOSP-02. Та карточка уже расширяет `Client.Prompt` полями `delivery` и `resume`, поэтому MEM-01 добавляет только `DeleteSession`.
- MEM-02 ждёт AUTOSP-08, которая правит `cmd/beacon/agents.go`.
- MEM-04 ждёт AUTOSP-04 для `cmd/beacon/mcp.go` и AUTOSP-11 для решения о запуске по RSS.
- MEM-06 ждёт AUTOSP-06 для `internal/app/taskcli/service.go`.
- MEM-02 ждёт TUI-03 из audit-03, которая правит `cmd/beacon/watch.go`.
- MEM-04 ждёт TUI-04 и PROTO-03 из audit-03 и audit-04, которые закрепляют `cmd/beacon/mcp.go`.

Батчи audit-03 и audit-04 вошли в `origin/main` после первой версии этого аудита. Их ID и файлы проверены по `docs/research/audit-03-batch.json` и `docs/research/audit-04-batch.json`. Зависимости задают последовательные волны. Запускать MEM и AUTOSP с пересекающимися OwnerFiles параллельно нельзя.

OwnerFiles в одной волне не пересекаются. Слои соблюдены: `cmd` меняет только `cmd` и порты `internal/infra`, `internal/app` меняет `internal/app`, инфраструктурные пакеты меняют только свои файлы. Границы layers.json карточки не нарушают.

ADR в батче фиксирует политику памяти: один общий serve, удаление сессий по закрытию Watch, реaper по карточкам и три вызова в цикле. Eval проверяет, что предупреждение о превышении RSS появляется и что сессия закрытого Watch удаляется.
