# Механизм утечки процессов `opencode serve`

Отчёт подготовлен 2026-11-21. Режим только чтение. Код, конфиги и процессы не менялись. `tools.beacon.*` не вызывались, `go test`, `go build` и `beacon gates` не запускались. Поиск шёл через `rg` с явными каталогами.

Этот разбор независим от `docs/research/process-leak-rootcause.md`. Где выводы совпадают, ссылки идут на конкретные строки. Где текущее дерево расходится с тем отчётом, расхождение помечено явно.

## Что прочитано

`/home/nbook/.config/opencode/opencode.jsonc`, `cli.json`, `service.json`, `AGENTS.md`, `package.json`, плагин `plugins/herdr-agent-state.js`, агент `agents/comment-sicko.md`, `skills/research/SKILL.md`, `/mnt/projects/beacon-handoff/opencode.json`, `beacon-impl/AGENTS.md`, `beacon-impl/.opencode/settings.json`, плагины `beacon-bridge.js` и `beacon-project/*`, агенты `issue-scout.md`, скиллы `night-shift` и `red-verdict-triage`, `internal/infra/opencode/server.go`, `cmd/beacon/opencode.go`, `cmd/beacon/mcp.go`, `cmd/beacon/gc.go`, `cmd/beacon/main.go`, SDK в `node_modules/@opencode-ai/sdk/dist`, бинарник CLI. Документация OpenCode открыта через context7 для версии 2.3.2.

## Пять находок

### 1. Beacon MCP подключён дважды и по умолчанию идёт через Code Mode

Глобальный `~/.config/opencode/opencode.jsonc:3-14` объявляет только удалённый сервер `context7`. Проектный `/mnt/projects/beacon-handoff/opencode.json:3-14` объявляет локальный сервер `beacon` с командой `.opencode/bin/beacon mcp` и `cwd` равным `beacon-impl`.

Плагин регистрирует тот же сервер второй раз. `.opencode/plugins/beacon-project/mcp.js:9-19` возвращает конфиг с `disabled: false`, а `registerBeaconMcp` в строках 25-46 применяет его через `ctx.mcp.transform` и вызывает `ctx.mcp.reload()` при смене корня. Точка входа `.opencode/plugins/beacon-project/index.js:13-20` делает это для каждой сессии, у которой есть корень проекта. Условия выхода нет только для сессий без `ctx.location`.

CLI имеет версию 2.3.2. В v2 инструменты MCP по умолчанию регистрируются с `codemode: true`, то есть попадают в каталог инструмента `execute`, а не в прямой список. `beacon-impl/AGENTS.md:95` прямо учит агентов пути `search({ query: "beacon", namespace: "beacon" })` и затем `tools.beacon.<name>` через `execute`. Ни в одном конфиге нет флага `codemode: false` для beacon.

Следствие: каждый research-агент в сессии с корнем проекта получает восемь инструментов beacon внутри `execute`. Ограничений по роли нет.

### 2. `serve` спавнит Go-часть Beacon, а не Code Mode runtime и не SDK

Цепочка подтверждается кодом.

`cmd/beacon/mcp.go:1547-1555` при создании wake-реестра вызывает `opencodeWakePorts()`. Функция в строках 1516-1521 вызывает `newOpencodePort()`. Тот в `cmd/beacon/opencode.go:118-128` вызывает `resolveOpencodeConfig()`. В строках 102-106 вызван `installedServerEnsurer.EnsureServer`, а в строке 128 в качестве энсюрера ставится `opencode.EnsureServer`.

`internal/infra/opencode/server.go:101-128` собирает команду:

```go
args := []string{"serve", "--hostname", req.host, "--port", strconv.Itoa(req.port)}
cmd := exec.Command("opencode", args...)
```

Порт берётся из `freePort()` в строках 130-143. Это короткий `net.Listen("tcp", "127.0.0.1:0")`, отсюда случайный номер порта. Пароль уходит через `OPENCODE_SERVER_PASSWORD`.

Так как новый `beacon mcp` делает это при старте, каждый старт сервера MCP пытается получить HTTP API OpenCode. Code Mode поднимает каталог инструментов MCP, поэтому число стартов `beacon mcp` совпадает с числом вызовов `tools.beacon.*`, которые сообщил владелец. Это объясняет пары `opencode serve` плюс дочерний `.opencode/bin/beacon mcp`: спавненный `serve` читает проектный `opencode.json` и сам регистрирует тот же локальный MCP-сервер как stdio-ребёнка.

Второй путь существует, но не является действующим путём в этой установке. SDK в `~/.config/opencode/node_modules/@opencode-ai/sdk/dist/server.js:3-17` тоже умеет `createOpencodeServer` и спавнит ту же команду с портом по умолчанию 4096. Таймаут старта 5000 мс, `close()` вызывает `stop(proc)`, который делает `proc.kill()` в `dist/process.js:4-13`. Но бинарник CLI 2.3.2 не содержит строк `createOpencode` или `opencode serve`, а `WORKER-PROMPT.md:22` прямо помечает локальный npm 1.18.4 как V1-заглушку, которую нельзя использовать как SDK. Обвинять этот путь без отдельного доказательства не стоит.

### 3. Процесс остаётся сиротой из-за отсутствия привязки к родителю, но в текущем дереве исправление уже есть

В версии, которую изучал diag1, `startServer` не ставил `Pdeathsig`, не писал PID и не проверял живой сервер перед повторным спавном. Каждый старт `beacon mcp` создавал новый `serve`, а смерть родителя оставляла процесс с родителем PID 1. Это объясняет 20 серверов за два часа и 29 сирот без живого `beacon mcp` в более раннем инциденте.

Текущий checkout находится на коммите `2a3362f1`. В нём уже применён PR 887 (`432a77e7`, `fix(opencode): reuse live serve and gc orphan processes (Q-A-02)`). Проверено в коде:

- `server.go:118` ставит `SysProcAttr{Pdeathsig: syscall.SIGTERM}`.
- `server.go:193-269` пишет `opencode-serve.json` с URL, паролем и PID, режим 0600.
- `server.go:390-429` сначала переиспользует запись, если PID жив, потом читает `service.json`, и только потом спавнит.
- `cmd/beacon/main.go:274` подключает `gc` к dispatch, а `cmd/beacon/gc.go:39-41` регистрирует сборщик serve.
- `cmd/beacon/gc.go:53-95` защищает PID и порт из `service.json` и из собственной записи, остальные помечает как `orphan`.

Проверено на диске: `/home/nbook/.local/state/opencode/service.json` содержит `pid: 3047150` и `url: http://127.0.0.1:49374`, поэтому защита основного сервера работает. Файла `$XDG_STATE_HOME/beacon/opencode-serve.json` сейчас нет.

Итог по механизму: утечка вызвана не движком Code Mode и не SDK, а тем, что `beacon mcp` при старте спавнит `opencode serve` для wake-деливери. В старом коде это приводило к сиротам. В текущем коде сироты должны умереть по `Pdeathsig`, но churn остаётся: после смерти предыдущего `beacon mcp` его `serve` умирает, а следующий старт поднимает новый.

### 4. У OpenCode v2 есть готовые выключатели, они нигде не применены

Документация v2 и код ядра подтверждают четыре рычага.

- `{ "action": "execute", "resource": "*", "effect": "deny" }` полностью убирает инструмент `execute`. В `packages/core/src/tool.ts` проверка `whollyDisabled("execute", rules)` отключает Code Mode. Инструменты MCP при этом становятся прямыми, поэтому их надо запрещать отдельно.
- `{ "action": "beacon_*", "resource": "*", "effect": "deny" }` запрещает все инструменты сервера beacon по имени действия `<server>_<tool>`. Можно запретить и один инструмент, например `beacon_watch_pr`.
- `codemode: false` в конфиге сервера заставляет его инструменты идти прямым списком. Это не запрет, а смена режима.
- Настоящий hook v2 это `ctx.tool.hook("execute.before", ...)` и `ctx.permission.hook("evaluate", ...)`. Явный `deny` из конфига final, hook для него не вызывается. Запрет через hook ставится в плагине.

Файл `.opencode/settings.json:3-16` содержит `PreToolUse` с `matcher`, `handler` и `permissionDecision: deny`. Этот файл содержит контракт адаптера и тесты Go. Нет доказательства, что OpenCode 2.3.2 читает этот файл как свои hooks. diag1 предлагал дописать туда запрет `grep -r`. Такой запрет, скорее всего, не сработает. Новый запрет надо писать плагином OpenCode.

### 5. research-агент получает те же права, что и исполнитель

`.opencode/agents/issue-scout.md:1-14` уже объявляет `mode: subagent` и действия `edit`, `shell`, `write` с `effect: deny`. Это правильная форма v2. Но в списке нет `execute` и `beacon_*`. Агент не может запустить shell, однако может вызвать `execute` и через него `tools.beacon.*`. Это и есть дыра, через которую research-агент запускает описанную выше цепочку.

Глобальный `skills/research/SKILL.md` состоит из 12 строк и предписывает поднять background agent, не ограничивая его инструменты. Скилл `night-shift/SKILL.md` тоже не задаёт профиль агента. Правила выживания в `beacon-project/context.js:13-24` содержат десять пунктов, но ничего про `rg`, цену вызовов beacon или локальные гейты. Они пишутся только в сессии, прошедшие `belongsToLocation` из строк 32-41, поэтому worktree вне канонического корня их не получает.

Глобальный `AGENTS.md` и проектный `AGENTS.md` запрещают локальные `go test`, `go build` и `beacon gates` текстом в `beacon-impl/AGENTS.md:10` и в правиле 2 шпаргалки. Принуждения на границе инструмента нет.

## Меры по порядку эффективности

### (a) Текст в AGENTS.md и скиллах

| Где | Что записать | Effort | Риск |
| --- | --- | --- | --- |
| `/home/nbook/.config/opencode/AGENTS.md`, таблица «Writing style» или новая таблица «Поиск и гейты» | Строки: `grep -r`, `grep -R`, `find /` запрещены; вместо них `rg <шаблон> <явный каталог> --glob <фильтр>`; локальные `go test ./...`, `go build ./...`, `beacon gates` не запускать; research-роль не вызывает `tools.beacon.*` | Низкий | Низкий. Текст не принуждает, но закрывает поведение агента копированием примеров |
| `beacon-impl/AGENTS.md`, раздел «Запреты» | Те же строки плюс: перед вызовом `tools.beacon.*` объединить все чтения в один проход; новый агент на сигнал `wave item done` только при новом headSHA или новом вердикте | Низкий | Низкий |
| `beacon-impl/.opencode/plugins/beacon-project/context.js:13-24` | Правила 11, 12, 13 с тем же смыслом. Это единственный текст, который агент перечитывает после сжатия | Низкий | Низкий. Правила по-прежнему не долетают до сессий вне `belongsToLocation` |
| `skills/research/SKILL.md` | Явная строка: research использует `read`, `grep`, `glob`, `webfetch`; не вызывает `execute`, `tools.beacon.*`, `go test`, `go build`, `beacon gates`; background-агент запускается только профилем `beacon-researcher` | Низкий | Низкий |
| `night-shift/SKILL.md` | Требование указывать `agent: beacon-researcher` при спавне research-агента | Низкий | Средний. Если оркестратор спавнит без имени агента, ограничение не применится |

### (b) Отдать research-агентам shell без Code Mode через конфиг

Это единственная мера, которая убирает саму возможность вызвать `tools.beacon.*`.

Создать профиль в `/home/nbook/.config/opencode/agents/beacon-researcher.md` или в `beacon-impl/.opencode/agents/`:

```yaml
---
description: Read-only research agent without Code Mode and Beacon tools
mode: subagent
permissions:
  - action: edit
    resource: "*"
    effect: deny
  - action: shell
    resource: "*"
    effect: deny
  - action: execute
    resource: "*"
    effect: deny
  - action: beacon_*
    resource: "*"
    effect: deny
  - action: subagent
    resource: "*"
    effect: deny
  - action: webfetch
    resource: "*"
    effect: allow
---
```

Затем скиллы `research` и `night-shift` должны запускать background-агента только с профилем `beacon-researcher`. Важно запретить и `execute`, и `beacon_*`: при запрете только `execute` инструменты beacon станут прямыми и останутся доступны.

Где ещё записать: можно дублировать правила в `opencode.jsonc` через `agents.beacon-researcher.permissions`, но глобальный конфиг сейчас в стиле v1, а CLI v2.3.2. Перед применением стоит проверить миграцию на тестовой сессии.

Effort: средний. Риск: средний. Основной риск не в синтаксисе, а в том, что оркестратор вызовет generic `task` без указания агента. Тогда ограничение не сработает. Второй риск: агент теряет возможность читать MCP-источники, поэтому research по GitHub придётся вести через `webfetch`.

Альтернатива с меньшим риском: для research-сессий задать каталог вне `beacon-impl`. Тогда `beacon-project/index.js:17` вернёт `false` и MCP вообще не будет зарегистрирован. Минус: агент не получит проектный `AGENTS.md` и шпаргалку.

### (c) Watchdog и уборка

Новый скрипт писать не надо, он уже есть в коде. Нужно только включить регулярный запуск.

| Где | Что сделать | Effort | Риск |
| --- | --- | --- | --- |
| Crontab владельца или systemd timer | `*/10 * * * * <path>/beacon gc --apply >> <log> 2>&1`, перед включением один раз запустить без `--apply` и сверить список | Низкий | Средний. GC убивает любой `opencode serve`, которого нет в `service.json` и в `opencode-serve.json`. Если другой инструмент поднимет сервер без записи, он будет убит |
| `cmd/beacon/gc.go:53-95` | Оставить защиту по PID и порту. Сейчас `service.json` содержит и pid, и url, защита работает | Нет изменений | Низкий |
| `internal/infra/opencode/server.go:118` | Оставить `Pdeathsig`. Это главное структурное изменение, которое уже доставлено PR 887 | Нет изменений | Низкий |

Дополнительный watchdog поверх GC нужен только если в системе есть спавн серверов вне Beacon. Тогда он должен перечислять cmdline `/proc/*/cmdline`, пропускать PID и порт из `service.json` и из `opencode-serve.json`, остальных завершать SIGTERM. Начинать с dry run. Порог тревоги больше двух живых серверов.

### (d) dns/http MST bijection

Такого механизма в прочитанных конфигах и коде нет. Поиск по `docs`, `internal` и `/home/nbook/.config/opencode` дал ноль совпадений для `bijection`, `MST` и связок `dns.*http`. Единственная доступная биекция это тройка PID, порт, sessionID:

- PID и порт главного сервера лежат в `$XDG_STATE_HOME/opencode/service.json`.
- PID и URL сервера, спавненного Beacon, лежат в `$XDG_STATE_HOME/beacon/opencode-serve.json`.
- sessionID пробуждения лежит в wake-указателе плагина `wake-pointer.json`, чтение в `beacon-bridge.js:278-311`.

Effort: средний, если нужна сводная таблица для диагностики. Риск: низкий. Нужно понимать, что это корреляция по записям состояния, а не встроенный сессионный протокол. Если под MST имелось в виду что-то другое, уточните термин.

## Запрет рекурсивного grep/find и локальных гейтов на уровне hook

Текст в AGENTS.md не останавливает агента. Нужен плагин OpenCode. Формат v2 подтверждён документацией: `Plugin.define` и `ctx.tool.hook("execute.before", ...)`.

Файл: `~/.config/opencode/plugins/no-unsafe-shell.js` для всех сессий или `beacon-impl/.opencode/plugins/no-unsafe-shell.js` только для проекта.

Логика хука:

1. Проверить `event.tool`. Для v2 это имя инструмента, например `bash` или `shell`.
2. Взять команду из `event.input`. В v1-совместимой форме команда лежит во втором аргументе hooks в поле `args`.
3. Отклонить, если команда содержит `grep -r`, `grep -R`, `find /`, `go test`, `go build`, `beacon gates` как отдельную команду или как часть цепочки через `;`, `&&`, `||`, `|`.
4. Бросать ошибку с текстом, который называет замену: `Use rg with explicit directories` или `Run gates in GitHub Actions CI`.

Effort: средний. Риск: средний. Ложные срабатывания возможны на `go build` в диагностических командах и на `find /tmp/...`, который не является рекурсивным поиском по всему дереву. Перед включением стоит неделю писать предупреждение в лог плагина, а не бросать ошибку.

Дополнительно:

- В `opencode.jsonc` можно задать `permissions` для named agent с deny на `shell` и шаблоны команд. Это грубее хука, но не зависит от плагина. Нужно проверить, как v2 парсит цепочки команд.
- Убрать готовые примеры `grep -En` из карточек `docs/tasks/T5.55.md` и `docs/tasks/T3.1-opencode-bridge-plugin.md`. Агент копирует увиденное, поэтому примеры работают сильнее запрета.
- Добавить в CI проверку, которая не даёт добавлять `grep -r`, `find /` и локальные гейты в `.opencode/agents`, скиллы и шаблоны карточек. Это lint правил, а не runtime-защита.

## Что не проверено

Прямой строки лога, которая связывает конкретный вызов `tools.beacon.*` с конкретным PID сервера, нет. Вывод о цепочке опирается на код `mcp.go`, `opencode.go` и `server.go`, на число процессов, которое сообщил владелец, и на хронологию из diag1.

Не проверялось поведение v2 config миграции для `opencode.jsonc` в стиле v1. Перед тем как писать `permissions` в этот файл, нужно проверить миграцию отдельной тестовой сессией.

Не проверялось, что `PreToolUse` из `.opencode/settings.json` действительно игнорируется OpenCode 2.3.2. Вывод основан на отсутствии упоминаний этого формата в документации v2 и на том, что файл содержит тестовый контракт адаптера.

Ни один конфиг, исходник или процесс в ходе подготовки не изменён.

## Что доставлено по BCX-82

Базовый замер из отчёта владельца: около 20 процессов `opencode serve` за два часа, порог тревоги больше двух живых серверов. Исходный профиль `issue-scout` запрещал `edit`, `shell` и `write`, но не запрещал `execute` и `beacon_*`. Через `execute` research-агент получал `tools.beacon.*` и поднимал цепочку `beacon mcp` и `opencode serve`. Текст в `AGENTS.md` не останавливал рекурсивный поиск и локальные гейты.

В PR по BCX-82 сделано следующее.

- `.opencode/agents/issue-scout.md`: deny для `execute`, `beacon_*`, `subagent`, `edit`, `shell` и `write`, allow для `webfetch`. Запрет `execute` и `beacon_*` стоит вместе. При запрете только `execute` инструменты Beacon становятся прямыми и остаются доступными.
- `.opencode/plugins/beacon-project/index.js`: хук `ctx.tool.hook("execute.before")` отклоняет `grep -r`, `grep -R`, `find /`, локальные `go test`, `go build` и `beacon gates`. Сообщение называет замену: `rg` с явным каталогом или CI на PR. Шаблоны срабатывают в начале команды и после `;`, `&&`, `||`, `|`. Хук fail-open, когда хост не поддерживает `ctx.tool.hook`. Переменная `BEACON_SHELL_GUARD=warn` включает режим наблюдения без блокировки. Режим по умолчанию блокирует команду, этого требует приёмка карточки.
- `AGENTS.md`: строки в таблице запретов и раздел о границе инструмента.

В PR не сделано следующее. Скиллы `research` и `night-shift` лежат в глобальном конфиге вне репозитория и вне OwnerFiles карточки, поэтому назначение профиля `beacon-researcher` в них остаётся за владельцем. Профиль реализован как обновлённый `issue-scout`, единственный research-агент в этом репозитории. Файл `opencode.json` не тронут по условию карточки.

Проверка лежит на CI и на тестовой сессии. Сессия с профилем `issue-scout` не должна видеть `execute` и `beacon_*`. Хук должен блокировать запрещённые команды и называть замену.
