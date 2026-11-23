# CCx-10. Проверка identity сессии при возобновлении
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/attach.go, internal/app/session/registry.go, cmd/beacon/attach_identity_test.go
Зависимости: нет
Ссылка: docs/research/comp-codex.md, codex-rs/thread-store/src/local/model_context.rs:59-74
Агент: ses_ccx10d_executor
Время: 2026-11-22T18:06:27Z
Цель: Не открывать сессию, если сохранённый id и id в журнале или реестре не совпадают.
Контекст: Источник: docs/research/comp-codex.md. Codex читает `session_meta` из журнала и отвергает запись, где `session_meta.meta.id` не равен запрошенному thread id (`codex-rs/thread-store/src/local/model_context.rs:59-74`). Beacon в `runAttach` берёт id из `savedAttachSession` и сразу подменяет его через registry (`cmd/beacon/attach.go:225-240`). `SessionRegistry.Resume` возвращает строку по id без проверки identity (`internal/app/session/registry.go:132-140`). Baseline: 0 проверок identity между чтением сохранённой сессии и вызовом `opencode` в `cmd/beacon/attach.go:261`.
Шаги: Добавить идентификатор сессии в запись реестра. Сверять запрошенный id с id из реестра и из журнала. При расхождении возвращать ошибку и не открывать сессию. Покрыть тестом на подменённый id.
Проверка: CI зелёный. Тест с расхождением id показывает 1 ошибку и 0 открытых сессий. Тест с совпадением открывает 1 сессию.
Приёмка: Изменения только в OwnerFiles карточки. Поведение `attach <sessionID>` без `--resume` сохранить.

Причина: Уже влито в main через PR #1870. Коммит 6c68bd79aaf12c3160574bf343309f6091e52c10 (merge-коммит PR #1870) является предком origin/main, git merge-base --is-ancestor вернул 0. Проверка identity лежит в cmd/beacon/attach.go (поле Identity в attachSession, registryResumeSession) и internal/app/session/registry.go (ResumeIdentified, ErrSessionIdentityMismatch), тест в cmd/beacon/attach_identity_test.go. Дубль кода не создан.
Закрыл: ses_ccx10d_executor
Время закрытия: 2026-11-22T18:07:14Z
## Подтверждение
- PR #2072 MERGED: 2026-11-22 (squash).

