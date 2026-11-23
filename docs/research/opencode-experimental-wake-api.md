# Experimental API OpenCode и wake-доставка: проверка утверждения владельца

Дата: 2026-11-21. Режим только чтение. Код, конфиги и тесты не менялись. `go test`, `go build`, `beacon gates` и `tools.beacon.*` не запускались. Поиск шёл через `rg` с явными каталогами `cmd/`, `internal/`, `docs/`. Первичные источники OpenCode получены через Context7 и страницы `opencode.ai`.

## Короткий ответ

Утверждение владельца подтверждается частично.

Верная часть. В OpenCode 2.x есть встроенный доступ к общему фоновому серверу без явного порта и пароля. Это команда `opencode api` и SDK-метод `Service.discover()`. Оба читают registration-файл `$XDG_STATE_HOME/opencode/service.json`. В файле лежат `url`, `pid`, `version` и пароль. Beacon не обязан поднимать свой `opencode serve` ради wake.

Неточности в утверждении.

Речь не об отдельном daemon с фиксированным сокетом. Фоновый сервер слушает TCP на loopback. Адрес берётся из registration-файла. Канал OpenCode на Linux использует порт 49374, это совпадает с локальным файлом, но хардкодить его нельзя. Команда `opencode service set port` меняет значение.

Слово experimental относится к HTTP API OpenCode и к маршрутам `/api/experimental/*`. Это не особый режим доставки wake.

`opencode api` может сам запустить фоновый сервис, если здорового сервиса нет. Это запуск OpenCode, а не спавн Beacon. Утверждение «не поднимает сервер» верно только при живом сервисе.

Порт для wake всё же используется. Убирается не порт как таковой, а выделение порта и управление процессом со стороны Beacon.

## 1. Что именно называется experimental API

Три вещи подходят под эту формулировку. Они описаны ниже с доказательствами.

### HTTP API как experimental surface

Страница https://opencode.ai/docs/api/ открывается фразой «Experimental HttpApi surface for selected instance routes». Документ описывает 173 операции и 518 схем из OpenAPI 3.1. Среди операций есть те, что нужны wake:

- `POST /api/session/{sessionID}/prompt` Send message
- `GET /api/session` List sessions
- `POST /api/session/{sessionID}/interrupt` Interrupt session execution
- `PATCH /api/session/{sessionID}/inbox/{inboxID}` Update inbox item
- `GET /api/event` Subscribe to events
- `GET /api/experimental/session/{sessionID}/wait` Wait for session

Маршруты с префиксом `/api/experimental/` помечены как experimental внутри этого API. К доставке wake отношения не имеют. Например, `POST /api/experimental/fs/watch` из `evals/main.go:3395` следит за файлами, а не за сессиями.

### Команда `opencode api`

Первичный источник, `packages/core/src/plugin/skill/opencode.md`, найден через Context7:

> Use the `opencode api` command. It uses the same discovery and authentication flow as the TUI and may start the background service when no compatible healthy service is available. It accepts either an HTTP method and path or an OpenAPI operation ID.

Там же сказано, что в Code Mode сначала идут встроенные инструменты `tools.opencode.*`, которые сами разбирают discovery и авторизацию. Проверка через `search({ namespace: "opencode" })` в этой сессии показала только `models`, `session_move`, `session_rename`, `worktree_*`, `list_mcp_resources` и `read_mcp_resource`. Инструмента отправки промпта в сессию среди них нет. Для Go-процесса Beacon этот namespace недоступен.

Синтаксис зафиксирован в локальных ADR: `docs/adrs/0095-cli-opencode-api.md:11` и `docs/adrs/0194-cli-opencode-api.md:11`. Оба описывают `opencode api [flags] <operation | method path...>` с флагами `--data`, `--header`, `--param`, `--server`.

### Discovery фонового сервиса

Первичный источник, `packages/client/src/effect/service.ts`, через Context7:

```typescript
/** Discover a healthy, compatible local service without starting one. */
export const discover = Effect.fn("service.discover")(function* (options: DiscoverOptions = {}) {
  const found = (yield* registered(options.file)).service
  if (found?.state !== "ready") return undefined
  if (!found.compatible) return undefined
  if (!matchesVersion(found.version, options)) return undefined
  return found.endpoint
})
```

В том же источнике указано, что registration-файл хранит пароль для Basic-авторизации. `service.discover()` только читает файл и делает health-probe. Спавн делает `service.ensure()`.

Путь файла из `packages/client/src/service-probe.ts`:

```typescript
export function fallback() {
  return join(process.env["XDG_STATE_HOME"] ?? join(homedir(), ".local", "state"), "opencode", "service.json")
}
```

Локальный файл `/home/nbook/.local/state/opencode/service.json` прочитан напрямую и содержит `version: 2.3.2`, `url: http://127.0.0.1:49374`, `pid: 3047150` и пароль. Тот же факт зафиксирован в `docs/research/process-leak-mechanism.md:54`.

Файл `/home/nbook/.config/opencode/cli.json:16-19` содержит experimental-ключи `new-designs` и `client-mcp`. Они относятся к интерфейсу и client MCP, к wake не относятся.

## 2. Как обратиться к фоновому серверу по умолчанию

Способ есть. Он включён по умолчанию в OpenCode 2.x. Страница https://opencode.ai/docs/cli/ в разделе Background service говорит:

> By default, OpenCode discovers or starts one shared background server for your user account. Every local OpenCode client connects to that server.

Практические детали:

- `opencode api POST /api/session/<id>/prompt --data '{"text":"..."}'` находит сервер и авторизуется сама. `--server` нужен только для явного выбора другого адреса. Это следует из `docs/adrs/0095-cli-opencode-api.md:15`.
- Адрес печатает `opencode service status`. Первичный источник `services/site/src/docs/content/cli/web.mdx` через Context7 называет порт 49374 для macOS, Linux и WSL и 28894 для Windows.
- Порт и пароль меняются командами `opencode service set port ...` и `opencode service set password ...` из того же источника. Поэтому приложение должно читать registration-файл, а не константу.
- Общий сервис выключается командой `opencode service set disabled true`, включается обратно через `opencode service unset disabled`. Это же страница CLI docs. `--standalone` даёт приватный сервер для одного вызова.
- Unix-сокета для этого механизма нет. Транспорт HTTP по TCP loopback.

Отдельный вопрос про запуск при отсутствии сервиса. Документация OpenCode прямо говорит, что `opencode api` «may start the background service when no compatible healthy service is available». То есть даже этот путь может поднять процесс. Разница в том, что процессом управляет OpenCode и он регистрируется в том же `service.json`. Beacon не выделяет для него порт через `net.Listen(:0)` и не пишет свой `opencode-serve.json`.

## 3. Сопоставление с wake-доставкой Beacon

Текущий путь доставки уже содержит нужный механизм как фолбэк.

| Файл и строка | Что делает |
| --- | --- |
| `internal/infra/wake/deliver.go:28-29` | Пакет прямо называет `opencode api` путём, которым пользуются CLI и TUI. |
| `internal/infra/wake/deliver.go:31-36` | `PromptFunc` определён как порт записи в живую сессию. |
| `internal/infra/wake/deliver.go:297-309` | `PromptFunc` пробуется первым. Ошибка `ErrNoPromptPort` ведёт в CLI-фолбэк. |
| `internal/infra/wake/deliver.go:316-343` | `deliverCLI` вызывает `opencode api POST /api/session/<id>/prompt` с таймаутом 10 с. |
| `internal/infra/wake/deliver.go:842-874` | `T202API` целиком построен на `opencode api` и поддерживает prompt с `resume`. |
| `internal/infra/opencode/client.go:47-81` | Тонкий HTTP-клиент читает `url` и `password` из `$XDG_STATE_HOME/opencode/service.json`. |
| `internal/infra/opencode/server.go:101-128` | `startServeProcess` собирает `opencode serve --hostname ... --port ...` и ставит `Pdeathsig`. |
| `internal/infra/opencode/server.go:387-429` | `EnsureServer` сначала берёт запись своего сервера с живым PID, потом `service.json`, и только потом спавнит. |
| `cmd/beacon/opencode.go:99-107` | `resolveOpencodeConfig` выбирает установленный ensurer, иначе `ReadConfig`. |
| `cmd/beacon/opencode.go:113-129` | `newOpencodePort` строит клиент и ставит `SetServerEnsurer(opencode.EnsureServer)`. |
| `cmd/beacon/mcp.go:1516-1542` | `opencodeWakePorts` вызывает `newOpencodePort` на строке 1517 и возвращает `nil, nil` при ошибке. |
| `cmd/beacon/mcp.go:1547-1556` | `reconcileWaker` регистрирует `OpenCodeDeliverer` с этими портами. |
| `cmd/beacon/main.go:127-137` | `wireOpenCodePorts` вызывает `newOpencodePort` при старте процесса. |

Цепочка спавна описана в `docs/research/mcp-q1-startup.md:51-59`. Старт `beacon mcp` доходит до `newOpencodePort()` ещё до первого `tools/call`. Ensurer становится не-nil после успешного вызова в `main.go`. Если после этого `service.json` исчезает, следующий вызов на `mcp.go:1517` уходит в `EnsureServer` и поднимает `opencode serve`. Это и есть постоянная смена процессов из `docs/research/process-leak-mechanism.md:56`.

`cmd/beacon/deliver.go` использует другой порт. Команда берёт `deliverTarget`, который ставится через `SetSessionCreator` в `cmd/beacon/opencode.go:85-93`. У этой команды нет собственного `opencode api`-фолбэка. Если на старте нет `service.json`, `wireOpenCodePorts` возвращается без установки порта и `beacon deliver` печатает unsupported по `deliver.go:220-227`.

## 4. Можно ли заменить newOpencodePort и EnsureServer

Для wake да. Удалять `EnsureServer` глобально не стоит. Он нужен не только в wake-пути, а также обслуживает spawn, watch, `beacon deliver` и registry sync через общую проводку в `main.go`. Замена затрагивает другие команды, если делать её на уровне `resolveOpencodeConfig`.

Точечный вариант уже спроектирован в `docs/research/mcp-lazy-port-design.md:51-57` и `:90-112`. Он называется вариант A и рекомендован там же. Суть:

1. Добавить в `cmd/beacon/opencode.go` конструктор `newReadOnlyOpencodePort()`, который вызывает только `opencode.ReadConfig()` и `opencode.New(cfg, opencodeHTTPDoer)`. Он не трогает `SetServerEnsurer` и не вызывает `resolveOpencodeConfig`.
2. В `cmd/beacon/mcp.go:1517` заменить `newOpencodePort()` на `newReadOnlyOpencodePort()`.
3. Оставить прежнее поведение при ошибке без изменений. Функция вернёт `nil, nil`, `OpenCodeDeliverer` пойдёт в `deliverCLI`, то есть в `opencode api`.

После такой правки путь wake никогда не достигнет `EnsureServer`. Проверка по тексту описана в `docs/research/mcp-lazy-port-design.md:116-125`.

Более радикальный вариант: убрать `PromptFunc` и `SessionActiveFunc` из wake и всегда ходить через `T202API` или `deliverCLI`. Он убирает HTTP-клиент из доставки полностью, но требует заменить проверку активности сессии на `opencode api session.list`. ADR 0060 отклонял `opencode api` как единственный транспорт по причине ограниченности обёртки (`docs/adrs/0060-cli-adapter-boundary-and-live-loop.md:11`, `:67`). Ограничение важно для SSE и списка агентов, а не для самого wake. Для watch и live source тонкий HTTP-клиент всё равно нужен.

Если фоновый сервис выключен через `opencode service set disabled true`, read-only конструктор не найдёт конфиг и wake уйдёт в CLI. Дальнейшее поведение зависит от политики OpenCode по автозапуску. Честный отказ без доставки лучше, чем спавн сервера, если цель владельца именно в отсутствии новых процессов.

## 5. Вердикт по утверждению владельца

Подтверждается по существу. Механизм есть, он встроен в OpenCode 2.x и уже используется в wake как CLI-фолбэк. Конкретный механизм это discovery registration-файла `$XDG_STATE_HOME/opencode/service.json` плюс команда `opencode api`, которая берёт оттуда адрес и пароль. Это убирает необходимость в спавне `opencode serve` из wake-пути при условии, что фоновый сервис жив.

Фразу «порт не нужен вообще» нельзя понимать буквально. TCP-порт остаётся, просто его больше не выделяет Beacon. Слово experimental относится к API OpenCode, а не к отдельному wake-механизму. Вероятнее всего владелец имел в виду один из трёх источников: вывод `docs/design/opencode-cli-manager.md:21` об использовании существующего сервиса и его registration-файла, готовую команду `opencode api` из `docs/design/opencode-cli-manager.md:13`, или уже реализованный фолбэк в `internal/infra/wake/deliver.go:316-343`.

Практический вывод для правки: менять `internal/infra/opencode/server.go` не нужно. Достаточно сделать wake-порт read-only по варианту A из `docs/research/mcp-lazy-port-design.md`.

## Источники

Локальные файлы:

- `/home/nbook/.local/state/opencode/service.json`
- `/home/nbook/.config/opencode/cli.json:16-19`
- `internal/infra/wake/deliver.go:28-36`, `:297-343`, `:842-874`
- `internal/infra/opencode/client.go:47-81`, `:107-133`
- `internal/infra/opencode/server.go:101-128`, `:193-201`, `:387-429`
- `cmd/beacon/opencode.go:99-129`
- `cmd/beacon/mcp.go:1516-1556`
- `cmd/beacon/main.go:127-137`
- `cmd/beacon/deliver.go:220-227`
- `docs/design/opencode-cli-manager.md:9-21`, `:109-111`
- `docs/research/mcp-q1-startup.md:51-59`
- `docs/research/mcp-lazy-port-design.md:51-57`, `:90-112`
- `docs/research/process-leak-mechanism.md:46-56`
- `docs/adrs/0060-cli-adapter-boundary-and-live-loop.md:11`, `:19`, `:41`, `:67`
- `docs/adrs/0092-background-server-source.md:14`, `:22`, `:32`
- `docs/adrs/0095-cli-opencode-api.md:11`, `:15`

Первичные источники OpenCode через Context7:

- https://github.com/anomalyco/opencode/blob/dev/packages/client/src/effect/service.ts
- https://github.com/anomalyco/opencode/blob/dev/packages/client/src/service-probe.ts
- https://github.com/anomalyco/opencode/blob/dev/packages/core/src/plugin/skill/opencode.md
- https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/content/cli/index.mdx
- https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/content/cli/web.mdx
- https://github.com/anomalyco/opencode/blob/dev/services/site/src/docs/content/troubleshooting.mdx

Страницы документации:

- https://opencode.ai/docs/cli/ раздел Background service
- https://opencode.ai/docs/api/
