# CSx-01. Асинхронные хуки с таймаутом
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/hookrun/hookrun.go, .opencode/plugins/beacon-project/index.js
Зависимости:
Ссылка: docs/research/comp-claude-sdk.md; клон anthropics/claude-agent-sdk-python 3184360
Агент: нет
Время: 2026-11-22
Цель: Дать хукам beacon явную асинхронную форму с таймаутом и отказом по истечении срока.
Контекст: SDK описывает асинхронный выход хука полями `async_` и `asyncTimeout` (`src/claude_agent_sdk/types.py:800-812`) и таймаут матчера по умолчанию 60 секунд (`types.py:879-904`). В beacon `Run` вызывает скрипт через `exec.Command` без контекста, без таймаута и без отложенного выполнения (`internal/infra/hookrun/hookrun.go:13-17`). Регистрация `registerToolLifecycle` ждёт завершения каждого колбэка (`.opencode/plugins/beacon-project/index.js:170-196`). Карточка CS-01 добавляет типизированные события и fail-closed проверку способности, но не решает задачу долгого колбэка. Эта карточка закрывает только асинхронную форму и таймаут.
baseline: `rg -n 'asyncTimeout|AsyncHook' internal cmd .opencode` на `origin/main` 7244e15b даёт 0 строк; `rg -n 'context.WithTimeout' internal/infra/hookrun` даёт 0 строк.
Шаги: Добавить опцию таймаута в `hookrun.Run`, передать `context.Context` в `exec.CommandContext` и вернуть ошибку по истечении срока. Добавить тип асинхронного ответа хука с полями `async_` и `asyncTimeout` в плагин. Отвергать `asyncTimeout`, который превышает таймаут матчера. Сохранить текущий синхронный путь как поведение по умолчанию.
Проверка: Тест вызывает `Run` со скриптом, который спит дольше таймаута, и получает ошибку контекста за заданное время. Тест плагина принимает `async_: true` с корректным `asyncTimeout` и отвергает значение больше таймаута матчера. Измеримая проверка: `rg -n 'asyncTimeout|AsyncHook' <ветка> -- internal .opencode` даёт не менее 2 строк, а тест с таймаутом проходит за время не больше `asyncTimeout + 1s`.
Приёмка: Долгий хук не держит вызов инструмента, а его срок ограничен значением из конфигурации.
