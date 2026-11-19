# ADR 0030. Остальные фичи из отчёта N для ночной смены

Статус: accepted

## Решение

ADR 0028 разложил три правила из отчёта N: идемпотентность по `clientRequestId`, квитанцию как записанное намерение и прерывание ожидания, когда агенту нужен человек. Этот ADR разкладывает оставшиеся восемь фич из раздела «Фичи которые стоит взять». Все они работают через один мост на протокол из ADR 0024 и не привязаны к продукту.

1. Имена и статусы задач берутся из схемы `io.modelcontextprotocol/tasks`, без локальных вариантов.
2. Отправка сообщения агенту имеет четыре режима `auto`, `queue`, `steer` и `restart` вместо одного `steer`.
3. Терминальное состояние задачи защищено от поздних событий: результат пишется до уведомления, а позднее событие его не перезаписывает.
4. Очередь, события и данные задачи лежат в одном SQLite-файле и пишутся одной транзакцией.
5. Worktree используют общий кэш зависимостей через символические ссылки на игнорируемые каталоги.
6. Комментарии из diff приходят агенту как одна из форм `steer`.
7. Наблюдение за чужими JSONL-файлами работает без демона и отдаёт JSON для скриптов, но не является источником вердикта.
8. Внешний MCP-клиент начинает с read only и получает право на запись только после решения человека.

## Причина

1. Сверка со [схемой](https://github.com/modelcontextprotocol/ext-tasks/blob/main/schema/2026-07-28/schema.ts) стоит одной карточки и даёт совместимость с любым MCP-клиентом ([N, пункт 1](https://github.com/modelcontextprotocol/ext-tasks/blob/main/schema/2026-07-28/schema.ts)).
2. У Beacon сейчас один режим `steer`, и это место для роста ([N, пункт 4](https://github.com/pingdotgg/t3code/blob/main/docs/orchestration-v2/orchestrator-mcp-server.md#L429-L443)). Поле `delivery` из ADR 0026 уже различает `steer`, `queue` и `unsupported`, поэтому четыре режима расширяют существующий контракт, а не заменяют его.
3. Запись результата до уведомления описана в [opencode-background-agents](https://github.com/kdcokenny/opencode-background-agents/blob/main/README.md#L46-L55) ([N, пункт 6](https://github.com/kdcokenny/opencode-background-agents/blob/main/README.md#L46-L55)). ADR 0019 закрывает Watch по терминальному вердикту, поэтому вердикт не должен меняться после закрытия.
4. Очередь и события в том же SQLite одной транзакцией с данными описаны в [honker](https://github.com/russellromney/honker/blob/main/README.md#L17-L18) и [backlite](https://github.com/mikestefanello/backlite/blob/main/README.md#L116) ([N, пункт 7](https://github.com/russellromney/honker/blob/main/README.md#L17-L18)). Это продолжает выбор SQLite из ADR 0015 и убирает рассогласование между хранилищами.
5. Общий кэш зависимостей для worktree через символические ссылки на игнорируемые каталоги описан в [parallel-code](https://github.com/johannesjo/parallel-code/blob/main/README.md#L40) ([N, пункт 8](https://github.com/johannesjo/parallel-code/blob/main/README.md#L40)). ADR 0021 дал каждому агенту отдельный worktree, и общий кэш снимает повторную установку зависимостей без слияния рабочих каталогов.
6. Комментарии из diff обратно агенту как форма steer описаны в [parallel-code](https://github.com/johannesjo/parallel-code/blob/main/README.md#L42) ([N, пункт 9](https://github.com/johannesjo/parallel-code/blob/main/README.md#L42)). Это тот же канал обратной связи, который ADR 0026 назвал `steer`, только источником сообщения становится review.
7. Наблюдение без демона по чужим JSONL и JSON-вывод для скриптов описаны в [agtop](https://github.com/ldegio/agtop/blob/main/README.md#L33) ([N, пункт 10](https://github.com/ldegio/agtop/blob/main/README.md#L33)). ADR 0027 запрещает читать логи как источник результата, поэтому наблюдение остаётся средством мониторинга, а вердикт по-прежнему приходит из ветки, PR или файла в worktree.
8. Внешний MCP-клиент начинает с read only и расширяет права после решения человека ([N, пункт 11](https://github.com/asheshgoplani/agent-deck/blob/main/README.md#L62-L72)). Это продолжает требование ADR 0020, что задача приходит из согласованного решения, и grant записи становится таким же решением.

ADR 0020 требует, чтобы задача приходила из согласованного решения. Статус `accepted` включает как всю серию 0014-0028, так и этот ADR, поэтому карточки по восьми пунктам появятся отдельными задачами, а не этим документом.

## Последствия

Из каждого пункта выходит одна небольшая карточка в `docs/tasks/` по правилам ADR 0020. Карточки не создаются этим ADR. Владелец файлов ниже это предложение для будущей карточки, а не изменение в этом PR.

- Пункт 1, стандартные имена. Владелец файлов: `internal/infra/mcp/schema.go`, `internal/infra/mcp/meta.go`, `internal/infra/mcp/server.go`, `internal/domain/types.go` и тесты к ним. Проверка: сверка со схемой проходит, локальных имен и статусов в ответе нет.
- Пункт 2, четыре режима. `auto` выбирает способ доставки сам, `queue` ставит в очередь, `steer` доставляет в работающий ход, `restart` перезапускает агента. Владелец файлов: `internal/infra/mcp/tool_wake.go`, `internal/infra/mcp/schema.go`, `internal/domain/types.go`, `internal/infra/ladder/escalation.go` и тесты к ним. Проверка: все четыре режима различаются в ответе, `restart` не принимается за `steer`, лестница из ADR 0017 не ломается.
- Пункт 3, терминальное состояние. Владелец файлов: `internal/infra/store/receipts.go`, `internal/infra/store/db.go`, `internal/domain/transition.go`, `internal/infra/mcp/tool_get_result.go`, `internal/infra/mcp/tool_wait_result.go` и тесты к ним. Проверка: событие, пришедшее после терминального вердикта, не меняет сохранённый результат и не открывает Watch повторно.
- Пункт 4, одна транзакция. Владелец файлов: `internal/infra/store/db.go`, `internal/infra/store/atomic_write.go`, `internal/infra/taskstore/store.go`, `internal/infra/store/receipts.go` и тесты к ним. Проверка: прерванная транзакция не оставляет задачу в очереди без события и событие без задачи.
- Пункт 5, общий кэш. Владелец файлов: `tools/worktree/link-deps.sh`, `worktrees/README.md` и тест в `tests/`. Проверка: два worktree видят один кэш, каталоги кэша перечислены в `.gitignore` и не попадают в diff.
- Пункт 6, комментарии из diff. Владелец файлов: `.opencode/plugins/beacon-bridge.js`, `internal/infra/mcp/tool_wake.go`, `tests/adapter/steer-prompt.test.js` и `internal/infra/mcp/tool_wake_test.go`. Проверка: комментарий из diff доходит до агента с доставкой `steer` и попадает в квитанцию по ADR 0014.
- Пункт 7, наблюдение. Владелец файлов: `internal/infra/mcp/tool_observe.go`, `internal/infra/store/observability.go`, `internal/infra/mcp/schema.go` и тесты к ним. Проверка: инструмент читает чужой JSONL без демона и отдаёт JSON, а вердикт по-прежнему приходит по ADR 0027.
- Пункт 8, read only. Владелец файлов: `internal/infra/mcp/server.go`, `internal/infra/mcp/meta.go`, `internal/infra/mcp/schema.go`, `internal/app/taskcli/service.go` и тесты к ним. Проверка: до решения человека внешний клиент не может вызвать инструмент записи, после решения диапазон инструментов расширяется.

## Отклонённое

Локальные имена и статусы задач вместо схемы `io.modelcontextprotocol/tasks`. Совместимость с MCP-клиентом теряется, а сверка со схемой делается заново при каждом изменении.

Один режим `steer` на все случаи. Он не различает постановку в очередь и перезапуск, поэтому не даёт лестнице из ADR 0017 нужных входных данных.

Разрешение позднему событию перезаписывать терминальный результат. Вердикт меняется после того, как человек его увидел.

Отдельные SQLite-файлы для очереди и событий. Частичная запись оставляет очередь без события или событие без очереди.

Встраивание кода opencode-background-agents. Проект закрыт и работает только на OpenCode V1, в код Beacon переносится только идея записи результата до уведомления ([N, «Что не брать», пункт 4](https://github.com/kdcokenny/opencode-background-agents/blob/main/README.md#L3-L5)).

Встраивание кода parallel-code на TypeScript. Идеи общего кэша и steer из diff берутся, код в Go-проект не переносится ([N, «Что не брать», пункт 3](https://github.com/johannesjo/parallel-code/blob/main/README.md#L21)).

Копирование кода и лицензии agtop. Берётся только идея наблюдения по JSONL, код в Beacon не копируется ([N, «Что не брать», пункт 5](https://github.com/ldegio/agtop/blob/main/README.md#L10)).

Демон для наблюдения и чтение JSONL как источника вердикта. Демон добавляет внешний процесс к одному бинарнику, а логи не являются публичным контрактом по ADR 0027.

Полный доступ внешнему MCP-клиенту с первого вызова. Это нарушает ADR 0020 и даёт внешнему клиенту право создавать задачи до решения человека.
