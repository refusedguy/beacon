# Сравнение с Claude Agent SDK

Дата: 2026-11-22. Метод: клонирование публичного репозитория и чтение исходного кода. Код beacon не менялся. Локально не запускались `go test`, `go build`, `beacon gates` и `batch-apply`. Ссылки на код SDK даны по коммиту `3184360` (v0.2.187, 2026-11-20), ссылки на beacon по `origin/main` коммита `2d34645b`.

## 1. Первичный источник

| Источник | Что прочитано | Коммит |
| --- | --- | --- |
| `anthropics/claude-agent-sdk-python`, `src/claude_agent_sdk/types.py` | PermissionMode, AgentDefinition, HookEvent, typed hook inputs, HookMatcher, PermissionUpdate, ClaudeAgentOptions, SessionStore, ResultMessage | 3184360 |
| `src/claude_agent_sdk/_internal/query.py` | регистрация и диспетчеризация хуков, обработка `can_use_tool`, `set_permission_mode`, initialize с агентами | 3184360 |
| `src/claude_agent_sdk/_internal/session_resume.py`, `src/claude_agent_sdk/client.py` | материализация сессии из store, применение опций, подключение | 3184360 |
| `src/claude_agent_sdk/_internal/transport/subprocess_cli.py`, `src/claude_agent_sdk/_internal/message_parser.py` | `--json-schema` из `output_format`, разбор `structured_output` | 3184360 |
| `examples/hooks.py`, `examples/agents.py`, `examples/tool_permission_callback.py` | примеры вызовов | 3184360 |

Клонирование в `/tmp/opencode/claude-sdk-research` глубиной 1 удалось. Репозиторий публичный, запасной `anthropic-sdk-python` не понадобился.

## 2. Пять приёмов SDK с file:line

### CS-01. Хуки как типизированный жизненный цикл с fail-closed решателями

`HookEvent` перечисляет события: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `SubagentStart`, `SubagentStop`, `PermissionRequest` и другие (`types.py:324-335`). Вход каждого события типизирован. `PreToolUseHookInput` несёт `tool_name`, `tool_input` и `tool_use_id` (`types.py:388-394`), `PostToolUseHookInput` добавляет `tool_response` и `duration_ms` (`types.py:397-407`). Поля `agent_id` и `agent_type` приходят через `_SubagentContextMixin` (`types.py:369-382`), чтобы различать параллельные сабзагенты на общем канале.

Выход `PreToolUse` даёт `permissionDecision` со значениями `allow`, `deny`, `ask`, `defer`, плюс `updatedInput` и `additionalContext` (`types.py:551-558`). `PostToolUse` может заменить ответ инструмента через `updatedToolOutput` (`types.py:561-576`). `HookMatcher` задаёт `matcher`, список колбэков, `timeout` и `on_failure` со значениями `continue` или `block` (`types.py:879-904`).

Регистрация идёт по `callback_id`: `Query.initialize` раскладывает матчеры и хранит колбэки в словаре (`_internal/query.py:348-372`), CLI вызывает их control-запросами `hook_callback` (`_internal/query.py:695-709`). Если матчер посылает `on_failure="block"`, а CLI не поддерживает это поле, соединение отвергается до старта (`_internal/query.py:396-409`). Это fail-closed проверка способности, а не надежда на то, что старый хост поймёт поле. Пример блокировки команды Bash лежит в `examples/hooks.py:46-70`, регистрация матчера в `examples/hooks.py:164-166`.

### CS-02. Разрешения как переключаемые режимы плюс колбэк с обновлениями правил

`PermissionMode` это `Literal` из шести значений: `default`, `acceptEdits`, `plan`, `bypassPermissions`, `dontAsk`, `auto` (`types.py:25-27`). Опция `permission_mode` описана в `ClaudeAgentOptions` (`types.py:2484-2493`). Режим меняется во время сессии control-запросом `set_permission_mode` (`_internal/query.py:851-858`), публичный метод открыт в `client.py:314-339`.

`can_use_tool` срабатывает только там, где правила CLI дали `ask` (`types.py:2619-2635`). Возвращаемый `PermissionResultAllow` несёт `updated_input` и `updated_permissions` (`types.py:294-316`). `PermissionUpdate.to_dict` выбирает область `session`, `localSettings`, `projectSettings` или `userSettings` и выбрасывает `ValueError`, если не хватает обязательного поля (`types.py:137-234`). В `_permission_updates_to_wire` все обновления применяются или ни одного (`_internal/query.py:118-140`). Обработчик control-запроса собирает `ToolPermissionContext` с `agent_id`, `blocked_path` и `decision_reason`, затем возвращает allow или deny (`_internal/query.py:640-693`). Документация прямо указывает: чтобы видеть каждый вызов инструмента, нужен `PreToolUse`, потому что allow из хука тоже пропускает колбэк (`types.py:2632-2634`).

### CS-03. Сабзагенты как данные, переданные в initialize

`AgentDefinition` это dataclass с полями `description`, `prompt`, `tools`, `model`, `skills`, `memory`, `mcpServers`, `maxTurns`, `background`, `effort` и `permissionMode` (`types.py:107-126`). Опция `agents` принимает словарь имя к определению (`types.py:2753-2757`). `ClaudeSDKClient` превращает каждое определение через `asdict`, отбрасывая `None` (`client.py:197-203`), и передаёт словарь в `Query` (`client.py:208-217`). `Query.initialize` кладёт его в поле `agents` запроса (`_internal/query.py:379-380`). Команды и большие конфиги поэтому не пишутся в аргументы процесса.

Атрибуция идёт по `agent_id` и `agent_type` в хуках жизненного цикла инструментов (`types.py:369-382`). Опция `forward_subagent_text` включает пересылку текста и рассуждений сабзагента с тем же `parent_tool_use_id` (`types.py:2669-2679`). Примеры определений `code-reviewer` и `analyzer` лежат в `examples/agents.py:28-39` и `examples/agents.py:87-100`.

### CS-04. Возобновление сессии через внешний store с материализацией в temp dir

Опции `resume` и `session_id` задают идентификатор сессии (`types.py:2499-2507`). `fork_session`, `resume_session_at` и `resume_drops_turn` позволяют ответвить диалог и проверить, что отбрасываемый диапазон принадлежит одному turn (`types.py:2711-2751`). Store задаётся полями `session_store`, `session_store_flush` и `load_timeout_ms` (`types.py:2866-2891`).

`materialize_resume_session` загружает записи из store, проверяет `resume` как UUID, чтобы исключить обход пути, пишет JSONL во временный `CLAUDE_CONFIG_DIR`, копирует учётные данные и восстанавливает транскрипты сабзагентов через `list_subkeys` (`_internal/session_resume.py:130-200`). `ClaudeSDKClient.connect` вызывает его до запуска процесса (`client.py:117-129`) и применяет материализованные опции в `_connect_inner` (`client.py:156-157`). Ошибка после `mkdtemp` удаляет каталог через `BaseException`, чтобы отмена не оставляла учётные данные на диске (`_internal/session_resume.py:183-191`).

### CS-05. Структурированный вывод, связанный с флагом рантайма

`output_format` принимает структуру Messages API вида `{"type": "json_schema", "schema": {...}}` (`types.py:2850-2856`). Транспорт извлекает схему и передаёт её как `--json-schema` (`_internal/transport/subprocess_cli.py:729-738`). Результат приходит в `ResultMessage.structured_output` (`types.py:1779`), парсер берёт его из поля `structured_output` ответа CLI (`_internal/message_parser.py:324`). Документация по `resume_drops_turn` отмечает, что при структурированном выводе turn заканчивается записями после последнего сообщения ассистента (`types.py:2748-2750`). Схема живёт в одном месте и доходит до процесса без дублирования в коде вызова.

## 3. Разрывы с beacon

| Приём | SDK, file:line | beacon по `origin/main`, file:line | Разрыв | Карточка |
| --- | --- | --- | --- | --- |
| XS-01, жизненный цикл хуков | `types.py:324-335`, `:551-576`, `:879-904`; `_internal/query.py:348-372`, `:396-409`, `:695-709` | `.opencode/plugins/beacon-project/index.js:67-82`, `:70`, `:75-76`, `:79`; `AGENTS.md:23` | Один хук `execute.before` на инструменты bash, shell и execute. Нет `PreToolUse` и `PostToolUse`, нет типизированного входа и решения `allow/deny/ask/defer`. При отсутствии `ctx.tool.hook` плагин работает fail-open (`:79`). В SDK fail-closed проверка способности стоит на `on_failure="block"` (`_internal/query.py:396-409`). | CS-01 |
| XS-02, режимы разрешений | `types.py:25-27`, `:137-234`, `:2619-2635`; `_internal/query.py:640-693`, `:851-858` | `internal/domain/types.go:246-250`; `docs/adrs/0072-autonomous-merge-loop.md:19-21`; `internal/infra/mcp/tool_ack.go:65-74`, `:121`, `:136` | `MergePolicy` описывает только мерх Watch: `autoMerge`, `requireApproval`, `waitFor`, `waitForMain`. Решения `approve`, `reject`, `blocked:not_mine` живут в `beacon_ack` и требуют человека. Нет переключаемого режима для вызовов инструментов агента и нет обновления правил с областью `session` или `projectSettings`. | CS-02 |
| XS-03, сабзагенты как данные | `types.py:107-126`, `:2753-2757`, `:369-382`; `client.py:197-203`; `_internal/query.py:379-380` | `cmd/beacon/spawn.go:148`, `:159`, `:523`; `cmd/beacon/mcp.go:2055`, `:2132`; `AGENTS.md:121-127` | Beacon спавнит агента командой с cardID, заголовком и worktree. Нет типизированного определения агента, передаваемого в ранутайм, и нет `agent_id` в событиях инструментов. `runSpawnACP` уже использует Agent Client Protocol, но это протокол запуска, а не реестр определений агента. | CS-03 |
| XS-04, возобновление сессии | `types.py:2499-2507`, `:2711-2751`, `:2866-2891`; `_internal/session_resume.py:130-200`; `client.py:117-129` | `AGENTS.md:101`, `:105-111`; `internal/domain/transition.go:55`, `:471`; `internal/infra/mcp/tool_ack.go:933` | Восстановление после смерти держится на `git status`, ключе `beacon/survival-rules` и `continueOf` на Watch с лимитом в пять раундов. Нет внешнего store транскриптов, материализации в temp dir, проверки UUID и восстановления транскриптов сабзагентов. | CS-04 |
| XS-05, структурированный вывод | `types.py:2850-2856`, `:1779`; `subprocess_cli.py:729-738`; `message_parser.py:324` | `internal/app/taskcli/service.go:321-322`, `:362`; `cmd/beacon/task.go:1021`, `:1032`; `docs/api/openapi.yaml:465-475` | `--format json` есть только у команд вывода задач. Нет схемы, обязательной для результата вызова, и нет типизированного поля структурированного результата в MCP-инструментах. openapi.yaml описывает JSON ответов, но не проверяет их по схеме. | CS-05 |

## 4. Карточки

| ID | Цель | Файл |
| --- | --- | --- |
| CS-01 | Добавить типизированные события `PreToolUse` и `PostToolUse` с fail-closed проверкой способности | `docs/tasks/CS-01.md` |
| CS-02 | Сделать режимы разрешений данными с обновлением правил на уровне сессии | `docs/tasks/CS-02.md` |
| CS-03 | Передавать определения сабзагентов в ранутайм и нести `agent_id` в событиях | `docs/tasks/CS-03.md` |
| CS-04 | Хранить транскрипты вне сессии и материализовать их при возобновлении | `docs/tasks/CS-04.md` |
| CS-05 | Связать схему вывода с результатом вызова и проверить её в CI | `docs/tasks/CS-05.md` |

## 5. Чего не нашёл

SDK не хранит карточки, не делает claim и не мержит PR. Это библиотека вызовов модели с хуками и control-протоколом. Сравнение идёт по механизмам расширения, а не по подмене оркестратора.

Публичное API не описывает вердикт по PR или проверкам CI. Ближайшее это хуки `Stop` и `SubagentStop` (`types.py:470-499`) с полями `background_tasks` и `session_crons`.

Материализация сессии описана только для store, который реализует протокол `SessionStore` (`types.py:2111`, `:2186`). Redis, S3 и Postgres адаптеры лежат в `examples/session_stores/`, в ядро SDK они не входят.

## 6. Список источников

- `/tmp/opencode/claude-sdk-research`, коммит `3184360393a92ca8a6ec7bbbdee571afd1ce1c56`, клонирован 2026-11-22 с глубиной 1. Все file:line раздела 2 и колонки SDK в разделе 3 относятся к этому коммиту.
- `https://github.com/anthropics/claude-agent-sdk-python`, прочитано 2026-11-22.
- Beacon `origin/main`, коммит `2d34645b54f45d84dba3a595350f17f8658759da`. Все file:line колонки beacon и раздела 3 относятся к этому коммиту.
- Проверки выдачи: `git grep -n 'set_permission_mode\|can_use_tool' origin/main -- internal cmd` даёт 0 строк; `git grep -n -- '--json-schema\|structured_output' origin/main -- internal cmd` даёт 0 строк; `git grep -n 'PostToolUse\|permissionDecision' origin/main -- internal cmd` даёт 0 строк, а те же слова в `docs/adrs/0035-o-claude-code-mechanics-features.md:15` и `docs/adrs/0065-trailcode-wave-au.md:17` это описание чужих практик, а не код beacon.
