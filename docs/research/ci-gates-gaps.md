# Разрывы в конфигурации CI и гейтов

Дата: 2026-11-21. Режим: чтение. Тесты, сборки, гейты и локальные проверки Beacon не запускались. Источники: `rg` по явным каталогам `.github/`, `tools/beacon-gates/`, `docs/gates/`, `docs/research/`, а также `git log`, `gh pr view` и `gh api`.

Состояние `origin/main` при сверке: `df88de4d`. В начале чтения было `4e9a7954`. Все выводы проверены по `df88de4d`. Ветка PR перебазирована на `e4826765`; новые коммиты не затрагивают `.github/` и `tools/beacon-gates/`, поэтому выводы остаются в силе.

## Что уже закрыто

| PR | Коммит в main | Что изменил | Остаток |
| --- | --- | --- | --- |
| #1235 | `232ca4b1` | Общий кэш Go для шагов setup-go в `ci.yml` и `tests.yml` | `docs.yml` и `e2e.yml` всё ещё отключают кэш |
| #1237 | `f33d0751` | Composite-экшен `.github/actions/go-runner-setup` для CI, registry, docs и gates-binary | `e2e.yml` появился со своим setup-go |
| #1239 | `c8ba9abe` | e2e вынесен из `ci.yml` в `e2e.yml`, лимит снижен до 5 минут | Джоба осталась с `continue-on-error` и сетевой установкой opencode |
| #1259 | `c8374f80` | Убрал `continue-on-error` у джобы docs | На main нет branch protection, поэтому проверка не обязательна для GitHub |

Ни один из четырёх PR не добавил required status checks на ветке main.

## Находки

| ID | Находка | Доказательство | Предложение | Приоритет | Карточка |
| --- | --- | --- | --- | --- | --- |
| C01 | main не защищён required status checks | `gh api repos/refusedguy/beacon/branches/main/protection` даёт HTTP 404 `Branch not protected`. PR #1239 сообщает, что protection не настроен. `internal/infra/ghci/provider.go:287` ставит `Required: true` каждому чеку из `gh pr checks`. | Включить защиту main и перечислить обязательные чеки: build, test, seed-spec, js-test, gates, cardindex, rules-broken, registry-check, docs, gates-binary. Информационные e2e и ci-provider-probe оставить вне списка. | P0 | ATX-C01 |
| C02 | `e2e.yml` дублирует setup-go и отключает кэш | `.github/workflows/e2e.yml:45-48` вызывает `actions/setup-go@v5` напрямую с `cache: false`. Composite-экшен из #1237 использует кэш по умолчанию. #1235 убрал `cache: false` в остальных workflow. | Заменить шаг на `./.github/actions/go-runner-setup`, убрать `cache: false`, оставить setup-node. Для нестабильности добавить `--max-time` к curl и кэшировать бинарник opencode. | P1 | ATX-C02 |
| C03 | `docs.yml` отключает кэш Go | `.github/workflows/docs.yml:28-30` передаёт `cache: 'false'`. Остальные джобы используют значение экшена по умолчанию или `'true'`. | Убрать переопределение кэша. | P2 | ATX-C03 |
| C04 | reachability-гейт зарегистрирован, но не запускается в CI | `tools/beacon-gates/main.go:24` регистрирует `reachability`. `rg -n 'reachability' .github/` даёт 0 строк. Джоба gates в `ci.yml:188-196` прогоняет capabilities, layers-ast и layers-list. В `reachability.json` 23 permanent-строки без проверки актуальности. | Добавить self-test и прогон reachability в джобу gates с теми же флагами. Обновить README. | P1 | ATX-C04 |
| C05 | wiring-гейт ищет поле, которое не пишет инструмент | `tools/beacon-gates/gate/config/baseline.json:10` задаёт `owner_field: "OwnerFiles"`. `internal/infra/mcp/tool_defer_wiring.go:153` пишет только `Владелец файлов:`. Две карточки из 13 без OwnerFiles: WIRING-1106 и WIRING-919. Фикстуры в `wiring.go:181-194` и `wiring_test.go` содержат только OwnerFiles. | Проверять оба имени поля или перейти на русское имя и обновить фикстуры. Добавить тест на карточку в формате инструмента. | P1 | ATX-C05 |
| C06 | wiring-гейт информационный при реальных нарушениях | `.github/workflows/ci.yml:201-207` ставит `continue-on-error: true`. 12 из 13 карточек WIRING имеют статус не `todo`, две без раздела `## Причина`, две без OwnerFiles. Провал шага прячется в зелёный статус джобы. | После C05 очистить или архивировать устаревшие карточки и убрать `continue-on-error`. Для legacy-карточек добавить явные исключения. | P1 | ATX-C06 |
| C07 | exception-таблицы расходятся по лимитам и политике | `baseline.json`: `baseline_limit: 0` и пустой `baseline`. `docs-baseline.json`: `baseline_limit: 40` и пустой `baseline` после #1264. `permanent.json`: 19 строк без лимита. `exceptions.go:121` проверяет лимит только для baseline. | Привести лимиты к числу строк, добавить self-test на согласованность таблиц и зафиксировать политику в `docs/gates/`. | P2 | ATX-C07 |
| C08 | документация workflow и комментарий в ci.yml устарели | `.github/workflows/README.md:5` описывает `actions/cache` для build, test и gates. README:20 сообщает о пяти джобах и e2e внутри ci.yml. README:43 указывает лимит e2e 20 минут, а `e2e.yml:38` содержит 5. `.github/workflows/ci.yml:198-200` называет docs-гейт информационным после #1259. | Обновить README и комментарий, добавить список обязательных чеков из C01. | P3 | ATX-C08 |

## Детали

### C01. Обязательность проверок

Проверка branch protection через API вернула 404. Значит, ни одна джоба, включая `#1259`, не блокирует merge на уровне GitHub. Beacon.autoMerge смотрит на свой вердикт, но ручной merge обходит этот контур. Плюс `internal/infra/ghci/provider.go:287` выставляет `Required: true` для всех чеков из `gh pr checks`, хотя `internal/domain/verdict.go:225` и `:239` умеют пропускать чеки с `Required: false`. Разницы между обязательной и информационной проверкой в провайдере нет.

### C02 и C03. Подготовка Go

Композитный экшен `.github/actions/go-runner-setup` содержит checkout и `actions/setup-go@v5` с `go-version-file: go.mod`. Параметр `cache` по умолчанию равен `true`. Его используют `ci.yml`, `docs.yml`, `gates-binary.yml` и `tests.yml`. `e2e.yml:45` обходит экшен. Там же `cache: false`, то есть каждый прогон заново качает toolchain и модули. По замеру в `docs/research/ci-speed-audit.md:53` шаги Set up Go и Set up Node занимают 7 с и 5 с.

### C04. Неподключённый гейт

В бинарнике `beacon-gates` зарегистрировано пять команд: capabilities, layers-ast, layers-list, reachability, wiring. В CI вызываются три из них плюс wiring информационно и rules через `./beacon gates rules`. Reachability не вызывается ни в одном workflow. Таблица `reachability.json` содержит 23 строки, из них 20 с правилом REACH-UNWIRED и ссылкой на Wave 6 или 7. Без прогона гейта эти строки могут стать устаревшими незаметно.

### C05 и C06. Wiring-гейт

Инструмент `beacon_defer_wiring` формирует карточку с полем `Владелец файлов:` и не добавляет `OwnerFiles:`. Шаблон `docs/templates/wiring-card.md` добавляет оба поля, но карточки из инструмента содержат только русское. Гейт читает `OwnerFiles` из конфига. Две карточки без этого поля получают finding WIRING-OWNERFILES. Две карточки без `## Причина` получают WIRING-REASON. Двенадцать карточек со статусом не `todo` получают WIRING-STATUS. Шаг в `ci.yml` помечен `continue-on-error: true`, поэтому все эти находки не влияют на зелёный статус джобы. Self-test построен на фикстурах с `OwnerFiles:` и не ловит расхождение.

### C07. Таблицы исключений

`exceptions.go` загружает `baseline.json` и `permanent.json` через `go:embed`. Правило `CAP-BASELINE-EXCEEDED` срабатывает только когда число строк baseline превышает `baseline_limit`. Сейчас limit равен нулю и строк нет, то есть механизм сокращения исключений не работает. В `docs-baseline.json` limit равен 40 при нуле строк после #1264. Permanent-таблица содержит 19 строк и не имеет лимита. У таблиц разные обязательные поля: `task` у baseline и `reason` у permanent. Единой проверки согласованности лимитов и отсутствия пересечений нет.

### C08. Расхождение документации

README в каталоге workflow описывает состояние до #1235, #1237 и #1239. Он называет пять джоб ci.yml и 20-минутный лимит e2e. В ci.yml сейчас восемь джоб, а e2e живёт в отдельном workflow с лимитом 5 минут. Комментарий про wiring-гейт в ci.yml ссылается на docs-гейт как на информационный, хотя #1259 убрал у него `continue-on-error`.

## Что дали docs/gates

Файлы G1, G2, G3, G4 и rollback-drill описывают раскатку Beacon, а не проверки CI. G1 по состоянию на 2026-11-19 помечен как заблокированный: 0 PR в прогоне, SLO не измерены, значения в таблицах пустые. G4 требует решения владельца и выборки процессов под нагрузкой. Ни один из этих документов не задаёт required status checks и не связан с конфигом гейтов, поэтому C01 нельзя вывести из них.

## Ограничения проверки

Гейты, тесты и сборки не запускались по правилам репозитория. Актуальность строк permanent и reachability не проверялась прогоном гейта, проверялось только существование путей и чтение конфигов. Выводы о wiring-гейте основаны на чтении кода, шаблона, инструмента и карточек, а не на запуске бинарника. Branch protection проверен только через GitHub API в момент сверки.
