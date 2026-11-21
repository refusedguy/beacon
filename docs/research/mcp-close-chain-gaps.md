# Разрывы в наборе MCP-инструментов и цепочке закрытия карточек

Дата: 2026-11-21. Режим: чтение. Код, тесты, сборки, гейты и инструменты Beacon не запускались. Источники: `rg` по явным каталогам `internal/infra/mcp/`, `cmd/beacon/`, `tests/e2e/`, `evals/`, `tools/beacon-gates/`, `docs/research/`, `docs/runbooks/`, `docs/tasks/` и чтение файлов. База: `origin/main` `33270854`.

Предыдущий аудит `docs/research/task-close-postmerge-audit.md` писался под `8a766a5d`. С тех пор в main вошли `#1229`, `#1233`, `#1271`, `#1279`. Часть его рекомендаций закрыта, часть осталась в другой форме. Ниже зафиксировано состояние базы `33270854`.

## 1. Таблица находок

| ID | Область | Находка и доказательство | Риск | Предложение | Карточка |
| --- | --- | --- | --- | --- | --- |
| M01 | Тесты | `internal/infra/mcp/tool_task_close.go` занимает 450 строк. Файла `tool_task_close_test.go` нет. `rg` по `*_test.go` в `internal/`, `cmd/`, `tests/` нашёл только имя инструмента в `internal/infra/mcp/contract_test.go:47-50` и строке `tests/e2e/e2e_test.go:58`. Ни один тест не вызывает обработчик, валидацию, flock или повтор закрытия. | Варианты входа, владение карточкой и повторные вызовы меняются без проверки. | Добавить `tool_task_close_test.go`: валидация полей, владелец `in_progress`, повтор с теми же значениями, конфликт при других значениях, битый ledger, CRLF, проверка `duplicateOf`. | ATX-M01 |
| M02 | `tools/list` | Золотой ответ меряется на `NewServer()` (`internal/infra/mcp/contract_test.go:119-152`), где 10 инструментов. Рабочий сервер добавляет одиннадцатый: `cmd/beacon/mcp.go:514` вызывает `mcp.AttachWake`, а `internal/infra/mcp/tool_wake.go:1801-1810` регистрирует `beacon_wake`. E2E ждёт 11 имён (`tests/e2e/e2e_test.go:58`, `:1124`). | Золотой размер 19467 и SHA `5d1d226c…` описывают сервер, которого нет в production. Изменение описания wake не ловится контрактным тестом. | Измерить золотой ответ на composition root из `cmd/beacon/mcp.go` и зафиксировать оба набора имён в контракте, либо увести wake в отдельный ADR с отдельным golden. | ATX-M02 |
| M03 | `tools/list` | Дескриптор `beacon_watch_pr` и тип `WatchPRInput` (`internal/infra/mcp/server.go:135-144`, `internal/infra/mcp/tool_watch_pr.go:192`) не знают про `cardID`. Рабочий обработчик расширяет вход структурой `watchPRInputWithCard` (`cmd/beacon/mcp.go:522-525`, `:565-572`). | inputSchema в production отличается от того, что проверяет golden. Клиент, который читает контракт, не видит поле `cardID`. | Перенести `cardID` в `mcp.WatchPRInput`, обновить golden в том же PR и убрать локальную обёртку в `cmd`. | ATX-M03 |
| M04 | `tools/list` | Advertised-схема `beacon_wait_result` строится из `WaitResultOutput` с пятью полями (`internal/infra/mcp/schema.go:52-59`). Обработчик возвращает `WaitReasonOutput` с полем `reason` (`internal/infra/mcp/tool_wait_result.go:162-175`). Тест разрешает лишние поля через `relaxSchema` (`internal/infra/mcp/contract_test.go:381`). | Строгий клиент отвергнет ответ или не увидит причину ожидания. Дрейф схемы проходит мимо golden. | Выводить advertised-схему из типа, который реально возвращает обработчик. Добавить тест строгой схемы отдельно от `relaxSchema`. | ATX-M04 |
| M05 | Закрытие | Все три пути закрытия пишут локальный файл. `closeLinkedCardFile` использует `wiringCardWriteFile = os.WriteFile` (`cmd/beacon/mcp.go:124`, `:153-180`). `beacon_task_close` меняет статус и ledger на диске (`internal/infra/mcp/tool_task_close.go:280-287`). `taskstore.CloseCard` тоже работает с файлом (`internal/infra/taskstore/close.go:58-83`). Коммита и PR нигде нет. Ledger лежит в state dir вне репозитория (`cmd/beacon/doctor.go:258-289`). | Карточка переходит в `done` только в рабочем дереве. В `origin/main` остаётся `todo`, поэтому CI и оркестратор видят старый статус. Аудит `#4` насчитал 17 таких карточек, текущее дерево показывает 79 изменённых файлов `docs/tasks/`. | Сделать обязательным docs-PR после close, как в ветке 6b скилла `beacon-executor`. Добавить проверку расхождения статусов между деревом и `origin/main`. | ATX-M05 |
| M06 | Поля карточки | `beacon_task_close` хранит `reason` и `duplicateOf` в sidecar-ledger (`internal/infra/mcp/tool_task_close.go:13-14`, `:156-169`, `:273-287`). В саму карточку пишется только `Статус`. Парсер читает `Причина`, `Дубликат`, `Закрыл`, `Время закрытия` из файла (`internal/infra/taskstore/store.go:203-206`). | После MCP-закрытия карточка не показывает причину, дубликат и время. Runbook требует доказательство, а инструмент его не сохраняет. | Записывать канонические поля закрытия через общий модуль записи карточек. Базовые 13 полей оставить без изменения. | ATX-M06 |
| M07 | Идемпотентность | `closeRecord` хранит `status`, `reason`, `duplicateOf`, `agentID`, `closedAt` (`internal/infra/mcp/tool_task_close.go:156-164`), но повтор сравнивает только `Status` и `Reason` (`:237-250`). Повтор с другим `agentID` или другой целью дубликата возвращает `ok` и оставляет ledger прежним. | Два разных решения выглядят как один закрытый вызов. Аудит теряет данные о том, кто и что закрыл. | Включить `agentID` и `duplicateOf` в ключ идемпотентности либо возвращать конфликт при их изменении. Покрыть тестами. | ATX-M07 |
| M08 | Идемпотентность | Одна карточка защищена тремя разными замками: `.beacon-task-claim.lock` (`internal/infra/mcp/tool_task_claim.go:312-317`), `.beacon-task-close.lock` (`internal/infra/mcp/tool_task_close.go:323-329`) и `taskstore.LockPath` (`cmd/beacon/task.go:549`). Claim пишет `Агент` под своим замком (`internal/infra/mcp/tool_task_claim.go:286`), close проверяет это поле под другим. | Параллельные claim и close видят разные версии карточки. Проверка владельца перестаёт быть надёжной. | Ввести один путь замка для claim, close и CLI `task done`. Добавить тест на параллельные вызовы. | ATX-M08 |

## 2. Инструменты без тестов

`internal/infra/mcp/` содержит обработчики с собственными тестами для ack, cancel_watch, claim ( lease ), defer_wiring, get_result, list_running, stats, task_claim, thread_read, wait_result, wake и watch_pr. Исключение один: `tool_task_close.go`. У него нет файла тестов и нет вызовов в чужих тестах. Контрактный тест проверяет только имя, схему вывода и аннотацию инструмента. E2E держит имя в `expectedSet`, но не вызывает его.

Отдельно стоит `internal/infra/mcp/tool_wake.go`: тесты на обработчик есть, но регистрация в production не покрыта контрактным тестом. Это следствие находки M02.

## 3. Расхождения `tools/list` и обработчика

Есть четыре независимых расхождения.

1. Множество инструментов. Golden описывает 10, production отдаёт 11 из-за `beacon_wake` (M02). E2E ожидает 11, контрактный тест ожидает 10. Оба утверждения верны для разных composition root, но в документах написано одно число.
2. Вход `beacon_watch_pr`. Production добавляет `cardID`, контракт его не знает (M03).
3. Выход `beacon_wait_result`. Схема обещает пять полей, обработчик добавляет `reason` (M04).
4. Комментарии. `internal/infra/mcp/contract_test.go:49-50` пишет, что список ожидаемых имён записывается «once the beacon_task_close descriptor is registered», хотя дескриптор зарегистрирован. `internal/infra/mcp/schema.go:280-282` пишет про девять инструментов и запрет десятого, хотя в таблице десять. Эти строки мешают читателю понять, какое число верно сейчас.

`expectedToolNames`, `ToolNames`, `toolAnnotations` и `outputSchemaFactories` выводятся из одной таблицы `toolDescriptors`. Независимыми остаются только ручные байты и SHA. Любое изменение дескриптора автоматически меняет три проверки из четырёх. Это описано в `docs/research/mcp-contract-drift-audit.md`, раздел 3, и до сих пор не исправлено.

## 4. Закрытие, которое доезжает только до локального дерева

Цепочка из `docs/runbooks/CLOSE-PROCEDURE.md` и скилла `beacon-executor` предполагает, что после `merged` карточка получает `done`. Механика работает до записи файла:

- `cibridge` вызывает `CloseMerged` только для `VerdictMerged` (`internal/infra/cibridge/verdict_applier.go:427-440`).
- `deferredCardLinks.CloseMerged` ищет связь через `linkedCardID` и вызывает `closeLinkedCardFile` (`cmd/beacon/mcp.go:56-92`).
- `closeLinkedCardFile` меняет `Статус`, чистит `Состояние` и `Вопрос`, добавляет маркер `PR #N MERGED` и пишет файл через `os.WriteFile` (`cmd/beacon/mcp.go:153-180`).

После этого цепочка обрывается. Коммит, пуш и PR не создаются. То же самое делает `beacon_task_close`: меняет статус и ledger, но не оставляет артефакта в git. Поэтому карточка в `origin/main` остаётся `todo`. В скилле `beacon-executor` этот случай назван веткой 6b и требует отдельного PR только с изменением карточки. Runbook и карточка об этом не напоминают.

Есть две дополнительные дыры на этом участке.

- `db.Link` пишет связь только после успешной регистрации Watch, а ошибка linkage логируется и не ломает вызов (`cmd/beacon/mcp.go:589-603`). Watch без связи не закроет карточку при merge.
- `linkedCardID` берёт одну строку через `LIMIT 1` (`cmd/beacon/mcp.go:110`). Если с одним Watch связано несколько карточек, лишняя останется без закрытия.

## 5. Дыры идемпотентности

Ключ `(cardID, status, reason)` работает внутри одного ledger, но не покрывает всё поле входа (M07). Повтор с изменённым `agentID` или `duplicateOf` считается тем же вызовом.

Ключ не общий для разных точек входа. CLI `beacon task done` пишет `Итог` и `Доказательство` через `taskstore`, а MCP-инструмент пишет свой ledger. После CLI-закрытия повторный MCP-вызов упирается в терминальный статус и получает конфликт. После MCP-закрытия CLI видит терминальный статус и тоже отказывается. Повторного закрытия не происходит, но и подтверждения идемпотентности нет.

Третья дыра это замки (M08). Claim, MCP close и CLI close блокируют один и тот же файл разными файлами блокировок. Условие гонки между чтением `Агент` и записью `Агент` остаётся.

Четвёртая дыра это маркер в `closeLinkedCardFile`. Проверка ищет подстроку `PR #N MERGED` в тексте карточки (`cmd/beacon/mcp.go:170-173`). Если такой текст уже есть в другом разделе, запись пропускается.

## 6. Заметка про ID карточек ATX-M*

Карточки этого исследования названы `ATX-M01` … `ATX-M08`, как задано в задании. Формат не поддерживается инструментами репозитория.

- `internal/infra/taskstore/store.go:94-100` принимает ID только с цифрами после дефиса или точки. `ATX-M01` не проходит. `ParseCard` возвращает ошибку (`:146-147`), а `LoadCards` прерывает всю загрузку (`:297-316`). Значит `beacon index build`, `beacon_task_claim` и `beacon_task_close` не увидят эти карточки.
- `tools/beacon-gates/cardindex/check.go:39-41` не содержит ATX в `idPattern`, поэтому gate `cardindex` не включает ATX-карточки в `docs/ROADMAP_STATUS.md`. Это верно и для существующих `ATX-01` … `ATX-44`.

Предложение: расширить оба набора шаблонов до `<FAMILY>-M<NN>` или сразу назначить этим карточкам числовые ID. Пока решение не принято, карточки ATX-M* остаются артефактом исследования и не участвуют в claim.

## 7. Что не проверялось

Тесты, сборка, гейты, `beacon index build`, CLI Beacon и ручной merge не запускались. Числа строк и номера строк сняты чтением файлов на базе `33270854`. PR с этим документом регистрируется через `beacon_watch_pr` с `autoMerge: true`, решение о мерже принимает Beacon.
