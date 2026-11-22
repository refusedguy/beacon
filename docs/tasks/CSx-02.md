# CSx-02. Зеркало жизненного цикла хуков в потоке сообщений
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/event/bus.go, internal/infra/observe/otel.go
Зависимости:
Ссылка: docs/research/comp-claude-sdk.md; клон anthropics/claude-agent-sdk-python 3184360
Агент: ses_csx02_executor
Время: 2026-11-22T16:58:40Z
Цель: Публиковать фазы хуков в потоке событий beacon, чтобы наблюдатель видел начало и конец каждого хука.
Контекст: SDK включает опцию `include_hook_events` (`src/claude_agent_sdk/types.py:2660-2667`) и тип `HookEventMessage` с фазами `hook_started` и `hook_response` (`types.py:1922-1939`). В beacon `event.Envelope` хранит только `seq`, `scope`, `type`, `schema_version` и `payload` (`internal/app/event/bus.go:21-27`). Span наблюдателя не несёт имени хука или его фазы (`internal/infra/observe/otel.go:129-141`). Карточка CS-01 добавляет хуки как механизм управления вызовом, а не как наблюдаемый поток. Эта карточка добавляет только зеркало событий.
baseline: `rg -n 'hook_started|hook_response|include_hook_events' internal cmd .opencode` на `origin/main` 7244e15b даёт 0 строк.
Шаги: Добавить тип события `hook` с полями `hook_event_name` и фазой в `event.Envelope`. Публиковать `hook_started` до вызова колбэка и `hook_response` после него через существующий `Tx.Emit` (`internal/app/event/bus.go:177`). Включить публикацию опцией, выключенной по умолчанию, чтобы не менять текущий поток.
Проверка: Тест вызывает один хук при включённой опции и читает ровно два сообщения с фазами `hook_started` и `hook_response`. При выключенной опции тест читает 0 новых сообщений. Измеримая проверка: `rg -n 'hook_started|hook_response' <ветка> -- internal` даёт не менее 2 строк, а счётчик сообщений в тесте равен 2.
Приёмка: Наблюдатель восстанавливает порядок фаз хука из потока событий без чтения логов.

Причина: Already landed in origin/main via PR #1738, merge commit 171825deded39dd3c72c1c4b9cb4c223bae16f18; git merge-base --is-ancestor 171825de origin/main exit 0. No duplicate code.
Закрыл: ses_csx02_executor
Время закрытия: 2026-11-22T16:59:10Z