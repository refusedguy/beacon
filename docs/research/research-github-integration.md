# Разрывы GitHub-интеграции: PR, merge, checks, правила

Дата: 2026-11-22.
База: origin/main, коммит `61e3dac0`.
Ветка: `research/github-integration`.
Объём: `internal/infra/github/`, `internal/infra/cibridge/`, `cmd/beacon/prcreate.go`, `cmd/beacon/registry_check.go`, `.github/workflows/ci.yml`.
Метод: чтение кода и тестов на origin/main, сверка с сигнатурами `go-github/v93`, поиск существующих карточек по OwnerFiles. Локальные `go test`, `go build` и `beacon gates` не запускались по ADR 0022. Проверка статическая.

Исключено по заданию: BCX-52 (`batch_provider.go`), BCX-17/18, AUD-01 (`mergePolicyHold`), `verdict_applier.go`, merge в cibridge. Дополнительно не созданы карточки на `ci_refresh.go` и на политику авто-мержа: эти файлы уже заняты карточками ATX-V01, ATX-V07, ATX-W06 и HD-074 в статусе todo или done. Их я только проверил.

Главный вывод. Merge-путь не фиксирует ожидаемый head SHA. Проверенное состояние читается до записи, а в PUT уходит только метод мержа. Параллельный push меняет head без повторного вердикта. Второй разрыв того же класса: async rebase возвращает SHA после одного чтения, хотя GitHub обновляет ветку в фоне. Третий разрыв лежит в учёте квот: счётчик очков сравнивает заголовки GraphQL шкалы 5000 с собственным бюджетом 500.

## Таблица находок

| ID | Область | Разрыв | Доказательство, symbol и file:line | Приоритет |
| --- | --- | --- | --- | --- |
| GH-01 | Merge, REST | PUT merge не передаёт ожидаемый head SHA | `RestClient.MergePullRequest`, `internal/infra/github/rest.go:242`, `rest.go:249`; `PullRequestOptions.SHA`, `pulls.go:502` модуля go-github | P0 |
| GH-02 | Rebase, REST | PUT update-branch принимает 202 и читает head один раз до завершения фонового обновления | `RestClient.RebasePullRequest`, `rest.go:273`, `rest.go:277`, `rest.go:282`, `rest.go:288`, `rest.go:293`; тест `TestRebasePullRequestReturnsNewHead`, `rebase_test.go:28`, `rebase_test.go:35` | P1 |
| GH-03 | Merge, CLI | `PRClient.MergePR` выбрасывает результат и возвращает nil при `merged=false` | `PRClient.MergePR`, `internal/infra/github/pr_create.go:194`, `pr_create.go:201`, `pr_create.go:204`; `githubPRProvider.Merge`, `cmd/beacon/prcreate.go:171`, `prcreate.go:176` | P1 |
| GH-04 | Rate limits | `Counter.Observe` пишет в `used` заголовки GraphQL шкалы 5000, а порог равен 500 | `BudgetPerHour`, `points.go:20`; `Counter.Observe`, `points.go:132`; `Counter.apply`, `points.go:204`; `observeHeaders`, `points.go:225`; вызовы в `graphql.go:407` и `tier2.go:239`; тест `graphql_test.go:320` | P1 |
| GH-05 | Stale head, tier-1 | `NeedsTier2` не сравнивает HeadSHA, поэтому контексты остаются от прежнего head | `Tier1Observation`, `tier1.go:52`; `NeedsTier2`, `tier1.go:63`, `tier1.go:71`; `observationFrom`, `tier1.go:111`; `dedupBySHA`, `tier1.go:228` | P2 |

## GH-01. Merge без ожидаемого head SHA

`RestClient.MergePullRequest` определён в `internal/infra/github/rest.go:242`. Он собирает `gh.PullRequestOptions` только с полем `MergeMethod` в `rest.go:249`. В `go-github/v93` поле `SHA` структуры `PullRequestOptions` описано как ожидаемый head SHA merge и расположено в `pulls.go:502`. Метод `PullRequestsService.Merge` перекладывает это поле в тело запроса в `pulls.go:564`. Вызов в Beacon его не заполняет.

Верхний слой читает состояние до записи. `BatchProvider.MergePR` вызывает `b.PRState` в `batch_provider.go:364`, затем `mergePRWithState` проверяет `State`, `Mergeable` и `MergeStateStatus` в `batch_provider.go:388` и только в `batch_provider.go:431` вызывает `MergePullRequest`. Вариант `MergePRWithState` в `batch_provider.go:376` использует уже готовый снимок батча, то есть окно между наблюдением и записью ещё шире. Если за это окно в ветку попадёт новый коммит, GitHub смержит уже другой head, а вердикт относился к прежнему.

Коды 405 и 409 преобразуются в `merge_refused` в `rest.go:253`-`259`. Это защита от отказа GitHub, а не от подмены head. Когда head изменился, но конфликта нет, отказа не будет.

Что делать. Передавать observed head SHA в `PullRequestOptions.SHA`. Источник значения уже есть в `domain.PRState` и в `MergeState`. При отказе по несовпадению SHA возвращать отдельную причину и держать Watch, а не переходить к повторному мержу без нового цикла CI.

Базовый замер: один вызов `MergePullRequest` в `rest.go:249`, два верхних вызова в `batch_provider.go:431` и `pr_create.go:201`; поле `SHA` в option не заполняется ни в одном из трёх мест.

## GH-02. Rebase возвращает SHA до завершения обновления

`RestClient.RebasePullRequest` определён в `rest.go:273`. Он отправляет `UpdateBranch` в `rest.go:277`. Комментарий в `rest.go:270`-`272` прямо говорит, что GitHub отвечает 202 и планирует обновление в фоне. Код считает `gh.AcceptedError` успехом в `rest.go:282`-`286`. После этого он один раз читает PR в `rest.go:288` и возвращает `pr.GetHead().GetSHA()` в `rest.go:293`.

Тест `TestRebasePullRequestReturnsNewHead` в `rebase_test.go:15` возвращает 202 в `rebase_test.go:28` и сразу отдаёт новый SHA в `rebase_test.go:35`. Проверка в `rebase_test.go:53`-`58` ожидает ровно два HTTP-вызова. Сценарий, где GET после 202 ещё видит старый head, не покрыт. В этом случае метод вернёт старый SHA как новый, и последующий повтор мержа будет построен на устаревшем наблюдении.

Что делать. После Accepted читать PR повторно до тех пор, пока head SHA не отличается от прежнего, с ограничением числа попыток и таймаутом. Возвращать ошибку, если обновление не подтвердилось. Добавить тест на последовательность HEAD, HEAD, NEW.

Базовый замер: один GET после PUT, нулевой цикл ожидания, один негативный тест на отсутствие head в `rebase_test.go:100`.

## GH-03. MergePR теряет confirmation result

`PRClient.MergePR` определён в `internal/infra/github/pr_create.go:194`. В `pr_create.go:201` он игнорирует `*PullRequestMergeResult`: `_, _, err := c.gh.PullRequests.Merge(...)`. Метод `PullRequestsService.Merge` в `pulls.go:554`-`580` возвращает ошибку только когда `client.Do` её вернул. Ответ HTTP 200 с полем `merged=false` ошибкой не является. После блока `if err != nil` в `pr_create.go:204` функция возвращает nil в `pr_create.go:207`. Вызывающий получает успех при неподтверждённом мерже.

Для сравнения, `RestClient.MergePullRequest` проверяет `result == nil || !result.GetMerged()` в `rest.go:262` и возвращает отдельную причину `merge_not_confirmed`. Второй разрыв того же метода: `PullRequestOptions` в `pr_create.go:201`-`203` содержит только `MergeMethod`, то есть ожидаемый SHA здесь тоже отсутствует.

`githubPRProvider.Merge` в `cmd/beacon/prcreate.go:171` лишь делегирует вызов в `prcreate.go:176`. Он не видит результат и не может сообщить об отказе.

Что делать. Возвращать структуру с полями `merged`, `reason`, `sha`, проверять `GetMerged()` и передавать ожидаемый SHA. Обновить адаптер в `cmd/beacon/prcreate.go` под новый контракт.

Базовый замер: один вызов `Merge`, ноль проверок результата, ноль полей SHA в option.

## GH-04. Смешение шкал в учёте квот

`BudgetPerHour` равен 500 в `points.go:20`. `Counter.Observe` в `points.go:132` передаёт заголовки ответа в `observeHeaders` и число из тела в `Counter.apply` (`points.go:204`). Функция `observeHeaders` в `points.go:225` сначала читает `x-ratelimit-used`, затем пару `x-ratelimit-limit` и `x-ratelimit-remaining` в `points.go:232`-`241`. `Counter.apply` в `points.go:210`-`214` при неотрицательном `obs.used` записывает его в `c.used` и полностью игнорирует `bodyCost`.

GraphQL-клиент вызывает `Observe` на каждом ответе в `graphql.go:407`. Клиент tier-2 делает то же в `tier2.go:239`. Тест `TestGraphQLWindowResetIsNotRateLimited` ставит `x-ratelimit-remaining` в 4999 в `graphql_test.go:320`, то есть реальный ответ GraphQL несёт заголовки шкалы 5000. Тест `TestHeadersOverrideBodyCost` закрепляет в `points_test.go:113`-`117`, что `limit=5000` и `remaining=4600` дают `used=400`.

Итог: после GraphQL-ответа `used` хранит число из шкалы 5000, а `BudgetExhausted` в `points.go:168` и `LimitReached` в `points.go:192` сравнивают его с 500. Возможны оба направления ошибки. Большое значение из GraphQL преждевременно включает backpressure. Малое значение перезаписывает накопленные REST-очки и позволяет потратить больше 500. Побочные эффекты видны в `Poller.Interval` (`tier1.go:39`-`47`) и в `LaunchGate.Admit` (`points.go:348`-`356`).

Что делать. Разделить REST core и GraphQL в отдельные счётчики или нормализовать заголовки GraphQL к бюджету 500 до записи. Тест должен прогонять GraphQL-ответ с `x-ratelimit-remaining: 4600` и проверять, что локальный бюджет не равен 400.

Базовый замер: два места вызова `Observe` с GraphQL-ответами, один порог 500, одна формула шкалы 5000 в тестах.

## GH-05. Tier-1 не реагирует на смену head

`Tier1Observation` содержит поле `HeadSHA` в `tier1.go:52`. Парсер `observationFrom` заполняет его из `headRefOid` в `tier1.go:111`. Функция `NeedsTier2` в `tier1.go:63` проверяет только `UNKNOWN` и сравнение `Rollup` и `MergeStateStatus` в `tier1.go:70`-`71`. Поле `HeadSHA` в сравнении не участвует.

Если push меняет head, но оставляет `rollup=PENDING` и `mergeStateStatus=CLEAN`, тир-2 не вызывается. Карта контекстов продолжает относиться к прежнему коммиту. Функция `dedupBySHA` в `tier1.go:228` тоже рассчитывает, что смена SHA попала в список `changed`, но этот список строится по `NeedsTier2`.

Оговорка. В текущем дереве `NewPoller` встречается только в тестах пакета. Рабочий контур читает батч через `BatchProvider.FetchBatch`, который запрашивает поля tier-2 в каждом цикле. Поэтому находка имеет ранг P2 и не влияет на текущий verdict path.

Что делать. Добавить сравнение `prev.HeadSHA != curr.HeadSHA` в `NeedsTier2` и тест на переход PENDING со смененным SHA.

Базовый замер: одно поле `HeadSHA`, одно место сравнения, которое его не использует.

## Проверено, новых разрывов не найдено

Авто-мерж. Политика собрана в `internal/app/orchestrator/decision.go` и `orchestrator.go`. Карточка HD-074 фиксирует приоритет blocked, затем правки сгенерированных файлов, затем waitFor, затем ready. Запрет на дублирование AUD-01 и `verdict_applier.go` соблюдён. Новых карточек нет.

Сопоставление заключений. Расхождение между `NormalizeConclusion` в `internal/domain/verdict.go:300` и `checkFacts` в `internal/infra/cibridge/ci_refresh.go:100` уже разобрано в ATX-V07 и ATX-V01. `ci_refresh.go` занят этими карточками, поэтому я его не трогал.

`rules-broken`. Джоба определена в `.github/workflows/ci.yml:315`. Команда `./beacon gates rules` выполняется в `ci.yml:341`, код сохраняется в `ci.yml:342`, ветка 2 печатает BROKEN в `ci.yml:347`-`348`, а `exit "$code"` в `ci.yml:354` сохраняет ненулевой статус. Маркер `R-TOOL-BROKEN` задан в `cmd/beacon/commands.go:74`. Разрыва нет.

`registry-check`. `checkRegistryConfig` в `cmd/beacon/registry_check.go:220` вызывает проверку схемы, лимитов и индекса. Индекс строится из того же roster в `registry_check.go:203`-`213`, что соответствует проекту ADR 0089: индекс производный, а не авторский. Расхождений в этой части не найдено, поэтому новой карточки нет.

## Источники

Файлы прочитаны на origin/main `ad9f3889`, проверены в `61e3dac0`: разница между этими коммитами затрагивает только `docs/research/`, сигнатуры и номера строк в коде не изменились.

- `internal/infra/github/rest.go`, `pr_create.go`, `points.go`, `tier1.go`, `tier2.go`, `graphql.go`, `cache.go`
- `internal/infra/github/rest_test.go`, `rebase_test.go`, `points_test.go`, `graphql_test.go`, `tier1_test.go`
- `internal/infra/cibridge/ci_refresh.go`, `bridge.go`
- `cmd/beacon/prcreate.go`, `cmd/beacon/registry_check.go`, `cmd/beacon/commands.go`
- `internal/app/orchestrator/orchestrator.go`, `decision.go`, `checkall.go`
- `internal/domain/verdict.go`, `types.go`
- `internal/infra/registry/index.go`, `index_check.go`
- `.github/workflows/ci.yml`
- модуль `github.com/google/go-github/v93@v93.0.0`, файл `github/pulls.go`
