# Исследование: планировщик и реестр

База: `origin/main` = `8608917dab33456083ef9f510e8b883e1309a02e`. Читались `internal/infra/server/scheduler.go`, `internal/infra/store/schedules.go`, весь `internal/infra/registry/`, `cmd/beacon/task.go`, `cmd/beacon/registry_check.go`, `cmd/beacon/registry_config.go`, `cmd/beacon/registry_sync.go`, `internal/app/orchestrator/daemon.go`, ADR 0086, 0089, 0090, 0284, `docs/design/agent-registry-schema.md`, eval `e09-cron-fixed-time.md`, карточки T5.12, T5.18, T5.19, T5.24, T5.54, BS.23, BS.33, INTG-03, S6.014, аудиты 04 и 05. Код не менялся, тесты и гейты не запускались.

## Что исключено до поиска

INTG-03 уже описывает два пункта: CRUD расписаний в SQLite и отказ от `schedules.json`. Методы `CreateSchedule`, `GetSchedule`, `ListSchedules`, `UpdateSchedule`, `DeleteSchedule`, `SetScheduleEnabled` уже лежат в `internal/infra/store/schedules.go:273,305,325,358,390,420`. Аудит 05 и карточка T5.54 уже описали не подключённый `registryListSource` и отложенную проводку `scheduleStoreFor`. Ниже этих повторов нет.

## Находки

### SR-F1. Секретный guard не вызывается и понимает только JSON

`internal/infra/registry/secretguard.go:48` `GuardConfig` и `:72` `GuardFile` не имеют вызовов вне тестов. Поиск `rg -n 'GuardConfig|GuardFile' . --glob '!*.md'` по всему дереву возвращает только их определения и `secretguard_test.go`. Загрузка авторского конфига идёт мимо guard: `cmd/beacon/main.go:399` `yaml.Unmarshal` и `:407` `checkRegistryConfig`, а `cmd/beacon/registry_check.go:220` `checkRegistryConfig` не вызывает guard. Второй дефект в том же файле: `secretguard.go:54` разбирает тело через `json.Unmarshal`, а конфиг это YAML (`cmd/beacon/registry_config.go:15` `agents.yaml`). Подключение guard как есть отвергнет любой корректный конфиг как `invalid JSON`. Карточка T5.18 помечена done, а HD-070 в подтверждении утверждает, что конфиг проверяется до регистрации. Утверждение не соответствует коду.

Приоритет: P1. Это средство защиты, которое существует в дереве, но не работает на живом пути.

### SR-F2. Тип поля `version` противоречит сам себе

`internal/infra/registry/validate.go:63` требует, чтобы `schema`, `version` и `project` были строками. `internal/infra/registry/migrate.go:100` `readVersion` разбирает `version` как целое и `:121` `checkSchema` требует префикс `beacon.agent-registry/v`. `docs/design/agent-registry-schema.md:12` тоже описывает `version` как целое число. Тесты закрепили обе версии: `internal/infra/registry/migrate_test.go:121` отвергает строковый `version`, а `internal/infra/registry/validate_test.go:13` и `cmd/beacon/registry_check_test.go:14` считают строку корректной. Живой путь использует `Validate` после `yaml.Unmarshal`, поэтому документированный пример `version: 1` из `docs/design/agent-registry-schema.md:107` даёт целое число и отвергается валидатором.

Приоритет: P1. Корректный по документации конфиг не проходит проверку.

### SR-F3. Проверка версии и схемы обходится

`internal/infra/registry/migrate.go:55` `Migrate` не имеет вызовов вне тестов. `cmd/beacon/registry_check.go:220` `checkRegistryConfig` выполняет только `Validate`, `ParseLimits` и проверку индекса. Поэтому конфиг с произвольным строковым `version` и чужим `schema` проходит `beacon registry check`, хотя `migrate.go:112` `checkSchema` и `:87` явно отвергают такие значения. Тесты `migrate_test.go:118` покрывают отказ, но до живого пути не доходят.

Приоритет: P2. Проверка есть, но не подключена.

### SR-F4. Пустой индекс молча принимается локальной проверкой

`cmd/beacon/registry_check.go:199` возвращается из `checkIndex`, если суммарное число capabilities равно нулю. `internal/infra/registry/index_check.go:22` и `:42` `CheckCapabilityIndex` прямо запрещает пустой индекс при непустом реестре. Поле `capabilities` необязательно: `internal/infra/registry/validate.go:82` требует только `roles`. Итог: реестр с исполнителями без capabilities проходит проверку, хотя контракт T5.24 требует отказа.

Приоритет: P2. Обход написан явно, исправление локально.

### SR-F5. Поля `alias` и `recommended` не проверяются

`internal/infra/registry/validate.go:34` разрешает ключи `alias` и `recommended` в конфигурациях, но `:96` проверяет только `members` и только как список строк. Тип `alias` и `recommended`, а также ссылочная целостность не проверяются нигде. `docs/design/agent-registry-schema.md:141` требует, чтобы каждый alias ссылался на существующую запись, а `recommended` указывал на один alias. Несуществующий alias проходит проверку молча.

Приоритет: P2. Требование схемы не реализовано.

### SR-F6. Расписание создаётся без `NextRunAt`

`internal/infra/server/scheduler.go:210` `buildSchedule` заполняет ID, часы и выражение, но не задаёт `NextRunAt`. Поле остаётся нулевым после `Schedule` и после записи в `FileScheduleStore`. `internal/infra/store/schedules.go:222` `validateSchedule` отвергает нулевой `NextRun`. После подключения адаптера к таблице каждое расписание из порта будет отклонено. eval `docs/evals/e09-cron-fixed-time.md` в негативном контроле требует, чтобы задача без `next_run_at` не запускалась после рестарта, но ни один компонент это время не вычисляет.

Приоритет: P1. Блокер для уже отложенной проводки расписаний.

### SR-F7. Разные словари видов расписания

`internal/infra/server/scheduler.go:35` объявляет `KindMark = "mark"`. `internal/infra/store/schedules.go:35` объявляет `ScheduleKindWebhook = "webhook"`, а `:68` `validScheduleKind` разрешает только `interval`, `cron`, `webhook`. CLI принимает `mark` в `cmd/beacon/task.go:925`. Адаптер порта, описанный в INTG-03, передаст строку `mark` в `validateSchedule` и получит отказ на `internal/infra/store/schedules.go:219`. Преобразования `mark` в `webhook` в дереве нет.

Приоритет: P1. Отказ появится сразу после подключения SQLite-стора. Карточка SR-04 отделяет нормализацию вида от CRUD INTG-03. INTG-03 уже влил методы CRUD, поэтому это не дубль, а продолжение того же контракта. OwnerFiles пересекаются с INTG-03, но INTG-03 лежит в P1, а SR-04 в P2.

### SR-F8. Нет маршрута Resume и нет цикла запуска

`internal/infra/server/scheduler.go:155` определяет `Resume`, `cmd/beacon/task.go:904` его вызывает, но `internal/infra/server/scheduler.go:486` `Register` монтирует только `schedule`, `list`, `pause`, `delete`. Внутренний HTTP API не умеет возвращать расписание из паузы. Шире: `internal/infra/store/schedules.go:113` `DueSchedules` и `:152` `MarkTriggered` не имеют вызовов вне тестов. `internal/app/orchestrator/daemon.go:65` `DaemonRun` ведёт цикл Watch, но не опрашивает расписания. ADR 0284 описывает планировщик, а реализационного планировщика нет. Карточка S6.014 закрыта как dropped с формулировкой "серверного планировщика нет", и с тех пор появился только серверный API, не цикл.

Приоритет: P2 для маршрута Resume, P1 для отсутствия цикла. Без цикла все расписания остаются записями.

## Сводка приоритетов

P1: SR-F1, SR-F2, SR-F6, SR-F7, часть SR-F8.
P2: SR-F3, SR-F4, SR-F5, маршрут Resume.
