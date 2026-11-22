# CSx-10. Контрольные точки файлов и откат по uuid сообщения
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/infra/worktree/gc.go, cmd/beacon/daemon.go
Зависимости:
Ссылка: docs/research/comp-claude-sdk.md; клон anthropics/claude-agent-sdk-python 3184360
Агент: нет
Время: 2026-11-22
Цель: Позволить агенту вернуть файлы worktree к состоянию на момент сообщения пользователя.
Контекст: SDK включает опцию `enable_file_checkpointing`, которая сохраняет копии файлов до изменения (`src/claude_agent_sdk/types.py:2858-2864`), и метод `rewind_files(user_message_id)`, который восстанавливает файлы по uuid сообщения (`src/claude_agent_sdk/client.py:364-394`). В beacon пакет worktree умеет только перечислять, проверять и удалять worktree (`internal/infra/worktree/gc.go:1-27`). Слово `Checkpoint` в `cmd/beacon/daemon.go:942-944` относится к SQLite WAL и не имеет отношения к файлам агента. CS-04 восстанавливает сессию, но не откатывает файлы.
baseline: `rg -n 'rewind_files|RewindFiles|file_checkpointing' internal cmd` на `origin/main` 7244e15b даёт 0 строк; `rg -n 'Checkpoint' cmd/beacon/daemon.go` даёт 2 строки, обе про SQLite.
Шаги: Добавить сохранение копий файлов worktree при каждом сообщении пользователя. Добавить команду или инструмент отката по идентификатору контрольной точки. Хранить копии вне worktree, чтобы GC не удалял их вместе с каталогом. Делать откат идемпотентным: повтор приводит к тому же содержимому.
Проверка: Тест меняет файл дважды, затем вызывает откат к первой точке. Хэш файла совпадает с хэшем первой копии, а worktree остаётся на месте. Измеримая проверка: `rg -n 'rewind|fileCheckpoint' <ветка> -- internal cmd` даёт не менее 3 строк, число удалённых worktree в тесте равно 0.
Приёмка: Откат восстанавливает байты файла и не трогает ветку или историю коммитов.
