# CCx-14. Read-only с первого shell-вызова после resume
Статус: done
Фаза: P2
Тип: task
Владелец файлов: internal/app/orchestrator/orchestrator.go, internal/app/orchestrator/read_only_resume_test.go
Зависимости: CCx-06
Ссылка: docs/research/comp-codex.md, codex-rs/exec/tests/suite/resume.rs:746-824
Агент: ses_ccx14d_executor
Время: 2026-11-22T18:11:05Z
Цель: Не давать первому shell-вызову после resume писать на диск до явного одобрения.
Контекст: Источник: docs/research/comp-codex.md. Codex проверяет, что read-only действует уже на первом shell-вызове после resume (`codex-rs/exec/tests/suite/resume.rs:746-824`). Beacon не связывает resume с режимом песочницы. Политика в `internal/app/orchestrator/orchestrator.go:72-92` касается только мержа, а изоляция executora это `git clone --reference` (`cmd/beacon/agents.go:406-413`). Baseline: 0 точек enforcement read-only после resume.
Шаги: Сохранять режим read-only в метаданных возобновлённой сессии. Применять его к перевыполу перед первым shell-вызовом. Снимать режим только после одобрения. Покрыть тестом на первый и второй вызов.
Проверка: CI зелёный. Тест показывает 1 отказ и 0 записанных файлов на первом вызове. После одобрения второй вызов записывает 1 файл.
Приёмка: Изменения только в OwnerFiles карточки. Поведение `beacon attach` без флага resume не менять.

Причина: Already landed in origin/main via PR #1931, commit 1f9ad9dcabb73b03b8eaca7006251a76e886a734 (feat(orchestrator): read-only first shell call after resume (CCx-14) (#1931)). git merge-base --is-ancestor 1f9ad9dcabb73b03b8eaca7006251a76e886a734 origin/main exit 0. No duplicate code written.
Закрыл: ses_ccx14d_executor
Время закрытия: 2026-11-22T18:11:53Z
