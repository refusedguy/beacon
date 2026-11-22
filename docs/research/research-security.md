# Исследование: безопасность и редактирование секретов

Дата: 2026-11-22. База: origin/main b693e56d. Репозиторий: beacon-impl.

## Метод

Чтение исходников в `cmd`, `internal`, `tools`. Команды `go test`, `go build` и `beacon gates` не запускались. Каждая строка таблицы содержит путь, номер строки и символ. Сеть и реальные права файлов не проверялись запуском. Приоритеты статические.

## Что не повторяется

AUD-04 влит PR #1470 и перевёл `openProcessStore` на `store.OpenProduction` (`cmd/beacon/mcp.go:707`). BCX-73 и BCX-74 закрывают хост Telegram и команду ACP из env. `docs/research/security-audit.md` разобрал SSRF через `BEACON_GITHUB_API`, токен в URL Telegram, actor human-decide и ACP-транспорт. Новые находки лежат в другом слое: три копии фильтра редактирования, контур драйвера sqlite-redact и файлы логов коротких команд.

## Таблица находок

| ID | Находка | file:line + символ | Риск | Приоритет |
| --- | --- | --- | --- | --- |
| SE-01 | Аудит HTTP API без редактирования полей | `internal/infra/httpapi/server.go:1358` `recordAudit`, `:1150` route | Actor и Command из тела запроса уходят через GET /api/v1/audit в открытом виде | P1 |
| SE-02 | Третий фильтр редактирования не знает токен Telegram | `internal/infra/server/api_audit.go:282-299` `auditTokenRE`, `redactAuditString` | Токен бота в полях аудита проходит в экспорт, хотя store и observe его маскируют | P1 |
| SE-03 | Драйвер sqlite-redact подключён только к одному открытию БД | `cmd/beacon/mcp.go:707` `OpenProduction`, `cmd/beacon/spawn.go:74,281,436` `store.Open`, `cmd/beacon/task.go:304` `store.Open` | Записи ACP, runtime_stats и сессий идут в SQLite без редактирования на уровне драйвера | P1 |
| SE-04 | Логи коротких команд пишутся без редактирования и с режимом 0644 | `cmd/beacon/observe.go:262` `OpenCLILog`, `internal/infra/observe/log.go:190,175,238` | Секрет в логе CLI остаётся в файле, доступном всем локальным пользователям | P2 |

## P0

Нет. Все четыре находки требуют дополнительного условия: секрет уже попал в поле запроса, текст ошибки или результат ACP.

## P1

### SE-01. Аудит HTTP API отдаёт поля запроса без редактирования

`internal/infra/httpapi/server.go:131` объявляет `AuditRecord`. Символ `s.ops.recordAudit` в `:1358` копирует `d.Actor` и `result.Command` из тела запроса без редактирования. `auditView` в `:719` и `s.ops.auditView` в `:1150` отдают записи через `GET /api/v1/audit`. Постоянный аудит применяет `redactAuditEntry` (`internal/infra/server/api_audit.go:301`, вызов в `:219`), поэтому endpoint с одинаковым путём ведут себя по-разному. Символ `ACTOR_NOT_PERMITTED` в `:1324` вставляет `d.Actor` в текст ошибки 403.

Риск: ключ или Bearer-токен, переданный в `actor` или `command`, возвращается вызывающему в открытом виде и остаётся в ответе аудита. Постоянный аудит такой токен маскировал бы.

Проверка: тест шлёт `actor` с токеном формата `ghp_` и с Telegram-токеном и ожидает `[REDACTED]` в `GET /api/v1/audit` и в теле 403.

### SE-02. Третий фильтр редактирования не знает токен Telegram

`internal/infra/server/api_audit.go:279-281` заявляет, что шаблоны совпадают с фильтром store. Символы `auditTokenRE`, `auditAuthRE`, `auditCredRE` в `:282-284` и `redactAuditString` в `:292` применяют только эти три шаблона. Шаблона `[0-9]+:[A-Za-z0-9_-]{35}` здесь нет, хотя он есть в `internal/infra/store/redaction.go:152` (`telegramTokenRE`, вызов в `:171`) и в `internal/infra/observe/log.go:84` (`telegramTokenRE`, вызов в `:98`). Комментарий о равенстве фильтров не соответствует коду.

Риск: токен бота в `Actor`, `CommandID`, `Action` или `Ingress` проходит в экспорт аудита, тогда как логи и SQLite его маскируют. Расхождение копий фильтра отмечено в `docs/research/security-audit.md`, но конкретно аудит не проверялся.

### SE-03. Драйвер sqlite-redact подключён только к одному открытию БД

PR #1470 заменил открытие в `cmd/beacon/mcp.go:707` на `store.OpenProduction`. Остальные производственные открытия того же файла `beacon.db` остались на `store.Open`: `cmd/beacon/spawn.go:74` (реестр сессий), `:281` (хранилище ACP-запусков), `:436` (runtime_stats) и `cmd/beacon/task.go:304` (`beacon task wait`). `Open` в `internal/infra/store/db.go:108` использует обычный `driverName`, `OpenProduction` в `:118` использует `redactingDriverName`.

Без драйвера остаются привязки без явного `Redact`: `internal/infra/store/acp_runs.go:114` пишет `result`, `internal/infra/store/blockers.go:449` пишет `payload` runtime_stats, `internal/infra/store/sessions.go:92` пишет `metadata` сессии. Символ `e.Payload` в `cmd/beacon/task.go:319` печатает сохранённое событие без выходного редактирования.

Риск: результат ACP или метаданные сессии с токеном попадают в SQLite в открытом виде. AUD-04 закрыл только долгоживущий процесс MCP.

Оговорка: часть путей записи в `internal/infra/store/db.go` вызывает `Redact` явно, поэтому не каждая запись из этих открытий незащищена. Разрыв в том, что гарантия драйвера перестала действовать для трёх других открытий.

## P2

### SE-04. Логи коротких команд пишутся без редактирования и с режимом 0644

`cmd/beacon/observe.go:260` выбирает `OpenServerLog` только для `mcp` и `daemon`, `:262` выбирает `OpenCLILog` для всех остальных команд. `OpenCLILog` в `internal/infra/observe/log.go:190` возвращает `openFreshLog`, который не оборачивает файл в `redactingWriter`. Символ `os.OpenFile` в `:175` создаёт файл с режимом `0o644`, `os.MkdirAll` в `:158` создаёт каталог логов с `0o755`. Только `OpenServerLog` подключает `redactingWriter` в `:238`.

Риск: лог `beacon human`, `beacon task`, `beacon init` или `beacon inspect` не имеет фильтра редактирования и читается любым локальным пользователем. Если текст ошибки транспорта содержит токен, он остаётся в файле.

## Итог

Четыре карточки, один P2, три P1, ноль P0. Запуск кода и сетевые проверки не выполнялись.
