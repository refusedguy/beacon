# E2E-прогон Beacon на реальных процессах

## Команда

```bash
bash tests/e2e/run.sh
```

Эквивалентно:

```bash
/tmp/opencode/go126/bin/go test -tags=e2e ./tests/e2e -count=1 -v
```

Файл `e2e_test.go` имеет build tag `//go:build e2e`. Обычный `go test ./...` пропускает его, поэтому в CI без opencode корневой тест не падает.

Токен GitHub не нужен: тест не делает запросов к GitHub. Владелец запускает локально. В CI тот же вызов живёт в отдельной job `e2e` в `.github/workflows/ci.yml` с `continue-on-error: true`: пока прогон не стабилен, он не блокирует merge. Job ставит Go из `go.mod`, Node 26.x и бинарник opencode v2.3.0 из платформенного npm tarball.

## Три инструмента, и никаких самописных клиентов

| Что проверяется | Инструмент |
| --- | --- |
| MCP `initialize`, `tools/list`, `tools/call` | официальный `github.com/modelcontextprotocol/go-sdk/mcp` клиент с `CommandTransport`: бинарник поднимается как subprocess, client сам ведёт handshake по stdio |
| `/api/plugin` и `/api/session` живого сервера | `opencode api --server http://127.0.0.1:PORT GET /api/plugin` и остальные вызовы того же CLI; пароль передаётся через `OPENCODE_PASSWORD` |
| `/api/event` | тот же `opencode api ... GET /api/event` запускается и проверяется на факт `session.execution.started`, но в v2.3.0 CLI не отдаёт тело SSE (0 байт), поэтому это ограничение инструмента фиксируется отдельной строкой лога и не выдаётся за пройденную проверку |
| составитель протокола | MCP Inspector CLI `npx --yes @modelcontextprotocol/inspector@2.5.0 --cli <binary> --method initialize --format json`, опционально |

Самописного JSON-RPC-клиента, `curl` и `fetch` в e2e нет. Inspector в этой среде за 60 с вывода не дал, поэтому по умолчанию пропускается; включение: `BEACON_E2E_INSPECTOR=1 bash tests/e2e/run.sh`. Основной MCP-клиент — go-sdk, а не Inspector.

## Что запускается по-настоящему

| Проверка | Внешний источник факта |
| --- | --- |
| `beacon mcp` отвечает на `initialize`, отдаёт ровно 7 инструментов и отвечает на `tools/call` | go-sdk `CommandTransport`, отдельный процесс |
| opencode v2.3.0 грузит плагин | `GET /api/plugin` живого `opencode serve`: `source.type=local`, `features.server=true`, `state.status=active` |
| указатель в `events.jsonl` будит сессию | `prompt.ok delivery=steer` в логе плагина и `session.execution.started` в подписке плагина (`ctx.event.subscribe` внутри живого opencode) |
| квитанция записана | `state/beacon/receipts/<id>.wrote` и строка `state=posted` в `receipts.jsonl` |
| негативный контроль | вместо рабочего обработчика ставится `fixtures/beacon-bridge-stub.js`; та же проверка обязана провалиться |

## Честные ограничения

1. **Плагин из `fixtures` — эталон, не плагин репозитория.** `tests/e2e/fixtures/beacon-bridge.js` скопирован из рабочего спайка `tmp/p0-wake/proj/.opencode/plugins/beacon-bridge.js` (T0.3). Файл `.opencode/plugins/beacon-bridge.js` в репозитории не менялся. Прогон доказывает, что реальный opencode грузит плагин и будит сессию, но **не доказывает**, что репозиторийский плагин уже умеет будить. Сейчас он каркас: подписан на `session.execution.started` и не вызывает `ctx.session.prompt`. Тест `repo_plugin_skeleton` печатает этот факт как диагностику. Это блокер для проверки шага (e) на продакшн-плагине.
2. **Семь инструментов — контракт, не поведение.** `NewServer` в `internal/infra/mcp/server.go` регистрирует `stubHandler` для всех семи инструментов. `tools/call beacon_list_running` проверяет только протокольный round trip. Реальные обработчики (`AttachWatchPR` и др.) нигде не собираются в точку входа.
3. **`beacon mcp` сейчас уже в main** (PR #40 влит). Тест собирает бинарник из корня репозитория и объясняет это в логе. Переменная `BEACON_MCP_SRC` позволяет указать другой checkout. Если `cmd/beacon/mcp.go` нигде нет, тест падает с явным сообщением.
4. **`opencode api` не умеет читать SSE.** Проверено на живом сервере 2.3.0: `opencode api GET /api/event` за 7 с вернул 0 байт, тогда как прямой поток отдавал `server.connected` и heartbeat. Заглушки здесь нет: тест запускает CLI, фиксирует отсутствие кадров отдельной строкой лога и не считает SSE доказанным. Факт пробуждения доказывается подпиской плагина, квитанцией и `prompt.ok`.

## Переменные окружения

| Переменная | Назначение | Значение по умолчанию |
| --- | --- | --- |
| `GO_BIN` | Go 1.26 для сборки beacon и запуска теста | `/tmp/opencode/go126/bin/go`, иначе `go` из PATH |
| `BEACON_MCP_SRC` | checkout с `cmd/beacon/mcp.go` | `worktrees/w43`, затем корень репозитория |
| `OPENCODE_BIN` | бинарник opencode | `opencode` из PATH |
| `OPENCODE_EXPECT_VERSION` | подстрока ожидаемой версии | `2.3.0` |
| `OPENCODE_SKIP_VERSION` | `1` отключает проверку версии | не задана |
| `BEACON_E2E_INSPECTOR` | `1` включает опциональный Inspector | не задана, Inspector пропускается |

Изоляция: каждый прогон создаёт каталог `/tmp/opencode/beacon-e2e-*` с отдельными `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME`, `XDG_STATE_HOME` и `OPENCODE_CONFIG`; во временном проекте делается `git init`, без чего opencode не считает его проектом и не грузит плагин. Настоящий `auth.json` копируется в изолированный data-каталог, чтобы `session.prompt` мог обратиться к провайдеру; содержимое файла в лог и в репозиторий не попадает. В конфиге выставлен `experimental.location_idle_timeout: false`, иначе плагин выгружается после простоя.

## Негативный контроль

Автоматический тест `negative_control_stub_handler_must_fail` ставит `fixtures/beacon-bridge-stub.js` вместо рабочего плагина и требует, чтобы та же самая функция проверки, которая зелёная на эталоне, вернула ошибку. Дополнительно он требует отсутствия квитанции и prompt. Если заглушка проходит проверку, прогон красный и e2e не считается рабочим.

## Что не проверяется

- Реальный GitHub, изменение статуса CI, демон опроса и вычисление вердикта.
- Поведение обработчиков MCP: в main они заглушки.
- Работоспособность репозиторийского адаптера: он ещё каркас.
- Ответ модели. Для факта пробуждения достаточно `session.execution.started`; провайдер в изолированном окружении может отвечать ошибкой, как зафиксировано в `tmp/p0-wake/Results.md`.
