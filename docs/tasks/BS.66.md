# BS.66. Реестр сессий с generations
Статус: todo
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
