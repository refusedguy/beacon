# CCx-12. Восстановление журнала, отсутствующего в базе состояния
Статус: done
Фаза: P2
Тип: task
Владелец файлов: cmd/beacon/attach.go, internal/app/session/registry.go, cmd/beacon/attach_repair_test.go
Зависимости: CCx-10, CCx-11
Ссылка: docs/research/comp-codex.md, codex-rs/exec/tests/suite/resume.rs:250-295
Агент: ses_ccx12c_executor
Время: 2026-11-22T18:10:23Z
Цель: Восстанавливать запись сессии по журналу, если база состояния её потеряла.
Контекст: Источник: docs/research/comp-codex.md. Codex отдельно тестирует случай, когда журнал есть, а записи в базе состояния нет (`codex-rs/exec/tests/suite/resume.rs:250-295`). Resume восстанавливает запись и продолжает работу. Beacon при отсутствии строки получает `found=false` из `SessionRegistry.Resume` (`internal/app/session/registry.go:132-140`). `registryResumeSession` просто возвращает id из `attach-sessions.json` (`cmd/beacon/attach.go:183-195`). Пути восстановления нет. Baseline: 0 restore-вызовов при `found=false`.
Шаги: Найти журнал по сохранённому id. Прочитать из него идентификатор и метаданные. Создать запись сессии заново и повторить resume. Покрыть тестом на отсутствующую строку в базе.
Проверка: CI зелёный. Тест показывает 1 восстановленную строку сессии и 1 успешный resume при отсутствии исходной записи.
Приёмка: Изменения только в OwnerFiles карточки. `attach-sessions.json` остаётся источником id по ADR 0043.

Причина: Реализация уже влита в main: PR #1869, merge commit 00621a08dad30f713533c9b0700e85c529a76361. git merge-base --is-ancestor <SHA> origin/main -> exit 0. Дубль кода не создавался.
Закрыл: ses_ccx12c_executor
Время закрытия: 2026-11-22T18:11:29Z
## Подтверждение
- PR #2093 MERGED: 2026-11-22 (squash).

