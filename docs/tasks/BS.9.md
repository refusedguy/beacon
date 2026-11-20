# BS.9. Project scope по resolved cwd и git root
Статус: done
Фаза: P0
Тип: task
Владелец файлов: internal/infra/server/project.go, cmd/beacon/doctor.go
Зависимости: BS.8
Ссылка: ADR 0200
Агент: нет
Время: нет
Цель: Состояние привязано к проекту.
Контекст: State лежит в beacon/projects/<name>-<hash>/beacon.db.
Шаги: Вычислять project id из cwd и git root, хранить worktree id и branch.
Проверка: Два проекта дают две разные БД.
Приёмка: Клиент не может выбрать чужой проект явно.

## Подтверждение
- PR #392 MERGED: 2026-11-20 (squash).
