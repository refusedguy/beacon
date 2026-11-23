# Аудит безопасности beacon-impl

Дата: 2026-11-21. Репозиторий: `beacon-impl`, коммит `d3b1bfb1`.

## Как снимался замер

Поиск шёл через `rg` с явными каталогами `cmd`, `internal`, `tools`, без `grep -r` по корню. Тестовые файлы `*_test.go` исключались. Команды `go test`, `go build` и `beacon gates` не запускались. Все выводы получены чтением исходников. Оценки severity статические: ни одна уязвимость не проверена запуском кода.

Проверенные классы: `exec.Command` с внешними данными, `filepath.Join` с внешними данными, утечка секретов в логи, HTTP по URL из конфига и env, гонки файловых замков, привилегии при GC и worktree, разбор YAML и JSON. Отдельно проверены ntfy, redaction токена Telegram, проверки actor в human-decide, batch-apply от derived docs и ACP-транспорт.

## Главные находки

### 1. Слэш-команды Telegram обходят реестр отправителей и меняют состояние watch

Вектор. `upHandleTick` (`cmd/beacon/up.go:245-256`) передаёт каждое сообщение в `handleUpCommand`. В `cmd/beacon/up.go:266` проверка channel ingress пропускает любое сообщение, начинающееся с `/`. Дальше `ParseBotCommand` (`internal/infra/wake/telegram.go:269-290`) разбирает команду без всякой проверки chat ID. Ветка `cmd/beacon/up.go:284-290` вызывает `decisions.Decide` для `/approve`, `/unblock`, `/pause`. `upDecisionAdapter.Decide` (`cmd/beacon/up.go:85-97`) не принимает actor и не проверяет права.

Реестр отправителей живёт только в `cmd/beacon/server.go:33-37` и применяется в `internal/infra/channel/ingress.go:134-137`. Флаг `BEACON_CHANNELS` тоже не влияет на слэш-команды. Любой пользователь, который может написать боту, способен закрыть watch, снять блокер или поставить subscriber'ов на паузу, зная watchID.

Severity: high. Приоритет: P0.

Fix. Перед dispatch в `handleUpCommand` проверять chat ID через тот же `ActorResolver`, что и channel ingress, и передавать полученного actor в `httpapi.ClientDecision`. Для команд без actor вернуть отказ и записать его в аудит. Убрать условие `!strings.HasPrefix(..., "/")` из `cmd/beacon/up.go:266` или дублировать проверку sender'а в слэш-ветке. Добавить тест на отказ неизвестного chat ID для `/approve`.

### 2. GitHub-токен уходит на произвольный хост через `BEACON_GITHUB_API` и невалидированный путь

Вектор. `newEnvFailureExcerptSource` (`internal/infra/mcp/tool_get_result.go:568-581`) берёт базовый URL из `BEACON_GITHUB_API` без проверки схемы и хоста. Значение по умолчанию `https://api.github.com`, но env может указать что угодно. `FailureExcerpt` (`:604-636`) собирает URL через `fmt.Sprintf` из `repo` и `headSHA`, которые приходят из Watch (`w.Repo`, `w.HeadSHA`, вызов в `:467`). Значения не проверяются на `..`, `?`, `@` или символы пути. `getJSON` (`:639-646`) ставит `Authorization: Bearer <токен>` на каждый запрос и не ограничивает получателя.

Пример: `headSHA` вида `abc/../../other` меняет путь запроса, а подмена базы в env отправляет заголовок Authorization на внутренний или внешний сервер. Токен из `BEACON_GITHUB_TOKEN` при этом не проходит через `observe.Redact`, если попал в текст ошибки транспорта, потому что шаблоны `Redact` покрывают только токены GitHub формата `ghp_`/`github_pat_` и Basic-заголовки.

Severity: high. Приоритет: P0.

Fix. Проверять базу на `https` и явно разрешать только хосты `api.github.com` и loopback для тестов. Кодировать `owner`, `name`, `headSHA` через `url.PathEscape` вместо `fmt.Sprintf` или перейти на go-github, как в `internal/infra/github/rest.go:308-343`, где клиент сам кодирует параметры. Не прикладывать Bearer-заголовок к хосту, который не прошёл проверку.

### 3. Базовый URL Telegram из env отправляет токен в пути запроса на любой адрес

Вектор. `TelegramConfigFromEnv` (`internal/infra/wake/telegram.go:50-58`) читает `BEACON_TELEGRAM_API`. `NewTelegramNotifySink` (`:73-83`) принимает любое значение, не проверяя схему и хост. `call` (`:162-190`) собирает endpoint как `s.base + "/bot" + s.cfg.Token + "/" + method` (`:167`). Токен находится в пути URL. Если env указывает на хост злоумышленника, токен уходит вместе с первым же запросом.

Собственный `sanitize` (`:227-236`) вырезает точное вхождение токена и зовёт `observe.Redact`, но `observe.Redact` (`internal/infra/observe/log.go:82-99`) не знает формат токенов Telegram (`<цифры>:<35 символов>`). Любой другой компонент, который залогирует этот токен, не очистит его.

Severity: medium. Приоритет: P1.

Fix. Ограничить `APIBase` списком `https://api.telegram.org` и loopback-адресами для тестов. Добавить шаблон Telegram-токена в `observe.Redact` и в `store.Redact` (`internal/infra/store/redaction.go:146-166`), чтобы дублированные фильтры не расходились. Хранить токен в заголовке или в теле запроса, если протокол позволяет, и не класть его в URL.

### 4. Path traversal в batch-apply через идентификатор карточки из derived docs

Вектор. `loadBatches` (`internal/infra/batchapply/apply.go:300-322`) читает исследовательский JSON без проверки содержимого. Поля `c.ID` и `closure.ID` (`:584`, `:590`) идут в `filepath.Join` без валидации: `:655` собирает `docs/tasks/<id>.md`, `:454` работает с закрытиями. `writeFileMode` (`:331-348`) соединяет это с root и пишет через `os.WriteFile`. Идентификатор вида `../../config` выводит запись за пределы `docs/tasks`. `slug` (`:276-287`) безопасен, потому что оставляет только `a-z0-9`, но к ID он не применяется.

Severity: medium. Приоритет: P1.

Fix. Проверять ID по шаблону `^[A-Za-z0-9][A-Za-z0-9._-]*$`, отклонять `..` и любые разделители пути до попадания в `filepath.Join`. Проверять итоговый путь через `filepath.Rel(root, path)` и отказывать, если он начинается с `..` или абсолютный. Применять ту же проверку к `cardClosure.ID` и к `OwnerFiles`.

### 5. batch-apply может мержить PR по данным из исследовательского документа

Вектор. `applyMergePlan` (`internal/infra/batchapply/apply.go:477-529`) берёт решения из `merge_decisions` тех же JSON-файлов и выполняет `gh pr merge` (`:519`). Решения проходят проверку CI через `gh pr checks` (`:484`), но условие `:514` разрешает merge, если список проверок пуст: `len(checks) > 0 && green < len(required)`. Ошибка `no checks reported` тоже превращается в пустой список (`:487-489`). То есть документ, который помечает PR без обязательных проверок, проходит как «doc-only».

Отдельно: `AGENTS.md` прямо запрещает `batch-apply` без команды владельца. Код этого запрета не проверяет, он исполняет `--merge-plan` по флагу (`cmd/batch-apply/main.go`).

Severity: medium. Приоритет: P1.

Fix. Требовать непустой список проверок для любого PR, у которого есть изменение кода, и отдельно подтверждать doc-only PR по changed files, а не по тексту ошибки `gh`. Добавить явный подтверждающий флаг владельца для `--merge-plan`, например `--confirm-merge`, и проверять его в `Run` до вызова `applyMergePlan`. Логировать каждое решение с хешем исходного JSON.

### 6. Гонка check-then-remove в файловом замке taskstore

Вектор. `AcquireLock` (`internal/infra/taskstore/store.go:350-395`) создаёт файл через `O_CREATE|O_EXCL`, но при `ErrExist` читает его отдельным вызовом `lockState` (`:377`, `:399-425`) и удаляет через `os.Remove` (`:379`, `:386`). Между чтением и удалением другой процесс может удалить и пересоздать файл. Два процесса получат эксклюзивный доступ одновременно. Дополнительно `processAlive` (`:427-433`) использует `kill(pid, 0)`, который можно обмануть переиспользованием PID.

В коде уже есть признание проблемы: комментарий `internal/app/taskcli/service.go:1442-1447` описывает именно эту гонку и объясняет, почему healing-замок использует `syscall.Flock`. Poller-замок в `cmd/beacon/mcp.go:1060-1077` тоже на `Flock`. Общий замок карточек остался на старой схеме.

Severity: medium. Приоритет: P1.

Fix. Заменить `AcquireLock` на `Flock(LOCK_EX|LOCK_NB)` на открытом дескрипторе, как в `lockHealing`. Удалить эвристику stale и PID-файл: ядро снимает блокировку при смерти процесса. Сохранить `LockPath` только как имя файла.

### 7. Actor в human-decide не привязан к аутентифицированному принципалу

Вектор. Маршрут `POST /api/v1/human/decide` (`internal/infra/httpapi/server.go:666`) защищён bearer-токеном (`:670-679`) и проверкой Origin (`:681-716`). Но `Decision.Actor` (`:197-208`) приходит в теле запроса. `handleDecide` (`:933-990`) не проверяет его и пишет в аудит как есть (`:987`). Sink в `cmd/beacon/mcp.go:617-629` добавляет `d.Actor` в текст события (`:628`), меняет статус watch, снимает блокеры и ставит subscriber'ов на паузу (`:635-673`).

Второй путь: `CommandProcessor.handle` (`internal/infra/server/api_wake.go:278-309`) тоже берёт `Actor` и `Ingress` из JSON. `execute` (`:203-219`) проверяет только непустоту actor и допустимость ingress. Сам `Handler()` в продакшене не смонтирован, но контракт уже допускает подмену.

Практический риск ограничен тем, что bearer-токен один на сервер. Владелец токена и так может всё. Проблема в том, что журнал audit и event log записывают произвольное имя как сигнал от человека, что ломает расследование и любую будущую проверку прав по actor.

Severity: medium. Приоритет: P2.

Fix. Не принимать actor из тела запроса. Выводить его из bearer-токена или из отдельной привязки токен → actor на границе. Если контракт требует поле actor, проверять, что оно совпадает с аутентифицированным принципалом до вызова sink. В `CommandProcessor.handle` убрать `Actor` и `Ingress` из `commandRequest` и задавать их middleware.

### 8. ACP-транспорт: исполняемый файл из env и неограниченное чтение HTTP-ответа

Вектор. `spawnACPConfig` (`cmd/beacon/spawn.go:254-269`) принимает `BEACON_ACP_COMMAND` и `BEACON_ACP_ARGS`. Команда целиком заменяет встроенный runner (`internal/infra/wake/acp.go:454-471`), затем `PersistentACPClient.Start` выполняет `exec.Command` (`:559-561`) и `ACPStdioTransport.Call` делает то же (`:143-145`). Оболочки нет, `strings.Fields` не интерпретирует кавычки, поэтому это не shell injection. Но кто управляет env, тот выбирает исполняемый файл.

HTTP-ветка `ACPHTTPTransport` (`internal/infra/wake/acp.go:181-220`) не проверяет URL и читает ответ через `io.ReadAll(resp.Body)` без лимита (`:213`). В продакшене этот тип не конструируется, но контракт открыт для вызывающих.

Severity: low. Приоритет: P2.

Fix. Оставлять env-override только для тестов и явно отключать его в продакшенной сборке или требовать отдельный флаг `--allow-acp-command-override`. Для HTTP-транспорта добавить `io.LimitReader` с явным потолком и проверять схему `http/https` плюс loopback.

### 9. Гонка между Listen и Chmod на Unix-сокете TUI

Вектор. `internal/tui/socket.go:198-212` сначала вызывает `net.Listen("unix", path)` (`:205`), а потом `os.Chmod(path, 0600)` (`:209`). До chmod сокет живёт с правами по umask, обычно 0755. В этом окне другой локальный пользователь может подключиться и отправить команду в `dispatch` (`:271`).

Severity: low. Приоритет: P3.

Fix. Создавать сокет в каталоге с режимом 0700 до вызова `Listen`. Альтернатива: выставить `umask 0077` на время `Listen` и вернуть его сразу после. Добавить проверку режима каталога при старте.

### 10. OTLP endpoint из env без ограничения адреса

Вектор. `ConfigFromEnv` (`internal/infra/observe/otel.go:151-162`) берёт `OTEL_EXPORTER_OTLP_ENDPOINT` и `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`. `url.Parse` в `:362` не проверяет хост. `:434-439` отправляет POST на заданный адрес. При включённом tracing это канал для исходящих запросов на любой адрес из env. Секретов в теле нет, поэтому риск ниже, чем в находке 2.

Severity: low. Приоритет: P3.

Fix. Проверять схему `http/https` и явно разрешённые хост+порт. Отключать экспорт по умолчанию, когда endpoint указывает не на loopback.

## Отдельные проверки

### ntfy URL из env

Реализации нет. Поиск по `cmd`, `internal`, `tools`, `scripts`, `.github`, `docs` нашёл только карточку `docs/tasks/T3.283.md:3`, где прямо сказано, что упоминаний ntfy в коде нет и адаптер не написан. Текущая сеть уведомлений это Telegram (`internal/infra/wake/telegram.go`) и порт `NotificationSink` (`internal/infra/wake/notify.go`). SSRF-вектор через ntfy отсутствует. Ближайшие аналоги разобраны в находках 2, 3 и 10.

### Redaction токена Telegram

`sanitize` в `internal/infra/wake/telegram.go:227-236` вырезает точное значение токена и вызывает `observe.Redact`. Все ошибки в `call` проходят через него (`:170-188`). Текст уведомления очищается в `SendText` (`:111`). Проблема в глобальном фильтре: `observe.Redact` (`internal/infra/observe/log.go:82-99`) и `store.Redact` (`internal/infra/store/redaction.go:146-166`) не знают формат токена Telegram. Защита работает только внутри одного адаптера. Дублирование фильтров между `observe` и `store` уже отмечено в комментарии `log.go:79-81`, и любое расширение одного без другого создаст расхождение.

### Проверки actor в human-decide

Проверки есть на трёх уровнях, но ни один не связывает actor с аутентификацией:

- `internal/infra/httpapi/server.go:943-954` проверяет команду, watchID и версию контракта. Actor не проверяется.
- `internal/infra/server/api_wake.go:214-219` требует непустой actor и допустимый ingress, но оба поля приходят из JSON.
- `internal/infra/channel/ingress.go:134-137` корректно резолвит sender через реестр, но этот путь обходится слэш-командами Telegram (находка 1).

Bearer-токен и Origin проверяются, так что для атаки нужен доступ к токену или к боту. Это снижает практический риск, но не снимает проблему подмены идентичности в журнале.

### batch-apply от derived docs

Два отдельных канала доверия: запись файлов по идентификатору из JSON (находка 4) и merge PR по `merge_decisions` (находка 5). `loadBatches` (`apply.go:300-322`) считает любой валидный JSON годным, проверка фактов (`:549-555`) не проверяет идентификаторы. Переназначение номеров ADR (`:594-627`) защищает от перезаписи существующих ADR, но не защищает от traversal в `id`.

### ACP транспорт

Stdio-вариант создаёт процесс через `exec.Command` без shell (`acp.go:143`, `:559`). Команда и аргументы приходят из env (находка 8). HTTP-вариант (`acp.go:181-220`) не имеет проверки URL и лимита тела. В продакшене конструируется только `NewWorkerRuntime` → `NewPersistentACPClient` (`acp.go:477-488`) со stdio. Capability negotiation в `:230-419` работает с JSON из доверенного процесса и не принимает сетевые данные.

### Разбор YAML и JSON

Небезопасных конструкций не найдено. `gopkg.in/yaml.v3` разбирает только в `map[string]any` (`tool_watch_pr.go:117-120`, `repo_detect.go:64-66`, `checkall.go:177-180`), что не создаёт произвольные типы. JSON разбирается в конкретные структуры. Входные тела ограничены: `http.MaxBytesReader` в `httpapi/server.go:935`, `MaxBodyBytes` 25 MB в `webhook/signature.go:17-19`, `io.LimitReader` на ответах в `opencode/client.go:262` и `tool_get_result.go:655`. Подпись webhook проверяется в константное время (`webhook/signature.go:33-48`), файл секрета требует 0600 (`:73-92`). Замечаний нет.

### Дополнительно: Kill по порту

`releasePort` → `portProcessKiller` (`cmd/beacon/agents.go:570-593`) выполняет `fuser -k <port>/tcp` для всех слушателей порта, а не только для процесса задачи. Если номер порта в реестре окажется занят другим сервисом, cleanup убьёт чужой процесс. Severity: low, приоритет P3. Fix: сохранять PID в реестре и проверять его перед kill, либо убивать конкретный PID, а не всех слушателей порта.

## Итоговая таблица, топ-10

| # | Находка | Файл:строка | Severity | Приоритет |
|---|---|---|---|---|
| 1 | Слэш-команды Telegram обходят реестр отправителей | `cmd/beacon/up.go:266`, `:284-290` | high | P0 |
| 2 | GitHub-токен и SSRF через `BEACON_GITHUB_API` | `internal/infra/mcp/tool_get_result.go:573`, `:612`, `:644` | high | P0 |
| 3 | Токен Telegram в URL по адресу из env | `internal/infra/wake/telegram.go:57`, `:167` | medium | P1 |
| 4 | Path traversal в batch-apply через card ID | `internal/infra/batchapply/apply.go:584`, `:655`, `:347` | medium | P1 |
| 5 | Merge PR по derived docs без проверки кода | `internal/infra/batchapply/apply.go:477-529` | medium | P1 |
| 6 | Гонка в `taskstore.AcquireLock` | `internal/infra/taskstore/store.go:377-391` | medium | P1 |
| 7 | Actor human-decide не привязан к аутентификации | `internal/infra/httpapi/server.go:987`, `cmd/beacon/mcp.go:628` | medium | P2 |
| 8 | ACP command из env и лимит HTTP-ответа | `cmd/beacon/spawn.go:263-268`, `internal/infra/wake/acp.go:213` | low | P2 |
| 9 | Гонка Listen/Chmod на Unix-сокете | `internal/tui/socket.go:205-209` | low | P3 |
| 10 | OTLP endpoint из env без списка адресов | `internal/infra/observe/otel.go:152-160`, `:434` | low | P3 |

Вне таблицы: `fuser -k` по номеру порта (`cmd/beacon/agents.go:584`), low/P3.

## Что не проверялось

Все оценки статические. Запуск кода, сетевые запросы, проверка реальных прав файлов и воспроизведение гонок не выполнялись. Для находок 1, 4 и 6 есть прямой текстуальный proof в коде или комментариях разработчиков. Для находок 2, 3, 8 и 10 эксплойт зависит от того, кто может менять env процесса, и этот факт в рамках аудита не устанавливался.
