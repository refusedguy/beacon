# Таксономия зависших PR и тихих отказов

Дата: 2026-11-22. Режим: чтение. Код не менялся, `go test`, `go build` и гейты не запускались.

Источники два. Первый: структурированные логи Beacon через `.opencode/bin/beacon logs --format json --tail 0`, запущенный из каталога `beacon-impl`, где лежит состояние демона. Второй: `git grep`, `git show` и `git log` по `origin/main` = `a550011a`. Логи покрывают период с `2026-11-20T14:34:08+05:00` по `2026-11-22T17:36:47+05:00` и содержат 96 955 записей. Все ссылки на строки кода проверены по `a550011a`.

## Метод сбора

Команда из задания `beacon logs --errors --tail 200` в каталоге `beacon-handoff` не даёт событий: там читается другое состояние, и в нём только записи `command_done`. Из каталога `beacon-impl` та же команда возвращает 854 записи, но окно в 200 строк содержит преимущественно `cycle_start`, `cycle_end`, `observe` и `ci_points`. В последних 200 строках лежат 7 `actor_error` и 4 `wake_skipped`, ни одного `merge_fail`. Поэтому частоты ниже считаны по всему корпусу через `--tail 0`.

`--errors` отбирает записи уровня `ERROR` и записи с полем `error`. События `wake_skipped` имеют уровень `INFO` и в эту выборку не попадают, поэтому их частоты взяты из полного потока. События `merge_fail` в 32 из 70 случаев не несут поля `error`, поэтому их причины восстановлены по соседним событиям и по коду.

## Сводка

| ID | Причина | Сигнал в логах | Частота | Где возникает | Где обрабатывается | Карточка |
| --- | --- | --- | --- | --- | --- | --- |
| A | `required:false` не двигает вердикт | Прямого сигнала нет | 0 записей | `internal/infra/ghci/provider.go:287`, `internal/app/orchestrator/checkall.go:460` | `internal/domain/verdict.go:216`, `:237`; `internal/app/orchestrator/checkall.go:464` | ST-01 |
| B | Усечённый клон даёт другой счёт cardindex | `verdict_published`, где `failedChecks` содержит `cardindex` | 81 запись: 55 `keep_working`, 15 `rebase`, 9 `block`, 2 `blocked` | `tools/beacon-gates/cardindex/check.go:174`, `.github/workflows/ci.yml:299` | `tools/beacon-gates/cardindex/check.go:102`, `:118` | ST-02 |
| C | Расхождение байтов `gates-binary` из-за версии Go | Прямого сигнала в логах демона нет | 0 записей | `.github/workflows/gates-binary.yml:41-59`, `docs/adrs/0023-gates-binary-toolchain.md:13` | `.github/workflows/gates-binary.yml:53-59` | ST-03 |
| D | TLS timeout при `gh pr merge` | `merge_fail` с `error`, `actor_error` провайдера | 25 `merge_fail`, 74 `actor_error`, 8 `merge_fail` с другими сетевыми ошибками | `internal/infra/github/batch_provider.go:350`, `internal/app/orchestrator/orchestrator.go:516` | `internal/infra/ghci/provider.go:208` только для чтения PR, для merge нет | ST-04 |
| E | Устаревшие счётчики `ROADMAP_STATUS.md` | Те же 81 запись cardindex, плюс 9 `block` | см. B | `docs/ROADMAP_STATUS.md:8`, `tools/roadmap-sync/main.go` | `tools/beacon-gates/cardindex/check.go:118` сравнивает индекс, но не числа в документе | ST-05 |
| F | Watch на старом SHA после push | `head_changed`, `actor_error` `watch_pr-head` | 206 `head_changed`, 2 `INVALID_INPUT: headSHA is required` | `internal/app/orchestrator/checkall.go:893`, `internal/infra/mcp/tool_watch_pr.go:724` | `internal/infra/cibridge/verdict_applier.go:16`, `internal/infra/mcp/tool_watch_pr.go:780`, `internal/domain/transition.go:312` | ST-06 |
| G | Обязательства подписчика | `wake_skipped` | 2 209 записей, разбивка ниже | `internal/infra/wake/deliver.go:278` | `internal/infra/cibridge/verdict_applier.go:652`, `:837`, `internal/infra/cibridge/bridge.go:412`, `:432` | ST-07 |

## A. Информационные проверки не меняют вердикт

`internal/infra/ghci/provider.go:287` выставляет `Required: true` каждому чеку из `gh pr checks`. Провайдер GraphQL в `internal/app/orchestrator/checkall.go:743` читает поле `isRequired`, и `informational()` на строке 460 помечает чек как информационный. Дальше движок вердикта намеренно пропускает такой чек: `internal/domain/verdict.go:225` убирает его из base-failure, `:239` пересобирает rollup, `:333` убирает из `realFailures`. Комментарий на строке 216 прямо описывает случай с `required:false` на базовой ветке.

Поведение корректное, но молчаливое. Watch остаётся в `watching` или `ready`, хотя check красный, и в логах нет записи о том, что провал проигнорирован по признаку `required:false`. Branch protection на main не настроен, что зафиксировано в `docs/research/ci-gates-gaps.md:22` и карточке `ATX-C01.md`. В логах демона строк с `required:false` нет: единственные две записи со словом `required` относятся к `headSHA is required`. Значит, частота причины A по логам равна нулю, и её стоит мерить отдельным событием, а не искать в существующих.

ST-01 предлагает писать предупреждение при регистрации Watch и при каждом пересчёте вердикта, если все чеки информационные или если красный чек был выброшен по `required:false`.

## B. Усечённый клон и счёт cardindex

`tools/beacon-gates/cardindex/check.go:174` запускает `git log --format=%s` как дочерний процесс. Счёт карточек зависит от того, какие коммиты видны. В `.github/workflows/ci.yml:299` стоит `fetch-depth: 0`, и комментарий на строке 288 объясняет, что усечённая история ломает пересборку. Локальный запуск гейта в клоне с глубиной 1 даёт цифры 839 вместо 885. Число 885 записано в `docs/ROADMAP_STATUS.md:8`, и в логах нет строк с 839 или 885. Это известный симптом сессии, а не измерение по корпусу.

В логах причина проявляется как 81 запись `verdict_published` с `failedChecks`, содержащим `cardindex`: 55 раз вердикт `keep_working`, 15 `rebase`, 9 событий `block` и 2 вердикта `blocked`. Записи распределены по десяткам PR, от 1105 до 1397, с максимумом восемь записей на PR 1111. Дополнительно `docs/research/research-docs-consistency.md:27` показывает, что `idPattern` в `cardindex/check.go:39` не покрывает новые семейства карточек, поэтому расхождение может быть вызвано не только глубиной клона.

ST-02 предлагает проверять глубину истории перед счётом и печатать явную причину, если `git log` усечён.

## C. Байты `gates-binary` и версия Go

ADR `docs/adrs/0023-gates-binary-toolchain.md:13` фиксирует случай: сборка на Go 1.27.4-X:nodwarf5 дала хеш `c0af0eb...`, сборка CI на Go 1.26.0 дала `d6d76a9...`. Причина в `GOEXPERIMENT=nodwarf5`, который попадает в build info. Workflow `.github/workflows/gates-binary.yml:41-59` сравнивает байты через `cmp -s` и при расхождении печатает команду пересборки. Демон об этом не пишет, поэтому в логах демона нет ни одной записи с `gates-binary`.

ST-03 предлагает добавить проверку версии тулчейна до сравнения байтов и печатать расхождение `go.mod` и локального `go version` как отдельную причину.

## D. TLS timeout при merge

Все 596 записей `actor_error` распределены так: 188 ошибок `RATE_LIMITED` от адаптеров `cibridge-batch` и `batchci`, 74 `TLS handshake timeout`, 55 других сетевых ошибок, 118 `PERSIST_FAILED`, 93 ошибки мёртвой сессии или обязательства, 23 ошибки разбора ID карточки, 8 `WATCH_NOT_FOUND`, 2 `ladder`, 2 `headSHA is required` и 33 прочие.

В `merge_fail` 70 записей: 25 `TLS handshake timeout`, 8 других сетевых ошибок, 3 `Merge already in progress`, 1 `RATE_LIMITED`, 1 прочая и 32 без поля `error`. Конкретные строки выглядят так: `gh pr merge 1527 --repo refusedguy/beacon --squash --delete-branch: ... net/http: TLS handshake timeout`.

`internal/infra/ghci/provider.go:66` оценивает TLS handshake timeout как переходную ошибку и `:208` удваивает задержку для чтения PR. Путь merge через `internal/app/orchestrator/orchestrator.go:516` и `internal/infra/github/batch_provider.go:350` такой защиты не имеет: `observe.MergeFailure` пишет ошибку, и Watch остаётся в состоянии готового к merge. ST-04 предлагает перенести логику переходных ошибок на путь merge с ограниченным числом повторов и явным предупреждением после исчерпания.

## E. Устаревшие счётчики

`docs/ROADMAP_STATUS.md:8` содержит `Закрыто done: 885`, и там же на строке 12 сказано, что три числа считает cardindex. Гейт `tools/beacon-gates/cardindex/check.go:96` читает закоммиченный индекс, `:102` пересобирает счётчики и `:118` печатает расхождение. Он сравнивает индекс, но не сверяет числа в самом `ROADMAP_STATUS.md`. В `docs/tasks/WIRING-489.md:35` зафиксирован случай, когда счётчики в ветке остались от main на 839, хотя пересборка давала 648.

ST-05 предлагает отдельную проверку чисел в документе и автоподстановку через `tools/roadmap-sync --write` в PR с явным diff.

## F. Watch на старом SHA

В корпусе 206 событий `head_changed`. Два события `actor_error` от `watch_pr-head` сообщают `INVALID_INPUT: watch_pr: headSHA is required when no CI provider is configured`.

Механизм закрытия старого Watch есть: `internal/infra/cibridge/verdict_applier.go:16` вызывает `domain.HeadChangedEvent`, `internal/domain/transition.go:312` закрывает Watch с причиной `head_changed` и создаёт эффект `EffectCreateWatch`, а `internal/infra/mcp/tool_watch_pr.go:780` переносит подписчиков. `internal/app/orchestrator/checkall.go:893` не публикует вердикт, если `headSHA` в хранилище отличается от присланного. Точка отказа в том, что сравнение происходит только при публикации вердикта и при явном вызове `watch_pr`. Если push пришёл между циклами, Watch продолжает опрашивать старый PR до ближайшего события. ST-06 предлагает проверять `HeadRefOid` провайдера на каждом цикле и инициировать переподписку без ожидания вердикта.

## G. Обязательства подписчика

`wake_skipped` распределены так:

| Причина | Число | Строка в коде |
| --- | --- | --- |
| `block notice already delivered for this incident` | 2 071 | `internal/infra/cibridge/verdict_applier.go:844` |
| `adapter circuit breaker open: wake deferred` | 71 | `internal/infra/cibridge/bridge.go:412` |
| `terminal verdict: no bound orchestrator` | 44 | `internal/infra/cibridge/verdict_applier.go:652` |
| `blocked Watch has no orchestrator address` | 11 | `internal/infra/cibridge/verdict_applier.go:837` |
| `terminal confirmation already delivered for this revision` | 8 | `internal/infra/cibridge/verdict_applier.go:662` |
| `adapter circuit breaker open` | 4 | короткая формулировка из более ранней версии, сейчас тот же случай пишет `internal/infra/cibridge/bridge.go:412` |

Большинство записей идут от повторной доставки того же уведомления, что само по себе не поломка. Проблема в 44 и 11 записях: Watch дошёл до терминального вердикта или до блокировки, но адреса оркестратора нет. В логах также 27 событий `wake_error`, 624 записей со словом `obligation` и 30 строк с `not the subscriber of obligation` в `internal/infra/wake/deliver.go:278`. ST-07 предлагает разделить ожидаемые повторы и настоящие потери подписчика, а для второй группы перепривязывать обязательство при регистрации и писать отдельный metric.

## Что не подтверждено

Число 839 как результат поверхностного клона не найдено ни в логах, ни в репозитории. Оно взято из описания сессии. Расхождение байтов `gates-binary` в логах демона не регистрируется, поэтому частота причины C равна нулю только для этого источника. Причины A и C не имеют прямых log-сигналов, и карточки ST-01 и ST-03 предлагают такой сигнал добавить.

## Карточки

ST-01. Предупреждение о `required:false` при старте Watch.
ST-02. Детект усечённой истории перед счётом cardindex.
ST-03. Диагностика версии Go при расхождении `gates-binary`.
ST-04. Повтор transition-ошибок merge с ограниченным бюджетом.
ST-05. Сверка чисел `ROADMAP_STATUS.md` с пересобранным индексом.
ST-06. Детект устаревшего headSHA и автопереподписка Watch.
ST-07. Разделение ожидаемых повторов wake и потерь подписчика.
