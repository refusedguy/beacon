# CCx-08. Одноразовая миграция правил с маркером no-clobber
Статус: done
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/orchestrate.go, cmd/beacon/config_migration_test.go
Зависимости: нет
Ссылка: docs/research/comp-codex.md, codex-rs/execpolicy/src/sandbox_migration.rs:10-29, :31-47
Агент: ses_ccx08c_executor
Время: 2026-11-22T17:34:10Z
Цель: Удалять устаревшие правила конфига один раз и оставлять проверяемый маркер.
Контекст: Источник: docs/research/comp-codex.md. Codex хранит маркер `.sandbox_migration` в home каталоге (`codex-rs/execpolicy/src/sandbox_migration.rs:10`). При старте он читает маркер, чистит файл правил и пишет маркер (`:16-29`). Запись идёт через tempfile и `persist_noclobber`; `AlreadyExists` считается успехом (`:31-47`). Beacon проверяет `skills-lock.json` только на чтение и сообщает о проблемах (`cmd/beacon/orchestrate.go:697-721`). Одноразовой миграции и маркера нет. Baseline: 0 маркеров миграции и 0 записей no-clobber в пути конфига.
Шаги: Выбрать каталог state для маркера. Добавить шаг миграции, который чистит устаревшие записи и пишет маркер временным файлом. Гонку двух процессов различать через `AlreadyExists`. Сохранить режим проверки конфига без изменений при отсутствии устаревших записей.
Проверка: CI зелёный. Два последовательных запуска дают 1 очистку и 1 маркер. Третий запуск не меняет файл правил. Два параллельных запуска оставляют 1 маркер.
Приёмка: Изменения только в OwnerFiles карточки. Формат `skills-lock.json` и его golden-тесты не трогать.

Причина: Implementation already landed in origin/main via commit 837b0d34 (PR #1830). git merge-base --is-ancestor 837b0d34 origin/main exit 0. No duplicate code written.
Закрыл: ses_ccx08c_executor
Время закрытия: 2026-11-22T17:35:34Z
## Подтверждение
- PR #2048 MERGED: 2026-11-22 (squash).

