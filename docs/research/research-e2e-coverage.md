# Research: покрытие e2e и живые тесты

Дата: 22 ноября 2026. Снимок: `origin/main`, коммит `b693e56d`. Метод: чтение файлов и git-истории. Тесты, сборка, gates и `batch-apply` не запускались.

## Границы

Отчёт не повторяет `docs/research/e2e-coverage-audit.md`, `docs/research/flaky-tests-audit.md`, `docs/research/flaky-tests-remaining.md` и карточки BCX-54, BCX-55, BCX-56, BCX-59, BCX-60. Из аудита исключены разбор хеша в `tests/e2e/README.md`, гонка `JSON.parse`, seeded artery, живой dedup, плагин репозитория, демон вердикта и ACP. Падение e2e с кодом 503 во внешнем API opencode здесь не карточка, а оговорка: внешний сбой, не дефект инфраструктуры репозитория.

## Таблица находок

| ID | Фаза | Утверждение | доказательство | Владелец |
| --- | --- | --- | --- | --- |
| EC-01 | P1 | Документация e2e и заголовки фикстур описывают набор до BCX-55 и BCX-59 | `docs/e2e/README.md:17,34,42,62`; `tests/e2e/README.md:26,30,37,51`; `tests/e2e/fixtures/beacon-bridge.js:1-9`; `tests/e2e/fixtures/beacon-bridge-stub.js:4-6` против `tests/e2e/e2e_test.go:13,19,61,791,800-809` | docs и fixtures |
| EC-02 | P1 | `tests/chaos/README.md` держит устранённый блокер и обещает замену фейков, которая не сделана | `tests/chaos/README.md:23,52,56`; `tools/beacon-gates/gate/config/layers.json:12`; `internal/infra/ladder/transitions.go:1`; `internal/infra/ladder/escalation.go:1`; `tests/chaos/duplicate-delivery_test.go:116,127` | `tests/chaos/README.md` |
| EC-03 | P2 | PR-фильтр джобы e2e не покрывает домен и store, которые сценарии вызывают | `.github/workflows/e2e.yml:14-22,37`; `tests/e2e/e2e_test.go:49-51,1101,1336,1442` | `.github/workflows/e2e.yml` |
| EC-04 | P2 | `resolveMcpSource` может собрать бинарник из чужого ворктри, а чистый детерминированный тест спрятан за build tag | `tests/e2e/e2e_test.go:1,96-114,101,1503-1535`; `.github/workflows/e2e.yml:8-13,39` | `tests/e2e/e2e_test.go` |
| EC-05 | P2 | Живой wake не имеет фикстуры авторизации в CI и фиксирует модель в конфиге | `tests/e2e/fixtures/opencode.json:3`; `tests/e2e/e2e_test.go:328-334,646-647,941-944`; `.github/workflows/e2e.yml:40-78` | `tests/e2e/fixtures/opencode.json` |

## P0

Новых находок P0 нет. Критичные контракты из аудита уже перенесены в влитые карточки. Новые разрывы ниже не ломают сборку, но дают ложное покрытие.

## P1

### EC-01. Документация e2e отстала от набора сценариев

`docs/e2e/README.md:42` называет плагин из `fixtures` эталоном, а репозиторийский плагин каркасом без `ctx.session.prompt`. Код в `tests/e2e/e2e_test.go:13` пишет обратное, а `runWakePositive` в `:879` грузит `.opencode/plugins/beacon-bridge.js`. Сценарий в `:804` называется `opencode_wake_with_repo_plugin`.

`docs/e2e/README.md:34` обещает ровно семь инструментов. `expectedSet` в `tests/e2e/e2e_test.go:61` берётся из `beaconmcp.AdvertisedToolNames()`. Число в docs не имеет источника.

`docs/e2e/README.md:17` и `tests/e2e/README.md:80` указывают джобу `e2e` в `.github/workflows/ci.yml`. Файл `.github/workflows/e2e.yml:1` содержит отдельный workflow с triggers в `:7-13`.

`tests/e2e/README.md:30` фиксирует шесть сценариев, `:37` имя `opencode_wake_with_reference_plugin`, `:51` внешний драйвер через `domain.Transition`. Текущий срез в `tests/e2e/e2e_test.go:800-809` содержит восемь сценариев, имя `opencode_wake_with_repo_plugin` и `cycleDaemon` в `:19-24`.

Заголовки фикстур повторяют старую версию: `tests/e2e/fixtures/beacon-bridge.js:5-8` называет репозиторийский плагин каркасом, а `tests/e2e/fixtures/beacon-bridge-stub.js:4-6` называет его заменой эталона.

Граница с BCX-52: та карточка меняет список имён и хеш. Эта карточка правит только описание набора, расположение джобы и роль плагина.

### EC-02. Устаревший блокер в README хаос-теста

`tests/chaos/README.md:52` пишет, что матрица `layers.json` не содержит слой `tests` и джоба `gates` даст `LAYER-NO-RULE: tests/chaos`. Строка `tools/beacon-gates/gate/config/layers.json:12` содержит `{"name": "tests", "rank": 0}`. Блокера нет.

`tests/chaos/README.md:23` и `:56` пишут, что лестницы T3.9 и эскалации T3.10 ещё нет в main. Файлы `internal/infra/ladder/transitions.go:1` и `internal/infra/ladder/escalation.go:1` существуют. Тест по-прежнему держит `decideResend` в `tests/chaos/duplicate-delivery_test.go:116` и `escalationLedger` в `:127`. README:23 описывает замену фейков как будущую работу, но она не выполнена. Сам факт фейков уже описан в `docs/research/e2e-coverage-audit.md:43`, поэтому карточка требует только честного текста README, без правки теста.

## P2

### EC-03. Фильтр PR не запускает e2e на изменения домена и store

`.github/workflows/e2e.yml:14-22` перечисляет пути `tests/e2e`, `tests/scenarios`, `internal/infra/mcp`, `.opencode/plugins`, `cmd/beacon`, `go.mod`, `go.sum` и сам workflow. Путей `internal/domain/**` и `internal/infra/store/**` нет.

Сценарии используют оба пакета. `tests/e2e/e2e_test.go:49-51` импортирует `domain` и `store`. `cycleDaemon.decide` в `:1101` считает вердикт через доменный механизм. `store.Open` в `:1336` открывает базу, `db.SaveWatch` в `:1442` пишет решение. Изменение в `internal/domain` или `internal/infra/store` меняет поведение e2e, но PR не запускает джобу. Проверка приходит только ночным cron в `.github/workflows/e2e.yml:8-10`.

Комментарий в `.github/workflows/e2e.yml:37` называет семь живых сценариев. Срез в `tests/e2e/e2e_test.go:800-809` содержит восемь.

### EC-04. Чужой источник сборки и тест вне обязательной джобы

`resolveMcpSource` в `tests/e2e/e2e_test.go:96-114` ищет `cmd/beacon/mcp.go` в трёх каталогах. Третий кандидат в `:101` это `filepath.Join(filepath.Dir(repoRoot), "w43")`, фиксированный соседний ворктри. Функция возвращает первый найденный каталог в `:107-109` и не сверяет его с `repoRoot`. `TestE2E` в `:1554-1557` собирает бинарник из этого источника. Если в текущем чекауте нет `mcp.go`, прогон молча тестирует чужой код. Замороженный хеш в `:791` и все файловые факты при этом остаются о текущем дереве.

`TestFullCycleDaemonFactDivergence` в `tests/e2e/e2e_test.go:1503-1535` не использует opencode, процесс, сеть или токен. Он сравнивает два решения `cycleDaemon.decide` и проверяет факт расхождения. Файл имеет build tag `//go:build e2e` в `:1`, поэтому `go test ./...` в обязательной джобе `test` этот тест не видит. Он живёт только в джобе `.github/workflows/e2e.yml:33-39` с `continue-on-error: true` и запускается по cron или вручную.

### EC-05. Нет фикстуры авторизации и проверки ответа модели

`tests/e2e/fixtures/opencode.json:3` фиксирует модель `opencode/glm-5.3-flash-free`. Файл копируется в каждый временный проект вызовом `copyFile` в `tests/e2e/e2e_test.go:318-319`.

`startServer` в `tests/e2e/e2e_test.go:328-334` копирует `auth.json` из HOME, только если он там есть. Workflow `.github/workflows/e2e.yml:40-78` не создаёт этот файл и не передаёт токен провайдера.

`waitWakeMessages` в `tests/e2e/e2e_test.go:646-647` считает ход продолженным по времени создания assistant-сообщения. Строки `:941-944` пишут `finish` в лог и явно не требуют успешного ответа модели. Так зелёный live wake возможен при ошибке провайдера или при отсутствии авторизации. Ограничение «без ответа модели» есть в `docs/research/e2e-coverage-audit.md:37`, но конфиг модели и отсутствие шага авторизации в workflow там не разобраны.

## Что проверено и не попало в бэклог

- `tests/scenarios/seeded.yaml` и `internal/testsystem/harness.go`. Карточка BCX-54 влита в `origin/main`. В `harness.go` есть `FaultController` в `:424`, `ArterySpec` в `:351` и `RunArtery` в `:525`. Четыре seed-отказа из `tests/scenarios/seeded.yaml:19-42` имеют читающий их код. Новых карточек нет.
- `tests/e2e/run.sh` и контракт инструментов. Одна команда запуска, `expectedSet` в `tests/e2e/e2e_test.go:61` берётся из MCP-пакета. BCX-52 уже ведёт эту линию.
- Список имён, хеш и вынос джобы. Покрыто BCX-52 и ATX-C02, ATX-C08.
- Ошибка 503 внешнего API opencode. Внешний инцидент, не дефект тестовой инфраструктуры.

## Проверка выводов

Все номера строк сверены с коммитом `b693e56d`. Символы приведены рядом с путями. `python3 -m json.tool` и `git cat-file` для batch-файла выполнены до публикации PR. Локальные `go test`, `go build`, `beacon gates` и `batch-apply` не запускались по правилам репозитория.
