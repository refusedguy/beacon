# ADR 0029. CLI-менеджер поверх фонового сервиса OpenCode

Статус: accepted

## Решение

CLI `beacon` содержит три команды: `beacon agents`, `beacon watch` и `beacon attach`. Команды читают registration-файл уже работающего фонового сервиса OpenCode `$XDG_STATE_HOME/opencode/service.json` и вызывают `opencode api` через существующий тип `runner` из `cmd/beacon/main.go`. `beacon agents` печатает список агентов, `beacon watch` показывает таблицу задач Beacon рядом с состоянием сессий OpenCode, `beacon attach` открывает сессию в обычном TUI через `opencode --session` и завершается.

Ядро остаётся на MCP и не знает имён агентов. Весь код, специфичный для OpenCode, по-прежнему живёт в одном JS-плагине `.opencode/plugins/beacon-bridge.js`. Готовность задачи CLI берёт из вердикта Beacon, а не из статуса `idle` OpenCode.

Отдельно признаётся открытым решением владельца напряжение между ADR 0006 и ADR 0024, которые требуют, чтобы весь OpenCode-код жил в JS-плагине, и новым тонким Go-адаптером в CLI. В рамках этого ADR выбран вариант без нового Go-HTTP-клиента: весь доступ к серверу идёт только через вызовы `opencode api` при помощи уже существующего `runner`, а состояние задач читается из квитанций плагина `$XDG_STATE_HOME/beacon/receipts`. Вопрос о окончательной границе между JS-плагином и CLI-адаптером остаётся открытым и не решается этим ADR.

## Причина

Проектное предложение `docs/design/opencode-cli-manager.md:3` фиксирует, что это предложение само по себе не меняет карточки и ADR, поэтому границы CLI и ядра требуют отдельного согласованного решения.

Сервис уже пишет свой адрес: `docs/design/opencode-cli-manager.md:11` описывает registration-файл `$XDG_STATE_HOME/opencode/service.json` с полями `url`, `pid`, `version` и приватным паролем, при этом `Service.discover()` читает файл и делает health-probe, но никогда не спавнит процесс. Отсюда решение читать registration-файл и не поднимать собственный сервер.

Готовый CLI-интерфейс уже есть: `docs/design/opencode-cli-manager.md:13` указывает на `opencode service start|restart|status|stop|get|set|unset` и `opencode api <operation | method path...>` с флагами `--data`, `--param`, `--header`. Прямо сказано, что обвязке не обязательно содержать собственный HTTP-клиент. Вызовы `opencode api session.list` и `GET /api/event` перечислены в `docs/design/opencode-cli-manager.md:35`. Поэтому доступ к серверу идёт через `runner`, а не через новый HTTP-клиент на Go.

Вывод предложения в `docs/design/opencode-cli-manager.md:21` это использовать уже существующий фоновый сервис и его registration-файл; собственный спавн нужен только если сервис отключён. Состав команд обоснован в `docs/design/opencode-cli-manager.md:51`: минимальная версия это `beacon watch` и `beacon attach`, а отдельная команда `beacon agents` дешевле по коду, чем флаг у `watch`. Место кода определено в `docs/design/opencode-cli-manager.md:53`: вызовы внешних процессов уже абстрагированы типом `runner`, каталог состояния даёт `stateDir()`, снимок Beacon читается через `SnapshotReader`.

Напряжение с прежними ADR зафиксировано в `docs/design/opencode-cli-manager.md:95`: ADR 0006 и ADR 0024 требуют, чтобы весь OpenCode-код жил в одном JS-плагине, а здесь добавляется Go-адаптер в CLI; пункт остаётся открытым решением владельца. Там же описан альтернативный путь без нового Go-кода: `beacon watch` читает только квитанции плагина, а запросы к серверу делает внешний `opencode api` через уже существующий `runner`. Этот путь и закреплён в решении выше.

ADR 0006 (`docs/adrs/0006-opencode-adapter-boundary.md:7`) и ADR 0024 (`docs/adrs/0024-one-bridge-per-protocol.md:7`, `docs/adrs/0024-one-bridge-per-protocol.md:9`) сохраняют границу: Go-ядро не содержит упоминаний и имён конкретных агентов, мост живёт рядом с плагином. ADR 0020 (`docs/adrs/0020-task-tool.md:20`) требует, чтобы задача приходила из согласованного решения, поэтому карточки CLI появляются только после статуса `accepted` этого ADR.

## Последствия

- CLI читает registration-файл фонового сервиса и не спавнит собственный сервер. Если сервис не запущен, команда печатает команду запуска и завершается с кодом 2.
- Все вызовы OpenCode идут через `opencode api` при помощи `runner`, поэтому подменяются в тестах и не требуют нового Go-HTTP-клиента.
- Ядро не знает имён агентов. Список агентов и состояние сессий остаются вне MCP, а граница ADR 0006 и ADR 0024 не меняется.
- `beacon watch` показывает вердикт Beacon, а не статус `idle` из OpenCode. Подписка живая, история не хранится, после обрыва соединения состояние перечитывается.
- Вопрос о границе между JS-плагином и Go-адаптером в CLI остаётся открытым решением владельца и выносится отдельно от этого ADR.
- Из этого решения выходят три карточки в `docs/tasks/` по правилам ADR 0020. Карточки не создаются этим ADR и будут созданы отдельными задачами. Гипотетические владельцы файлов:
  - `beacon agents`: `cmd/beacon/agents.go`, `cmd/beacon/agents_test.go`.
  - `beacon watch`: `cmd/beacon/watch.go`, `cmd/beacon/watch_test.go`.
  - `beacon attach`: `cmd/beacon/attach.go`, `cmd/beacon/attach_test.go`.
- Проверка карточек: команды читают registration-файл, вызывают `opencode api` только через `runner`, не содержат собственного HTTP-клиента и не добавляют имён агентов в Go-ядро.

## Отклонённое

Новый Go-HTTP-клиент OpenCode в CLI или в `internal/domain`. `docs/design/opencode-cli-manager.md:13` показывает, что `opencode api` уже покрывает нужные вызовы, а `docs/design/opencode-cli-manager.md:107` прямо запрещает добавлять HTTP-клиент в домен и MCP-инструменты.

Собственный спавн `opencode serve --hostname --port` по образцу t3code. `docs/design/opencode-cli-manager.md:15` описывает такой путь, но `docs/design/opencode-cli-manager.md:21` выбирает фоновый сервис и его registration-файл как вывод для Beacon.

Хардкод порта `4096`. Это значение по умолчанию, а не адрес работающего сервиса (`docs/design/opencode-cli-manager.md:111`).

Опрос сервера в цикле вместо подписки на `/api/event` (`docs/design/opencode-cli-manager.md:101`). История событий не хранится, поэтому после обрыва перечитывается статус, а не нагоняются пропущенные события.

Признание `session.idle` или пустого статуса готовностью задачи (`docs/design/opencode-cli-manager.md:103`). Готовность приходит из вердикта Beacon.

Перенос OpenCode-кода в Go-ядро или MCP-инструменты. Это ломает границу ADR 0006 и ADR 0024.

Собственный TUI или веб-интерфейс (`docs/design/opencode-cli-manager.md:99`). `beacon attach` открывает обычный TUI `opencode --session`.
