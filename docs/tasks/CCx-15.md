# CCx-15. Восстановление workspace root при resume
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/app/session/registry.go, cmd/beacon/agents.go, internal/app/session/workspace_resume_test.go
Зависимости: CCx-06, CCx-11
Ссылка: docs/research/comp-codex.md, codex-rs/exec/tests/suite/resume.rs:826-945, :890, :947-1053
Агент: нет
Время: нет
Цель: Возвращать сессии только запрошенные корни и сохранять явные настройки из прежней версии.
Контекст: Источник: docs/research/comp-codex.md. Codex добавляет в сохранённый workspace только запрошенные корни (`codex-rs/exec/tests/suite/resume.rs:826-945`). Он также восстанавливает неименованный read-only корень (`:890`) и сохраняет явные настройки из прежней версии (`:947-1053`). Beacon хранит `Metadata` в записи сессии (`internal/app/session/registry.go:121-127`), но песочница создаётся заново через `git clone --reference` (`cmd/beacon/agents.go:406-413`). Список корней при resume не восстанавливается. Baseline: 0 полей workspace root, восстанавливаемых при resume.
Шаги: Записать корни и флаги только для чтения в метаданные сессии. При resume объединить сохранённые корни с явно запрошенными. Не добавлять корни, которых не было в запросе. Сохранить явные настройки из прежней записи. Покрыть тестом на два сохранённых корня и один запрошенный.
Проверка: CI зелёный. Тест показывает ровно 1 корень в сессии после resume. Явная настройка из прежней записи остаётся в 1 экземпляре.
Приёмка: Изменения только в OwnerFiles карточки. Формат `agent sandbox argv` из `cmd/beacon/agents_test.go` сохранить.
