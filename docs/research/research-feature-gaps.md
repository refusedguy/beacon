# Разрывы между обещаниями операторских сценариев и кодом

Дата: 2026-11-22. База: `origin/main` `9436385e`. Режим: чтение исходников, документации и eval-спецификаций. Код, тесты, сборки, гейты и команды Beacon не запускались. Затронуты каталоги `cmd/beacon/`, `internal/infra/mcp/`, `internal/infra/httpapi/`, `internal/infra/store/`, `internal/tui/`, `internal/app/orchestrator/`, `docs/`, `README.md`, `AGENTS.md` и история коммитов PR 1460, 1582, 1608, 1612, 1607.

Владелец говорит, что «не хватает фич». Ниже не список новых идей, а места, где сценарий уже описан в документации, но доведён до конца не был. Каждая находка содержит ссылку на код и на обещание в документации.

## 1. Сводка

| ID | Разрыв | Код | Обещание | Приоритет |
| --- | --- | --- | --- | --- |
| FT-01 | `graphReady` всегда `no-reader` в production | `cmd/beacon/mcp.go:748-790`, `internal/infra/mcp/server.go:733,763-765`, `cmd/beacon/inspect.go:201-202` | `README.md:64,130`, `AGENTS.md:60`, PR 1608, PR 1612 | P0 |
| FT-02 | CRUD расписаний из PR 1460 не подключен к CLI | `cmd/beacon/task.go:902-903`, `internal/infra/store/schedules.go` | `docs/tasks/INTG-03.md:12`, PR 1460 | P1 |
| FT-03 | Снимок SharedGate из PR 1582 не имеет потребителя | `internal/infra/store/observability.go:786` | PR 1582 | P1 |
| FT-04 | Нет wiring-карточек и записей в `beacon deferred` для PR 1460, 1582, 1608, 1612 | `docs/tasks/WIRING-*.md`, `cmd/beacon/deferred.go:123-155` | `AGENTS.md:117,127`, `docs/templates/orchestrator-task.md:136,191` | P1 |
| FT-05 | `beacon deferred done` описан в шаблоне, но отсутствует в CLI | `cmd/beacon/deferred.go:126-155`, `internal/infra/store/deferred.go:318` | `docs/templates/wiring-card.md:15,17` | P1 |
| FT-06 | Две из трёх точек входа в живой экран не существуют | `cmd/beacon/main.go:255-287`, `cmd/beacon/up.go:171-177` | `docs/evals/tui-live-single-screen.md:11-16`, `docs/research/research-dir3-tui.md:34,44` | P1 |
| FT-07 | HTTP-путь `beacon human` будит, но не возобновляет Watch; CLI не знает `context` и `force_retry_merge` | `cmd/beacon/mcp.go:1000-1043`, `cmd/beacon/human.go:35-50`, `internal/infra/httpapi/server.go:1354-1358` | `README.md:333`, `docs/evals/w21-human-question-revision.md:5,24-35`, `docs/evals/tui-live-single-screen.md:24-32` | P1 |
| FT-08 | `beacon doctor` не проверяет конфиг OTEL | `cmd/beacon/doctor.go:106` | `docs/evals/e11-metrics-counters.md:20,27` | P2 |
| FT-09 | `beacon orchestrate --format json` отдаёт ребейзы как голые номера PR | `internal/app/orchestrator/orchestrator.go:310-320`, `cmd/beacon/orchestrate.go:282-302` | `AGENTS.md:78,86`, `README.md:214` | P2 |

## 2. Находки по пунктам

### 2.1. Живой борд и `graphReady`

`beacon board --live` в коде есть. Адаптер в `cmd/beacon/board_wire.go:19-27` ставит `runTuiBoard`, обёртка в `cmd/beacon/board.go:24-33` перехватывает `--live` и вызывает `runLiveBoard` (`cmd/beacon/board.go:76-119`). Граф зависимостей строится из поля `Dependencies` карточек в `internal/tui/board.go:78-95`, фильтры по статусу и владельцу есть в `cmd/beacon/board.go:79-80`. Эти части обещания `README.md:134-136` выполнены.

Разрыв в другом месте. Маркер `graphReady` обещан как доказательство свежести графа в `README.md:64`, `README.md:130` и `AGENTS.md:60`. PR 1608 добавил порты `GraphIndexReader`, `RegistryStatsReader` и `GenerationLedger` в `mcp.Dependencies` (`internal/infra/mcp/server.go:481-487`) и их установку в обработчиках (`internal/infra/mcp/server.go:733-734,763-765`). PR 1612 добавил поле в ответ `beacon_task_claim` (`internal/infra/mcp/tool_task_claim.go:153-163`). Оба коммита прямо отложили правку `cmd/beacon/mcp.go` на wiring-карточку.

На базе `9436385e` композитный корень `buildMCPDependencies` (`cmd/beacon/mcp.go:748-790`) задаёт `DeferWiring`, `TaskClaimStore`, `TaskCloseStore`, но не задаёт ни один из трёх портов ADR 0314. Следствия:

- `internal/infra/mcp/server.go:763-765` не вызывает `SetGraphIndexReader` для claim, поэтому `out.GraphReady` остаётся `false`.
- `internal/infra/mcp/server.go:733-734` не вызывает сеттер для stats. В `internal/infra/mcp/tool_stats.go:573` источник `GraphReadyNoReader` равен `no-reader`, и маркер в `loadErrors` не пишется.
- `cmd/beacon/inspect.go:201-202` читает сигнал из сохранённого stats-ответа. Без маркера `graphReady.source` равен `no-reader`. Это и есть заявленный в задании разрыв.

Живой экран не показывает готовность графа вообще: в `internal/tui/` нет ни одного упоминания `graphReady`. Пользователь видит рёбра, но не видит, свежий ли индекс.

Вторая часть пункта про TUI. Eval `docs/evals/tui-live-single-screen.md:11-16` и исследование `docs/research/research-dir3-tui.md:34,44` обещают три точки входа: `beacon tui`, `beacon up --tui` и `beacon board --live`. Реализована только третья. В `cmd/beacon/main.go:255-287` нет ключа `tui`. В `cmd/beacon/up.go:171-177` объявлены `--interval`, `--once` и `--channels`, флага `--tui` нет. Обещание из TU3-01 не закрыто. Список из девяти глаголов и одноклавишные действия уже влиты: `internal/tui/app.go:109-119` и `internal/infra/httpapi/server.go:1354-1358` совпадают.

### 2.2. Отложенная проводка 1460, 1582, 1608

Три PR оставили проводку вне своих OwnerFiles и не оформили её записью в `beacon deferred`.

PR 1460 (INTG-03) добавил `CreateSchedule`, `GetSchedule`, `ListSchedules`, `UpdateSchedule`, `DeleteSchedule` и `SetScheduleEnabled` в `internal/infra/store/schedules.go`. Сообщение коммита говорит, что адаптер и переключатель `scheduleStoreFor` нуждаются в отдельной wiring-карточке. Карточка `docs/tasks/INTG-03.md:12` повторяет это. На базе `9436385e` `cmd/beacon/task.go:902-903` по-прежнему вызывает `server.NewFileScheduleStore(...schedules.json)`. Новые методы store не вызываются ни одной production-точкой.

PR 1582 (OBS-04) добавил `SharedCIWaitSnapshot()` в `internal/infra/store/observability.go:786`. Сообщение коммита говорит, что подключение снимка к `beacon_stats` требует файлов вне карточки. Поиск вызовов `SharedCIWaitSnapshot(` на базе даёт одно вхождение: само объявление. Потребителя нет.

PR 1608 (MP-01) добавил порты в `mcp.Dependencies`, но оставил `cmd/beacon/mcp.go` нетронутым из-за открытых PR. PR 1612 повторил то же для `graphReady`. Это одна и та же дыра владения, описанная в FT-01.

Wiring-карточек для четырёх PR нет. В `docs/tasks/` лежат `WIRING-485`…`WIRING-493`, `WIRING-887`, `WIRING-915`, `WIRING-919`, `WIRING-926`, `WIRING-931`, `WIRING-1106`. Карточка `FT-04` предлагает завести недостающие карточки и записать их через `beacon_defer_wiring`, чтобы оркестратор увидел их первыми по правилу `AGENTS.md:127`.

`beacon deferred` как команда подключена: `cmd/beacon/deferred.go:123-155` читает durable store через `mcp.DeferWriter` и печатает table или json. Разрыв в другом: шаблон `docs/templates/wiring-card.md:15,17` и карточки `WIRING-485`, `WIRING-486`, `WIRING-487`, `WIRING-490` требуют вызвать `beacon deferred done` и видеть `status: done`. Такой подкоманды нет. `runDeferred` (`cmd/beacon/deferred.go:126-155`) принимает только `--format` и игнорирует позиционные аргументы. Метод `MarkDeferredDone` существует в `internal/infra/store/deferred.go:318` и вызывается автоматически из `cmd/beacon/mcp.go:70` при влитом PR, но из CLI недоступен. Это FT-05.

### 2.3. Проверки doctor

`cmd/beacon/doctor.go:106` выполняет ровно три проверки: версию Go, наличие `beacon-gates` и каталог состояния. Это совпадает с обещанием `docs/tasks/T0.6-beacon-cli-binary.md:20`. Нарушения здесь нет.

Есть другое обещание. Eval `docs/evals/e11-metrics-counters.md:20` требует, чтобы `beacon doctor` печатал WARN о неверном `BEACON_OTEL_EXPORTER`. Отрицательный контроль в строке 27 помечает пропуск этой проверки отдельно. В `cmd/beacon/doctor.go` нет ни одной строки с `OTEL` или `EXPORTER`. Карточки OBS-07 в репозитории нет, поэтому обещание из eval осталось без реализации. FT-08 фиксирует это как измеримую проверку.

### 2.4. Потребители вывода

`beacon why` (`cmd/beacon/task.go:252-293`) печатает цепочку блокировок текстом. Флага `--format json` у команды нет, MCP-инструмента для неё нет, в `docs/templates/orchestrator-task.md:136,191` и в `AGENTS.md:127` она не упоминается среди источников задач. Обещание ADR 0045 пункт 21 о цепочке с держателями шагов выполнено для человека. Автоматического потребителя не найдено. Отдельной карточки нет: в документации нет требования, чтобы этот вывод читал агент.

`beacon workload` (`cmd/beacon/workload.go:123-154`) печатает table или json. Функция `orchestrator.RunWorkload` имеет потребителя: демон вызывает её в `cmd/beacon/daemon.go:737`, чтобы подписать оркестратора на активные Watch. Сам отчёт CLI потребителя не имеет. ADR 0156 пункт 21 обещал флаги `--project` и `--all` и счётчик `uncovered`; в `cmd/beacon/workload.go:124-126` есть только `--format`, а в `internal/app/orchestrator/workload.go:65-75` поля `uncovered` нет. Карточка `docs/tasks/T3.384.md` объявила это требование снятым и закрыта как дубль. По правилу `AGENTS.md:129` dropped-карточки в работу не берут, поэтому новой карточки нет.

`beacon orchestrate` (`cmd/beacon/orchestrate.go:244-280`) печатает отчёт и возвращает `orchestrator.Result`. Демон в `cmd/beacon/daemon.go:741` выбрасывает результат, потому что wake отправляются внутри `orchestrator.Run`. Поле `NeedsRebase` в `internal/app/orchestrator/orchestrator.go:318` это `[]int` с номерами PR. Без watchID и revision потребитель не может вызвать `beacon_get_result` и получить `nextAction`, который `AGENTS.md:86` считает обязательным источником команды rebase. Таблица в `cmd/beacon/orchestrate.go:299-301` печатает `REBASE pr=<n>`. FT-09 предлагает сделать элементы отчёта объектами с watchID и revision.

### 2.5. Human-команды и wake

Путей два, и они ведут себя по-разному.

Локальный путь без сервера. `sqliteHumanStore.Apply` (`cmd/beacon/human.go:144-182`) пишет событие в журнал, затем для `approve`, `reject`, `rework`, `resume` вызывает `applyAckDecision` (`cmd/beacon/human.go:187-214`). Там `AckHandler.Decide` или `Ack` меняет состояние Watch в той же ревизии. Тест `cmd/beacon/human_test.go:278-348` проверяет, что `needs_input` переходит в `watching`, ревизия растёт на один, обязательство получает `claimedAt`. Wake в этом пути нет: `AckHandler` не имеет порта доставки, а сервера в этом сценарии нет.

HTTP-путь при наличии `connection.json`. `humanDecisionSink.Apply` (`cmd/beacon/mcp.go:982-1051`) переключает состояние только для `cancel`, `unblock`, `pause` и `resume`. Для `approve`, `reject`, `rework`, `context` и `force_retry_merge` ветки нет. Watch остаётся в `needs_input`. Затем для всех команд кроме `cancel` вызывается `s.wake` (`cmd/beacon/mcp.go:1056-1089`), который шлёт `domain.WakePayload{Verdict: "human:" + command}`. Получатель получает wake, но состояние, которое тот wake должен был разблокировать, не изменилось. Это противоречит eval `docs/evals/w21-human-question-revision.md:24,30,34-35`, где ответ возвращается в ту же Watch через ack с `continue_of` или decision.

Второй разрыв в наборе глаголов. Сервер принимает девять команд (`internal/infra/httpapi/server.go:1354-1358`), живой экран предлагает девять клавиш (`internal/tui/app.go:109-119`), а `cmd/beacon/human.go:35-50` разрешает только семь: `approve`, `reject`, `rework`, `unblock`, `pause`, `resume`, `cancel`. Команды `context` и `force_retry_merge` CLI отвергает с кодом 2. Обещание `README.md:333` про ответ `context` через human gate таким образом не выполняется. FT-07 объединяет оба разрыва, потому что они лежат в одном сценарии ответа человека.

## 3. Карточки

Карточки ниже записаны в теле отчёта, а не отдельными файлами `docs/tasks/`. Причина: добавление новых карточек в `docs/tasks/` требует пересборки `docs/ROADMAP_STATUS.md`, который CI сравнивает побайтово (`.github/workflows/ci.yml:311-316`). Этот PR меняет только документацию и не трогает индекс. Когда владелец подтвердит карточки, их нужно материализовать отдельным PR вместе с обновлением индекса.

Формат ID `FT-01` подходит под шаблон `taskstore.IsID`, поэтому карточки можно будет взять через `beacon_task_claim` после материализации.

### FT-01. Привязать порты ADR 0314 в композитном корне

Статус: todo. Фаза: P0. Тип: task. Владелец файлов: `cmd/beacon/mcp.go`, `cmd/beacon/mcp_test.go`. Зависимости: нет.

Цель: `buildMCPDependencies` задаёт `GraphIndexReader`, `RegistryStatsReader` и `GenerationLedger`, поэтому `graphReady` в claim и stats отражает реальное состояние индекса, а `beacon inspect` не показывает `no-reader`.

Контекст: PR 1608 и PR 1612 добавили порты и обработку, но оставили `cmd/beacon/mcp.go` вне OwnerFiles. Сейчас `cmd/beacon/mcp.go:748-790` не задаёт порты, а `internal/infra/mcp/server.go:733,763-765` ставит сеттеры только при не-nil.

Шаги: построить `GraphIndexReader` поверх `internal/infra/taskstore/index.go:283`, где уже есть `GraphStatus`; передать его вместе с остальными портами в `mcp.Dependencies`; добавить тест, что production-зависимости несут не-nil порт.

Проверка: с реальным проектом ответ `beacon_stats` содержит маркер свежего графа; `beacon_task_claim` возвращает `graphReady: true`; `beacon inspect --json` показывает `graphReady.source` не равным `no-reader`. Отрицательный контроль: после изменения карточки без пересборки индекса источник становится `stale`, а не `true`.

Приёмка: три порта привязаны, оба маркера отражают состояние индекса, CI зелёный.

### FT-02. Подключить адаптер расписаний к SQLite

Статус: todo. Фаза: P1. Тип: task. Владелец файлов: `cmd/beacon/task.go`, `cmd/beacon/task_test.go`. Зависимости: PR 1460 уже в main.

Цель: `beacon task schedule` пишет расписания в таблицу `schedules` через методы из PR 1460, а не в `schedules.json`.

Контекст: `internal/infra/store/schedules.go` содержит CRUD, но `cmd/beacon/task.go:902-903` вызывает `server.NewFileScheduleStore`. `docs/tasks/INTG-03.md:12` прямо отложил адаптер на wiring-карточку.

Шаги: реализовать адаптер `server.ScheduleStore` поверх `*store.DB`; заменить `scheduleStoreFor`; покрыть тестом идемпотентности и чтения после переоткрытия store.

Проверка: `beacon task schedule create` создаёт запись, видимую через `store.ListSchedules`; файл `schedules.json` не создаётся; повторное создание даёт ту же ошибку, что и файловый store.

Приёмка: файловый бэкенд больше не используется в production, CI зелёный.

### FT-03. Открыть снимок SharedGate через stats

Статус: todo. Фаза: P1. Тип: task. Владелец файлов: `internal/infra/mcp/tool_stats.go`, `internal/infra/mcp/tool_stats_test.go`. Зависимости: FT-01.

Цель: оператор видит состояние process-wide CI-гейта в `beacon_stats`, а не только в логе.

Контекст: `internal/infra/store/observability.go:786` экспортирует `SharedCIWaitSnapshot()`, но вызовов нет. PR 1582 отложил подключение к stats.

Шаги: добавить необязательное поле снимка в `StatsOutput`; заполнить его из `store.SharedCIWaitSnapshot()` в композитном корне; добавить тест с двумя ожидающими на одном ключе.

Проверка: в структурированном ответе `beacon_stats` есть ключи активных ожидающих и числа совмещённых раундов; с двумя ожидающими значение ожидающих равно двум, число раундов равно одному.

Приёмка: снимок доступен через stats и покрыт тестом, CI зелёный.

### FT-04. Завести wiring-карточки для четырёх PR

Статус: todo. Фаза: P1. Тип: task. Владелец файлов: `docs/tasks/WIRING-1460.md`, `docs/tasks/WIRING-1582.md`, `docs/tasks/WIRING-1608.md`, `docs/tasks/WIRING-1612.md`. Зависимости: FT-01, FT-02, FT-03.

Цель: отложенная проводка из четырёх PR видна оркестратору через `beacon deferred --format json` и обрабатывается первой на следующей волне.

Контекст: `AGENTS.md:117` требует вызывать `beacon_defer_wiring` сразу, как только часть фичи осталась неподключённой. `AGENTS.md:127` и `docs/templates/orchestrator-task.md:136` ставят такие записи в начало очереди. В репозитории нет карточек с номерами 1460, 1582, 1608 и 1612.

Шаги: для каждого PR завести карточку по шаблону `docs/templates/wiring-card.md`; вызвать `beacon_defer_wiring` с тем же watchID, что у исходного PR; сверить, что запись появилась в `beacon deferred`.

Проверка: `beacon deferred --format json` содержит четыре записи с нужными номерами PR и статусом `todo`; каждая ссылается на существующий файл `WIRING-<pr>.md`.

Приёмка: четыре записи видимы оркестратору, карточки материализованы в `docs/tasks/`, индекс обновлён.

### FT-05. Добавить `beacon deferred done`

Статус: todo. Фаза: P1. Тип: task. Владелец файлов: `cmd/beacon/deferred.go`, `cmd/beacon/deferred_test.go`. Зависимости: FT-04.

Цель: оператор может перевести отложенную запись в `done` из CLI, как требует шаблон wiring-карточки.

Контекст: `docs/templates/wiring-card.md:15,17` и несколько старых WIRING-карточек вызывают `beacon deferred done`. `runDeferred` (`cmd/beacon/deferred.go:126-155`) знает только `--format`. Метод `store.DB.MarkDeferredDone` есть в `internal/infra/store/deferred.go:318`.

Шаги: разобрать подкоманду `done` и аргумент watchID; вызвать `MarkDeferredDone` и вернуть ошибку `ErrDeferredNotFound` как код 2; добавить тест на повторный вызов и на неизвестный ID.

Проверка: `beacon deferred done <watchID>` возвращает код 0, а `beacon deferred --format json` показывает `status: done` для этой записи; повтор возвращает тот же результат; неизвестный watchID даёт код 2 без записи.

Приёмка: подкоманда описана в каталоге и покрыта тестом, CI зелёный.

### FT-06. Закрыть три точки входа в живой экран

Статус: todo. Фаза: P1. Тип: task. Владелец файлов: `cmd/beacon/main.go`, `cmd/beacon/catalog.go`, `cmd/beacon/up.go`, `cmd/beacon/board_wire.go`. Зависимости: нет.

Цель: `beacon tui`, `beacon up --tui` и `beacon board --live` ведут в один живой раннер.

Контекст: `docs/evals/tui-live-single-screen.md:11-16` фиксирует три точки входа как обещание TU3-01. Реализована только `board --live` (`cmd/beacon/board.go:76-119`). В `cmd/beacon/main.go:255-287` нет `tui`, в `cmd/beacon/up.go:171-177` нет `--tui`.

Шаги: зарегистрировать `tui` в `runnerFor` и каталоге; добавить `--tui` в `up`; направить оба пути в тот же адаптер, что и `board --live`; сохранить текстовый fallback для pipe.

Проверка: `beacon tui --help` и `beacon what tui` возвращают код 0; `beacon up --tui --once` доходит до рендера живого снимка; все три точки входа в тесте вызывают один и тот же `App.RunLive`.

Приёмка: три точки входа работают, eval `tui-live-single-screen` проходит, CI зелёный.

### FT-07. Довести ответ человека до состояния Watch и расширить CLI

Статус: todo. Фаза: P1. Тип: task. Владелец файлов: `cmd/beacon/mcp.go`, `cmd/beacon/human.go`, `cmd/beacon/human_test.go`. Зависимости: нет.

Цель: ответ `approve`, `reject`, `rework` или `context` возобновляет ту же Watch и будит подписчика и в HTTP-, и в локальном пути. CLI принимает все девять глаголов сервера.

Контекст: `cmd/beacon/mcp.go:1000-1043` не меняет Watch для четырёх verbs и шлёт wake с поддельным вердиктом `human:<verb>`. `cmd/beacon/human.go:35-50` не знает `context` и `force_retry_merge`. `docs/evals/w21-human-question-revision.md:24-35` требует возврата в ту же Watch с ростом ревизии на один.

Шаги: добавить в `humanDecisionSink` вызов того же ack-порта, что использует локальный путь, для `approve`, `reject`, `rework` и `context`; сохранить wake после успешного перехода; расширить `humanOutcomes`; покрыть тестом HTTP-путь на Watch в `needs_input`.

Проверка: после POST `approve` Watch имеет статус `watching`, ревизию на один больше, то же ID и доставленный wake; `beacon human context <watchID> --note ...` возвращает код 0; `force_retry_merge` тоже принимается.

Приёмка: оба пути дают одинаковый переход, набор глаголов CLI совпадает с `validCommands`, CI зелёный.

### FT-08. Проверять конфиг OTEL в doctor

Статус: todo. Фаза: P2. Тип: task. Владелец файлов: `cmd/beacon/doctor.go`, `cmd/beacon/doctor_test.go`. Зависимости: нет.

Цель: `beacon doctor` предупреждает о неверном `BEACON_OTEL_EXPORTER`, как требует eval.

Контекст: `docs/evals/e11-metrics-counters.md:20,27` описывает проверку и негативный контроль. В `cmd/beacon/doctor.go:106` три проверки, ни одной про OTEL.

Шаги: добавить проверку, которая читает значение переменной и сравнивает его с допустимым набором; печатать `WARN` с именем переменной; оставить exit code 0 для WARN по правилу `RULES-CHECK-CONTRACT.md:19`.

Проверка: `BEACON_OTEL_EXPORTER=bogus beacon doctor` печатает строку с `WARN` и именем переменной; при пустом или `otlp` предупреждения нет; код выхода 0 в обоих случаях.

Приёмка: eval e11 снимает проверку без пометки «пропущено», CI зелёный.

### FT-09. Сделать ребейзы в отчёте оркестратора адресуемыми

Статус: todo. Фаза: P2. Тип: task. Владелец файлов: `internal/app/orchestrator/orchestrator.go`, `cmd/beacon/orchestrate.go`. Зависимости: нет.

Цель: потребитель отчёта `beacon orchestrate --format json` может привязать каждый ребейз к Watch и ревизии и получить `nextAction` из `beacon_get_result`.

Контекст: `NeedsRebase` в `internal/app/orchestrator/orchestrator.go:318` это `[]int`. Таблица в `cmd/beacon/orchestrate.go:299-301` печатает только номер PR. `AGENTS.md:86` требует выполнять команду из `nextAction`, а не угадывать её по номеру PR.

Шаги: заменить `[]int` на тип с полями `watchID`, `revision` и `pr`; заполнить его из обработанных Watch; обновить табличный рендер и JSON-тест.

Проверка: в JSON каждый элемент `needsRebase` содержит `watchID`, `revision` и `pr`; по watchID вызывается `beacon_get_result` и возвращает `nextAction: rebase`; табличный вывод показывает watchID рядом с PR.

Приёмка: отчёт адресуем, тест JSONShape обновлён, CI зелёный.

## 4. Что проверено и работает

- `beacon board --live` доходит до `App.RunLive`, имеет текстовый fallback и граф по зависимостям карточек (`cmd/beacon/board.go:76-119`, `internal/tui/board.go:78-95`).
- Список глаголов живого экрана и сервера совпадает: по девять (`internal/tui/app.go:109-119`, `internal/infra/httpapi/server.go:1354-1358`).
- Локальный путь `beacon human approve` меняет `needs_input` на `watching` в той же Watch и ставит `claimedAt` (`cmd/beacon/human_test.go:278-348`).
- `beacon doctor` выполняет три проверки, обещанные в `docs/tasks/T0.6-beacon-cli-binary.md:20`.
- `beacon deferred` читает durable store и печатает table или json (`cmd/beacon/deferred.go:123-155`).
- Автоматическое закрытие отложенной записи при влитом PR работает через `cmd/beacon/mcp.go:58-76`.

## 5. Что не проверялось

Тесты, сборка, гейты, `beacon index build`, CLI Beacon и ручной merge не запускались. Номера строк сняты чтением файлов на базе `9436385e`. Проверки в карточках описаны как действия, которые должен выполнить исполнитель после материализации карточек.

Этот документ и карточки в нём регистрируются через `beacon_watch_pr` с `autoMerge: true`. Решение о мерже принимает Beacon.
