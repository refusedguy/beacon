# Разрывы переходов Watch, Blocker и Obligation

Дата: 2026-11-22. База: `origin/main` `d2b36cbf`. Режим: только чтение кода. Локально не запускались `go test`, `go build`, `beacon gates`, `batch-apply`, живой мост и ручной merge. Тест-кейсы в таблице описаны как ожидаемое поведение для карточек, они ещё не написаны. Проверки выполнит CI на PR.

## Область и метод

Изучены `internal/domain/transition.go`, `internal/domain/fanout.go`, `internal/infra/cibridge/ci_refresh.go`, `internal/infra/cibridge/verdict_applier.go`, `internal/infra/cibridge/bridge.go`, `internal/infra/mcp/tool_ack.go`, `internal/infra/store/blockers.go`, `internal/infra/store/db.go` и их тесты. Поиск шёл через `git grep` по явным каталогам, без рекурсивного обхода `worktrees/` и `tmp/`. Номера строк сняты на зафиксированном коммите `d2b36cbf`.

Не повторяются следующие источники.

- `docs/research/watch-lifecycle-gaps.md`: GC обязательств, снимок GC, poller `WatchOutcome`, гонка `head_changed`, `CloseWatch` без ревизии, `SharedGate.Wait`.
- `docs/research/mcp-close-chain-gaps.md`: tools/list, локальная запись карточки, замки claim/close, `duplicateOf`.
- `docs/research/research-verdict-contract.md`, карточка VC-03: переходы `blocked -> parked`, `blocked -> needs_input`, `parked -> blocked`, `needs_input -> blocked` и потеря `Blocked`. Эти переходы здесь не описаны.
- `AUD-02`, `AUD-05`, `INTG-02`: поля Prompt, три замка карточки и подключение `GenerationLedger`. Генерации здесь не рассматриваются.
- `ADR 0090`: разделение registry, вердикта и квитанции. Находки ниже не меняют это разделение.

## Таблица разрывов

| ID | Место, символ | Риск и сценарий | Предложение | Тест-кейс |
| --- | --- | --- | --- | --- |
| LC-01 | `internal/domain/transition.go:483-485`, `:508-516`, `transitionVerdict`; `internal/infra/cibridge/ci_refresh.go:40`, `fetchSnapshots`; `internal/infra/cibridge/verdict_applier.go:145`, `:316`, `applyVerdict`, `publishRecheck` | Watch в `needs_input` получает вердикт CI. Guard в `transitionVerdict` перечисляет `closed`, `parked`, `blocked`, но не `needs_input`. `fetchSnapshots` тоже отбирает все статусы кроме `closed` и `blocked`. Апдейтер публикует actionable вердикт, `transitionVerdict` ставит `notifying`, инкрементирует revision и возвращает `EffectWakeSubscribers`. Агент ждёт ответа человека, а оркестратор уже получает wake по `ready` или `retry`. Для `VerdictMerged` тот же путь закрывает Watch с `closeReason=merged`, хотя документ переходов допускает из `needs_input` только cancel, head changed и accept | Добавить `StatusNeedsInput` в guard `transitionVerdict`, `applyVerdict`, `publishRecheck` и в отбор `fetchSnapshots`. Пока человек не ответил, CI меняет только проверки Watch, но не статус и не обязательства | Watch `needs_input` revision 5, приходит `VerdictReady`. Статус остаётся `needs_input`, revision 5, эффектов нет, обязательств новых нет. Ответ `continueOf` переводит ту же Watch в `watching` revision 6 |
| LC-02 | `internal/infra/cibridge/verdict_applier.go:723`, `applyBlocked`, `:829-832`, `notifyBlocked`, `:973-1015`, `unblockFixed`; `internal/infra/store/blockers.go:164-185`, `GetOrCreateOpenBlocker` | `unblockFixed` возвращает целевую Watch из `blocked` в `watching` и крутит generation, но не вызывает `ResolveBlocker`. Инцидент остаётся открытым. `GetOrCreateOpenBlocker` ищет открытый инцидент по `(repo, baseRef, signature)`. Повторный блок с теми же проверками получает тот же ID, а `notifyBlocked` пропускает wake, потому что обязательство `blocker:<ID>` уже доставлено. Оркестратор не узнаёт о новом блоке. `ActiveBlockers` показывает открытый инцидент без заблокированных Watch | Разрешать инцидент в той же транзакции, где `unblockFixed` пишет `watching`. Передавать список разблокированных Watch в `rotateGenerations`. Проверить, что повторный блок с той же сигнатурой создаёт новый dedup-ключ и новый wake | Инцидент I1 с `ci/test`, wake доставлен. Мерж фиксера вызывает `unblockFixed`. Инцидент закрыт, `ActiveBlockers` пуст. Новый блок с `ci/test` открывает I2 и доставляет второй wake |
| LC-03 | `internal/infra/mcp/tool_ack.go:896-907`, `continueWatch`, `:900`, `ContinuationRegistry.Next`; `internal/domain/transition.go:55`, `MaxContinueRounds` | Раунд списывается до проверки статуса. Ack с корректной ревизией и `continueOf` на Watch, которая уже вышла из `needs_input`, расходует round, затем `Transition` возвращает no-op и метод даёт ошибку `CodeLimitExceeded`. Обязательство не получает `claimedAt`, потому что возврат происходит до `StampClaimed`. Пять таких ack исчерпают лимит в пять раундов, и настоящий ответ человека будет отвергнут | Сначала проверить `w.Status == StatusNeedsInput` и совпадение `ContinueOf`, потом вызвать `Next`. При отказе перехода не списывать round | Watch в `watching` revision 6. Ack с `continueOf` и revision 6 возвращает ошибку, `Rounds` равен 0. Следующий ответ на припаркованную Watch получает round 1 и запускается |
| LC-04 | `internal/infra/cibridge/ci_refresh.go:40`, `fetchSnapshots`; `internal/infra/cibridge/bridge.go:887-891`, `Refresh`; `internal/infra/cibridge/verdict_applier.go:564-566`, `closeWatch`; `internal/domain/transition.go:485`, `transitionVerdict` | Watch в `blocked` исключён из CI-опроса, а ветка `Refresh` для заблокированной Watch только steer'ит оркестратора. Если PR внешне merged или closed, вердикт до `applyVerdict` не доходит. Даже при наличии снимка `closeWatch` вызывает `transitionVerdict`, который заблокированную Watch не закрывает. Watch остаётся в `LiveWatches`, инцидент открыт, карточка не закрывается | Опрашивать снимок для заблокированной Watch. Перед веткой `blocked` обрабатывать терминальное состояние PR отдельным путём закрытия, который не зависит от `transitionVerdict`. Помечать закрытие как `merged` или `closed` и разблокировать связанный инцидент | Watch `blocked`, провайдер сообщает `MERGED`. За один цикл `Refresh` статус `closed`, `closeReason=merged`, инцидент закрыт, карточка получила `CloseMerged` |
| LC-05 | `internal/domain/fanout.go:124-133`, `Subscribe`, `:215-219`, `findByAlias`, `:232`, `normalizeWatch`; `internal/infra/store/db.go:649-653`, `LiveWatches`; `internal/infra/mcp/tool_watch_pr.go:749`, `closeSuperseded` | `LiveWatches` содержит только `watching`, `notifying`, `blocked`. Поиск алиаса при повторной регистрации не видит Watch в `needs_input` и `parked`. `normalizeWatch` получает новый ID с суффиксом времени, `closeSuperseded` старую Watch тоже не закрывает. После рестарта исполнителя на том же SHA существуют две Watch на один алиас: старая с неотвеченным вопросом и новая в `watching`. Ответ `continueOf` адресует старую, оркестратор видит новую | Включить `needs_input` и `parked` в набор, который используют `findByAlias` и `closeSuperseded`. При повторной регистрации на тот же алиас продлевать существующую Watch, а не создавать вторую | Watch в `needs_input`, те же repo, PR, SHA. Повторный `beacon_watch_pr` возвращает тот же watchID, старая Watch сохраняет вопрос и revision, новых Watch нет |

## P0

Только LC-01. Watch с открытым вопросом получает wake из CI и может быть закрыта до ответа человека. Это нарушает правило ADR 0031 о том, что ответ продолжает ту же Watch. Исправление локализовано в трёх файлах, владельцы карточки не пересекаются с владельцами других карточек этой фазы.

## P1

LC-02 и LC-03. Первый разрыв превращает открытый инцидент в источник пропущенного wake. Второй расходует лимит человеческих ответов на сообщения, которые не возобновляют Watch. Оба требуют правки в разных слоях и не меняют контракт MCP-инструментов.

## P2

LC-04 и LC-05. Первый оставляет заблокированную Watch навсегда после внешнего мержа. Второй создаёт вторую Watch на тот же алиас, когда исполнитель перерегистрируется во время ожидания ответа или после парковки. Оба чаще всего проявляются после рестарта агента, поэтому идут после исправлений P0 и P1.

## Владельцы файлов

| Карточка | Владельцы |
| --- | --- |
| LC-01 | `internal/domain/transition.go`, `internal/domain/transition_test.go`, `internal/infra/cibridge/ci_refresh.go`, `internal/infra/cibridge/verdict_applier.go` |
| LC-02 | `internal/infra/cibridge/verdict_applier.go`, `internal/infra/store/blockers.go` |
| LC-03 | `internal/infra/mcp/tool_ack.go`, `internal/infra/mcp/tool_ack_test.go` |
| LC-04 | `internal/infra/cibridge/ci_refresh.go`, `internal/infra/cibridge/bridge.go`, `internal/infra/cibridge/verdict_applier.go` |
| LC-05 | `internal/domain/fanout.go`, `internal/domain/fanout_test.go`, `internal/infra/store/db.go`, `internal/infra/mcp/tool_watch_pr.go` |

Внутри одной фазы владельцы не пересекаются. P0 содержит только LC-01. В P1 LC-02 владеет `verdict_applier.go` и `blockers.go`, LC-03 владеет файлами `tool_ack*`. В P2 LC-04 владеет файлами `cibridge`, LC-05 владеет `fanout`, `db.go` и `tool_watch_pr.go`. Все перечисленные пути существуют на базе `d2b36cbf`.

## Что не проверялось

Живой прогон моста, SQLite, MCP-сервера и CI не выполнялся. Гипотезы проверены чтением кода и существующих тестов на зафиксированном коммите. Тест-кейсы в таблице не запускались. Пакет `research-lifecycle-batch.json` содержит пять карточек LC-01..LC-05, eval `lifecycle-transition-guards` и факты с владельцами.
