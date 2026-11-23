# Аудит e2e и smoke-покрытия beacon

Дата: 21 ноября 2026. Репозиторий: `beacon-impl`, основная рабочая копия на коммите `d3b1bfb1`. Дополнительно прочитана свежая ветка `fix/idx05` в каталоге `ak2-recheck-9039bc0` (коммит `0ddacf6e`), где уже легли PR #1112 и #1145.

Метод: чтение файлов, git-истории и логов CI. Тесты не запускались, код не менялся. Факты о падениях взяты из логов `/tmp/opencode/e2e-1112.log` и сообщений сессий в локальной базе OpenCode. Номер сессии и порядковый номер сообщения приведены рядом с каждым таким фактом.

Локальная `main` отстаёт от `origin/main` (`2a3362f1`). Для workflows сверены обе версии. На `origin/main` есть джобы `seed-spec`, `cardindex`, `rules-broken` и workflow `tests.yml` с джобой `registry-check`. Вызов `evals/run.sh` отсутствует и там, и там. Джоба `e2e` остаётся с `continue-on-error: true`.

## Главное

1. Единственный живой прогон, который что-то доказывает, это `tests/e2e`. Он грузит эталонный плагин из `fixtures`, а не плагин репозитория. Джоба `e2e` имеет `continue-on-error: true`, поэтому красный прогон не блокирует merge.
2. `evals/run.sh` в CI не вызывается вообще. В `.github/workflows/` нет ни одной джобы с eval. Проверка `10_two_phase_local_ci_gate` только читает файлы и сравнивает хеши.
3. Договор на инструменты MCP продублирован в трёх местах и трижды расходился: e2e `expectedSet`, `beaconmcp.ToolNames` и `evals/main.go`. Последний расход 9 против 10 tools сломал CI 21 ноября. В ветке `fix/idx05` evals по-прежнему ждёт 8 инструментов.
4. ADR 0061 описал пять новых e2e-сценариев. В набор попал один, `live_opencode_full_mcp_cycle`. Четыре остались на бумаге: гейт ShipFacts, честная доставка без дублей, продолжение той же Watch после ответа человека и восстановление после краша.
5. Из восьми фич, которые интересовали заказчика, живым зондом подтверждена только доставка wake. Dedup, ACP, registry, human-decide, board TUI, GC и shutdown проверяются моделями, unit-тестами или описаниями сценариев в `docs/evals/`.

## Что на самом деле исполняется

| Слой | Команда | Что делает | В CI |
| --- | --- | --- | --- |
| `tests/e2e` | `bash tests/e2e/run.sh`, build tag `e2e` | Поднимает `opencode serve` 2.3.0, эталонный плагин, реальные обработчики MCP через go-sdk `CommandTransport` | Да, джоба `e2e`, `continue-on-error: true` (`.github/workflows/ci.yml:198-241`) |
| `tests/chaos` | `go test ./...` | Реальный SQLite и файл квитанций, лестница и эскалация заменены фейками (`duplicate-delivery_test.go:30-33`, `:122-124`) | Да, джоба `test`, блокирующая (`:60-101`) |
| `tests/adapter` | `node --test` | Реальный JS-плагин с фейковым хостом, `session.prompt` и event bus внутри процесса теста | Да, джоба `js-test`; в политике Beacon помечена `required: false` (`:103-137`) |
| `evals` | `bash evals/run.sh` | 42 проверки, из них часть на реальном store, часть на исполняемых моделях W14-W18, B01-B04, OW01-OW03, D01-D04, EC01-02, N01-04 | Нет ни одной джобы |
| `internal/...`, `cmd/...` | `go test ./...` | 213 файлов unit-тестов, включая ACP-хардшейк внешнего бинарника | Да, джоба `test` |

## Покрытие e2e сценариями

В наборе семь записей (`tests/e2e/e2e_test.go:770-778`), хеш набора `85303c45...` (`:761`) совпадает со списком сценариев в коде. Версия в `tests/e2e/README.md:26` равна `10243acb...` и отстала на один сценарий. Расхождение уже описано в `docs/evals/t3-227-t3-228-coverage.md`, гейт eval 10 теперь считает хеш по коду, а не по README.

| Сценарий | Что подтверждает | Ограничение |
| --- | --- | --- |
| `repo_plugin_skeleton` | Наличие строк `session.prompt` и `events.jsonl` в плагине репозитория | Нет проверки, только лог (`:845`) |
| `mcp_go_sdk_CommandTransport` | `initialize`, `tools/list`, один `tools/call` на настоящем бинарнике | Обработчики в точке входа заглушки, жёсткое число инструментов |
| `mcp_inspector_optional` | Handshake через MCP Inspector | По умолчанию SKIP, в среде npx не даёт вывода |
| `opencode_wake_with_reference_plugin` | `prompt.ok delivery=steer`, `session.execution.started`, квитанция | Плагин скопирован из спайка T0.3, а не из репозитория |
| `live_wake_via_opencode_api` | `POST /api/session/{id}/prompt` принимает steer и агент продолжает ход; невалидный sessionID отклонён | Один вызов, без проверки повтора и без ответа модели |
| `live_opencode_full_mcp_cycle` | Watch, registration в store, wait, get, ack, отчёт по CycleInstructions | Вердикт пишет внешний драйвер через `domain.Transition` (`:976-1014`), демона нет |
| `negative_control_stub_handler_must_fail` | Заглушка обязана провалить ту же проверку | Негативный контроль есть только для плагина и пустого `repo` |

## Покрытие chaos и evals

`tests/chaos` проверяет три сценария на реальном store: двойная доставка даёт один эффект, indeterminate не ретраится и эскалируется один раз, рестарт сохраняет обязательства за 5 секунд. Лестница и эскалация в тесте это `decideResend` и `escalationLedger`, то есть фейки. Живого wake после рестарта нет.

`evals/main.go` содержит 42 именованные проверки. Реальный путь идут только первые восемь проверок, а также 12, 14 и 15 через `NewServerWithDeps` и SQLite. Проверки 12-14 и 16-20 запускают плагин через Node-харнесс с фейковым хостом. W14-W18, B01-B04, OW01-OW03, D01-D04, EC01-02, N01-04 считаются на исполняемых моделях внутри раннера. Набор `W01-W13` включается только при `BEACON_WAKE_EVAL=1` и требует песочницы Pi и Cursor с токенами. Это следует из `evals/README.md:262-304` и `evals/main.go:655-666`.

Больше двухсот файлов в `docs/evals/` это спецификации вида «сценарий, проверки, негативный контроль». Код за ними в основном не стоит. Примеры: `w06-acp-wake-sequence.md`, `w07-acp-role-matrix.md`, `w06-deliveryid-dedup.md`, `w21-human-question-revision.md`, `e05-wait-pending-question.md`, `u01-render-15-watches.md`, `e10-tui-no-db-write.md`, `w25-seed-regression-artery.md`.

## Живой зонд по перечисленным фичам

| Фича | Живой зонд | Чем подтверждено | Что осталось |
| --- | --- | --- | --- |
| Доставка wake | Да, но на эталоне | `opencode_wake_with_reference_plugin` и `live_wake_via_opencode_api`, плюс ручной proof в `evals/README.md:205-229` | Плагин `.opencode/plugins/beacon-bridge.js` в живом сервере не грузится. Спецификация `docs/evals/e01-e2e-repo-plugin-not-fixture.md` не реализована |
| Dedup | Нет | Chaos на реальном store, eval `18_w09_receipt_outcomes` на фейковом хосте, `rs-wake-dedup-across-restart.md` как текст | Нет живого повтора указателя в сессию и проверки, что второй steer не ушёл после рестарта |
| ACP | Нет | `docs/evals/acp-smoke.md` и `internal/infra/wake/acp_smoke_test.go` спавнят `opencode acp` и делают `initialize`, `session/new`, `session/load` | `session/prompt` и wake через ACP не проверяются. Сценарии W06 и W07 описаны только в документах |
| Registry | Нет | `internal/infra/registry/*_test.go`, модельные eval `OW02`, `OW03`, джоба `registry-check` на `origin/main` проверяет конфиг позитивом и негативом | Нет живого теста с двумя проектами в одном сервере и реальными сессиями |
| Human-decide | Нет | `cmd/beacon/human_test.go`, спецификации `w21-human-question-revision.md` и `e05-wait-pending-question.md` | Сценарий `needs_input_continue_of_same_watch` из ADR 0061 п4 не написан |
| Board TUI | Нет | `internal/tui/*_test.go` с фейковым сокетом, спецификации `u01-u04` и `e10-tui-no-db-write.md` | Нет прогона TUI против живого сервера, нет измеренных первого кадра и RSS |
| GC | Нет | Unit-тесты `internal/infra/mcp/tool_list_running_test.go`, `watch_bindings_test.go`, коммит `3f575e01` про alive-PID | Сценарий `crash_recovery_durable_store_no_old_pid` из ADR 0061 п5 не написан |
| Shutdown | Нет | `internal/infra/server/lifecycle_test.go`, `cmd/beacon/registry_stop_test.go` | Нет живого kill сервиса на середине `wait_result` с проверкой, что старый PID и lease не читаются |

## Три падения из нашей сессии

### 1. Договор на инструменты: 9 против 10

Факт из `/tmp/opencode/e2e-1112.log`:

- `:265`: `tools/list вернул 10 инструментов: [beacon_ack beacon_cancel_watch beacon_defer_wiring beacon_get_result beacon_list_running beacon_stats beacon_task_claim beacon_wait_result beacon_wake beacon_watch_pr]`.
- `:287`: в `live_opencode_full_mcp_cycle` ожидалось девять имён без `beacon_wake`.
- `:294-304`: `TestE2E/mcp_go_sdk_CommandTransport` и `TestE2E/live_opencode_full_mcp_cycle` упали.

Обсуждение в сессии `ses_e01cd0538ffeJRp9cfIPfWxtvf`, сообщения 441, 451, 563. Причина: PR зарегистрировал `beacon_wake` через `AttachWake`, а два списка ожидания остались на девяти именах. Починил коммит `f5c77e25` в ветке `fix/idx05`: добавил `beaconmcp.WakeToolName` в `expectedSet` и заменил `len(names) != 9` на `len(names) != len(expectedSet)`.

Почему это повторится. В `beacon-impl/evals/main.go:43-67` всё ещё `contractToolCount = 7` и список из семи имён. В `ak2-recheck-9039bc0/evals/main.go:63-72` список из восьми имён, без `task_claim` и `wake`. evals в CI не запускается, поэтому расхождение видно только при ручном прогоне.

Как зафиксировать:

- Экспортировать из `internal/infra/mcp` один набор зарегистрированных имён, включая `beacon_wake` и `beacon_task_claim`, и использовать его в обоих е2е-вызовах и в `evals/main.go`. Удалить числовые константы 7, 8, 9 и жёстко заданные списки.
- Добавить unit-тест в `internal/infra/mcp`, который сверяет набор инструментов сервера с экспортированным списком. Тогда новая регистрация без правки контракта падает в обычной джобе `test`, а не в необязательной `e2e`.
- Сделать `evals/run.sh` отдельной джобой в `.github/workflows/ci.yml`. Сейчас проверка `10_two_phase_local_ci_gate` читает файлы и не ловит расхождение кода с ожиданием.
- Править `frozenScenarioHash` и копию в `tests/e2e/README.md` одним коммитом с изменением набора. Расхождение хешей уже стоило красного eval 10, это зафиксировано в `docs/evals/t3-227-t3-228-coverage.md`.

### 2. Гонка записи квитанции в `beacon-bridge.js`

Факт из сессии `ses_e02babb9dffeN1QFcKyX5Mqs6E`, сообщение 768:

```
test at .opencode/plugins/beacon-bridge.js:1335:36
✖ T3.16: плагин читает указатель по событию review (28.34ms)
SyntaxError: Unexpected end of JSON input
  at JSON.parse (beacon-bridge.js:959:25)
```

Сессия `ses_e037c25eeffea3FSw3fneULDr5`, сообщение 1026, объясняет причину: тест посылает событие и раньше ждал фиксированные 30 мс. Добавленный `await claim(...)` увеличил задержку, и чтение попало в середину записи. Аудит `docs/research/flaky-tests-audit.md:99-116` подтверждает код: цикл ждёт только `existsSync`, затем вызывает `stop()` и `JSON.parse`. Наличие файла не означает, что строка дописана.

Как зафиксировать:

- Писать квитанцию одной операцией во временный файл с последующим `rename`, либо одной `writeFileSync` полной строки с завершающим переводом строки. Тогда `existsSync` уже будет значить, что строка готова.
- Перенести `JSON.parse` в цикл ожидания. Успешный разбор и есть признак готовности. Вызывать `stop()` после разбора, а не до него. Патч-идея лежит в `docs/research/flaky-tests-audit.md:160-179`.
- Добавить детерминированный тест с внедрённым планировщиком и 20 повторами, чтобы гонка не зависела от загрузки CI.
- Добиться, чтобы проверка блокировала merge: сейчас `js-test` помечен `required: false`, поэтому тот же инцидент не мешает мержу.

### 3. Seeded regression artery

Спецификация `docs/evals/w25-seed-regression-artery.md` требует трёх прогонов: healthy, seeded, healthy_again. ADR 0214 говорит, что покрытие одними тестами impl не считается. Ветка `fix/idx05` добавила `tests/scenarios/seeded.yaml` и `internal/testsystem` коммитом `63cffc79` (PR #1145). Но CI-шаг `seed-spec` в логе `/tmp/opencode/ak2-run2.log:194-208` только проверяет `grep` по четырём seed-именам и трём именам прогонов, затем запускает `TestGoldenFingerprint`, `TestSeedRoundTrip`, `TestSeedDrivesTrace`. Настоящего прогона healthy, seeded, healthy_again с живым fault controller нет.

Как зафиксировать:

- Реализовать fault controller, который читает `tests/scenarios/seeded.yaml` и внедряет четыре отказа из `docs/tasks/S7.011.md`: lost baton, late event, pruned cursor, duplicate clientRequestId.
- Запускать три прогона в одной джобе и требовать: healthy pass, seeded fail на ожидаемом маркере, healthy_again pass. Результат сравнивать с наблюдаемым фактом, не с внутренним счётчиком теста.
- Сохранить негативный контроль из спецификации: без seed сценарий не должен давать диагностического значения.
- Для artery-правок требовать пару сценариев по ADR 0214 и не принимать замену `grep`-проверкой спецификации.

## Итог: топ-10

| Сценарий | Покрыт | Чем | Пробел | Приоритет |
| --- | --- | --- | --- | --- |
| Контракт `tools/list` MCP | Частично | `mcp_go_sdk_CommandTransport`, `live_opencode_full_mcp_cycle`, 42 eval | Три отдельных списка имён, падение 9 против 10 в CI, evals не в CI | P0 |
| Запись квитанции и гонка `JSON.parse` | Частично | `tests/adapter/receipts-wx.test.js`, аудит флакности | `existsSync` перед дописыванием строки, `stop()` до чтения, проверка `required: false` | P0 |
| Seeded regression artery | Нет | `seeded.yaml`, grep-проверка спецификации, модельные тесты `internal/testsystem` | Нет живого прогона healthy, seeded, healthy_again и fault controller | P0 |
| Доставка wake через плагин репозитория | Нет | Эталон из `fixtures`, ручной proof через HTTP API | Реальный `.opencode/plugins/beacon-bridge.js` не грузится в живом e2e, сценарий из `e01-e2e-repo-plugin-not-fixture.md` не написан | P0 |
| Dedup и идемпотентность доставки | Частично | Chaos на реальном store, eval `18_w09_receipt_outcomes`, `rs-wake-dedup-across-restart.md` | Нет живого повтора указателя и проверки, что после рестарта второй steer не ушёл | P1 |
| Human-decide и продолжение той же Watch | Нет | `cmd/beacon/human_test.go`, спецификации W21 и E05 | Сценарий `needs_input_continue_of_same_watch` из ADR 0061 п4 отсутствует | P1 |
| Registry и граница проекта | Нет | Unit-тесты registry, модельные eval `OW02` и `OW03`, джоба `registry-check` | Нет живого теста с двумя проектами и реальными сессиями | P1 |
| Вердикт полного цикла | Частично | `live_opencode_full_mcp_cycle` | Вердикт записывает внешний драйвер, демона нет, гейт ShipFacts из ADR 0061 п2 не написан | P1 |
| ACP wake sequence | Нет | `acp_smoke_test.go` только handshake, спецификации W06 и W07 | `session/prompt`, `session/resume`, роль клиента против агента не проверяются живым адаптером | P2 |
| Board TUI, GC и shutdown | Нет | Unit-тесты TUI, lifecycle и watch GC, спецификации U01-U04 и E10 | Нет прогона TUI против сервера, kill на середине `wait_result` и проверки старого PID | P2 |

## Источники

- `tests/e2e/e2e_test.go`, `tests/e2e/README.md`, `tests/e2e/run.sh`, `tests/e2e/fixtures/beacon-bridge.js`
- `tests/chaos/duplicate-delivery_test.go`, `tests/chaos/README.md`
- `evals/main.go`, `evals/README.md`, `evals/run.sh`, `docs/evals/*.md`
- `.github/workflows/ci.yml`, `.github/workflows/README.md`
- `docs/e2e/README.md`, `docs/adrs/0061-local-e2e-with-live-opencode.md`, `docs/adrs/0214-seeded-regression.md`
- `docs/tasks/T3.342.md`, `docs/tasks/S7.011.md`, `docs/tasks/RS-WK-02.md`
- `docs/research/flaky-tests-audit.md`, `docs/evals/t3-227-t3-228-coverage.md`
- `/tmp/opencode/e2e-1112.log`, `/tmp/opencode/ak2-run2.log`
- Сессии `ses_e01cd0538ffeJRp9cfIPfWxtvf`, `ses_e02babb9dffeN1QFcKyX5Mqs6E`, `ses_e037c25eeffea3FSw3fneULDr5`, `ses_e07974f3afferWqmGMxXPudXrj`
