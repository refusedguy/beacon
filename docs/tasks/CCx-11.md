# CCx-11. Resume дописывает в существующий файл журнала
Статус: todo
Фаза: P2
Тип: task
Владелец файлов: internal/app/session/registry.go, internal/infra/store/sessions.go, internal/infra/store/sessions_resume_test.go
Зависимости: CCx-10
Ссылка: docs/research/comp-codex.md, codex-rs/exec/tests/suite/resume.rs:154-226, :672
Агент: нет
Время: нет
Цель: Сохранить тот же файл журнала при возобновлении и дописывать в него новые записи.
Контекст: Источник: docs/research/comp-codex.md. Codex проверяет в тесте, что `exec resume --last` дописывает в уже существующий файл (`codex-rs/exec/tests/suite/resume.rs:154-226`). Тот же контракт проверен для resume по id (`:672`). Beacon хранит сессию в SQLite через `SaveSession` (`internal/infra/store/sessions.go:64`) и открывает внешнюю сессию в `cmd/beacon/attach.go:261`. Договора о том, что resume продолжает прежний журнал, в этом пути нет. Baseline: 0 тестов на продолжение журнала, 1 upsert сессии в `internal/infra/store/sessions.go:64`.
Шаги: Записать путь журнала в метаданные сессии. При resume проверять, что путь существует, и дописывать, а не создавать новый. Если путь пропал, возвращать отдельную ошибку. Покрыть тестом на три исходные строки и одно возобновление.
Проверка: CI зелёный. Тест показывает тот же путь файла и 4 строки после resume вместо нового файла с 1 строкой.
Приёмка: Изменения только в OwnerFiles карточки. Формат rows в SQLite и миграции базы не менять.
