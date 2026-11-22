# Разрывы конфигурации и env: research-config-env

Дата: 2026-11-22. База: `origin/main` @ `ad9f3889`. Режим: read-only. Код не менялся, сборка, `go test` и `beacon gates` не запускались.

## Метод и границы

Источник фактов: `git grep` по каталогам `cmd internal tools` и чтение файлов в свежем дереве `origin/main`. Для сверки с документацией смотрел `README.md` и `docs/tasks`. Каждая строка таблицы содержит путь, номер строки и символ.

Проверял четыре класса разрывов: env без валидации, env без документации, противоречия между кодом и документацией, флаги CLI без env-эквивалента.

Не повторяю BCX-74..79. Эти карточки закрывают `BEACON_ACP_COMMAND` и `BEACON_ACP_ARGS`, документацию 21 переменной, три имени корня проекта, разбор булевых значений, пятнадцать тихих default и протокол `grpc`. Не повторяю `docs/research/config-env-audit.md`: там дана инвентаризация переменных и общие правила валидации. Ниже только новые разрывы.

## Таблица находок

| ID | Разрыв | Доказательство, file:line + символ | Класс | Приоритет |
| --- | --- | --- | --- | --- |
| CE-01 | Ключ `OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT` объявлен и описан как читаемый composition root, но production-код его не читает | `internal/infra/observe/observe.go:115-118` комментарий и `ContentCaptureEnv`; `:123` `ParseContentCaptureMode`; `:147` `SetContentCapture`. Вызовы парсера и сеттера только в `internal/infra/observe/observe_test.go:156,161,167-180`. `docs/tasks/RS-OBS-04.md:20,23` утверждают, что парсер разбирает значение env | переменная не читается | P1 |
| CE-02 | `BEACON_DAEMON_INTERVAL` описан как настройка команды `beacon daemon`, но команда его не читает; ссылки и default в README устарели | `cmd/beacon/daemon.go:27-30` только константа `daemonIntervalEnv`; единственный читатель `cmd/beacon/mcp.go:204` `os.Getenv(daemonIntervalEnv)` в in-process seam, `:210` `observe.WarnConfig`. Флаг команды `cmd/beacon/daemon.go:35` `daemonIntervalDefault = 0` и `:472` `fs.Duration("interval", daemonIntervalDefault, ...)`. `README.md:291` пишет default `3m`, ссылки `daemon.go:25,219` и `mcp.go:60-68`, а также «неверное молча даёт 3m» | противоречие кода и документации | P1 |
| CE-03 | `BEACON_NTFY_URL` и `BEACON_NTFY_TOPIC` читаются без проверки схемы, хоста и формата топика | `internal/infra/wake/ntfy.go:20-21` `NtfyURLEnv`, `NtfyTopicEnv`; `:46-52` `NtfyConfigFromEnv` только `TrimSpace`; `:70` default `defaultNtfyURL`; `:105` `endpoint := s.base + "/" + url.PathEscape(s.topic)` и POST. `README.md:267-268` фиксирует риск, но проверки в коде нет и карточки на неё нет | env без валидации | P1 |
| CE-04 | Три долгоживущие команды имеют `--interval` без env-эквивалента, хотя `beacon daemon` использует env для того же цикла | `cmd/beacon/up.go:152` `fs.Duration("interval", 2*time.Second, ...)` и `:232` `time.NewTicker(*interval)`; `cmd/beacon/watch.go:214` и `:443,632`; `cmd/beacon/doctor.go:37`. В этих файлах нет `os.Getenv` для интервала. Контраст: `cmd/beacon/mcp.go:204` читает `BEACON_DAEMON_INTERVAL` | флаг CLI без env-эквивалента | P2 |

## P0

Новых P0 не найдено. Три находки P1 ниже дают утечку управления конфигурацией или SSRF-канал, но каждая из них требует доступа к env процесса, поэтому к P0 их относить не стал.

## P1

### CE-01. Фантомный ключ захвата содержимого

`internal/infra/observe/observe.go:115-117` прямо говорит: composition root читает ключ один раз и ставит результат через `SetContentCapture`. Константа объявлена в `:118`. Парсер `ParseContentCaptureMode` в `:123` и сеттер `SetContentCapture` в `:147` существуют. Поиск по production-файлам не находит ни одного `os.Getenv(ContentCaptureEnv)` и ни одного вызова парсера вне тестов. Единственные вызовы лежат в `internal/infra/observe/observe_test.go:156,161,167-180`.

Карточка `docs/tasks/RS-OBS-04.md:20` пишет, что парсер разбирает значение `OTEL_INSTRUMENTATION_GENAI_CAPTURE_CONTENT`, а `:23` подтверждает, что реализация влита PR #866. Реализация действительно влита, но только в части перечисления, парсера и границы записи. Оператор не может включить opt-in через env: процесс всегда остаётся в режиме `ContentCaptureNoContent`, пока вызывающая сторона не вызовет `SetContentCapture` из Go-кода.

По умолчанию это безопасно, и карточку RS-OBS-04 закрывать не нужно. Нужно либо подключить чтение ключа на границе процесса, либо убрать из комментария и карточки обещание env-управления.

### CE-02. Env демона не управляет командой демона

`README.md:291` описывает `BEACON_DAEMON_INTERVAL` со default `3m`, ссылками `cmd/beacon/daemon.go:25,219` и `cmd/beacon/mcp.go:60-68` и фразой «неверное молча даёт 3m». На текущей главной ветке эти ссылки не указывают на читателя. В `cmd/beacon/daemon.go:27-30` лежит только константа. Единственный production-читатель находится в `cmd/beacon/mcp.go:204` внутри `mcpDaemonInterval`, то есть в in-process seam MCP-сервера, о чём говорит комментарий в `cmd/beacon/daemon.go:27-29`.

Команда `beacon daemon` берёт интервал из флага. `cmd/beacon/daemon.go:35` задаёт `daemonIntervalDefault = 0`, `:39` описывает его как адаптивные 10s или 30s, а `:472` подставляет это значение в `fs.Duration`. Env в этом пути не участвует. Дополнительно `cmd/beacon/mcp.go:210` теперь вызывает `observe.WarnConfig` на неверном значении, поэтому утверждение README про молчаливый default тоже устарело.

Итог: оператор, который выставил `BEACON_DAEMON_INTERVAL` для `beacon daemon`, не меняет поведение команды, а документация ведёт по несуществующим строкам.

### CE-03. Нет ограничения адреса ntfy

`internal/infra/wake/ntfy.go:20-21` объявляет `BEACON_NTFY_URL` и `BEACON_NTFY_TOPIC`. `NtfyConfigFromEnv` в `:46-52` применяет только `TrimSpace`. `NewNtfyNotifySink` в `:68-70` обрезает конечные слеши и подставляет `https://ntfy.sh`, но не проверяет схему и хост. `Send` в `:105-106` собирает адрес из строки env и отправляет POST.

`README.md:267` уже называет риск: уведомление уходит на произвольный хост. Проверки при этом нет, а отдельной карточки на неё в `docs/tasks` нет. Уровень совпадает с разбором `BEACON_TELEGRAM_API`: доступ к env процесса даёт outbound-запрос на внутренний адрес. Минимальная мера: разрешить только `http` и `https`, отклонить приватные сети и пустой хост, предупредить через `observe.WarnConfig` при неверном значении. Для топика стоит проверить формат в `:52`, сейчас неверное значение молча отключает sink через `Configured` в `:85`.

## P2

### CE-04. Интервалы долгоживущих команд без env

`beacon up` и `beacon watch` работают как долгоживущие процессы, но их интервал задаётся только флагом. `cmd/beacon/up.go:152` объявляет `--interval` со default `2s`, `:232` создаёт `time.NewTicker(*interval)`. `cmd/beacon/watch.go:214` объявляет тот же флаг, `:443` и `:632` используют его в живом контуре. `cmd/beacon/doctor.go:37` задаёт интервал внешней проверки только флагом. В этих трёх файлах нет чтения env для интервала.

Для `beacon daemon` такой env существует, хотя и подключён к in-process seam, как описано в CE-02. Разница мешает оператору держать конфигурацию в одном месте: сервисный_unit должен редактировать argv, а не env.

Рядом лежит ещё один пример того же класса. `cmd/beacon/prcreate.go:289` задаёт `--base` со default `main` и не имеет env-эквивалента, тогда как `--provider` получает `BEACON_PR_PROVIDER` через `internal/app/prcreate/config.go:11,18`, а `--target` получает `BEACON_PARENT_SESSION` в `cmd/beacon/prcreate.go:305`.

## Что не проверялось

Сборка, тесты и gates не запускались по запрету AGENTS.md. Все выводы получены чтением файлов на `origin/main` @ `ad9f3889`. Эксплуатируемость SSRF-канала из CE-03 не оценивалась отдельно, как и поведение бинарника.
