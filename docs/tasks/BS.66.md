# BS.66. Реестр сессий с generations
Статус: dropped
Фаза: P5
Тип: task
Владелец файлов: internal/infra/store/sessions.go
Зависимости: BS.11
Ссылка: ADR 0198
Агент: нет
Время: нет
Цель: Известно, кто жив в сессии и можно ли возобновить.
Контекст: openrig session-registry хранит kind, native session id и provenance токена.
Шаги: generation ordinal, occupant kind, resume-token rank, сравнение рангов.
Проверка: Низкий ранг не перезаписывает высокий.
Приёмка: Thread привязан к worktree и branch.

## Подтверждение
- PR #394 DROPPED: Session registry with generations/occupant/resume-token rank is a dead T2xx prototype (T211SessionRegistry in wake/deliver.go); live sessions are opaque ses_ IDs.
