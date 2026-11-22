# AUTOSP-08. Fetch перед worktree в Go-пути автоспавна
Статус: todo
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/agents.go, cmd/beacon/agents_test.go
Зависимости: 
Ссылка: ADR 0306
Агент: нет
Время: нет
Цель: Создавать worktree только после обновления origin/main.
Контекст: Источник: docs/research/audit-01-autospawn.md, таблица строка 8. cmd/beacon/agents.go:964 worktreeAddArgs и :1003 вызов git worktree add в bindRepoWorktree без предшествующего fetch. Правило HD-011 уже реализовано в .opencode/plugins/beacon-bridge.js функцией checkFetchBeforeWorktree и зафиксировано в docs/tasks/HD-011.md:1-13. baseline: Go-путь это правило не использует, дубль проверки писать нельзя.
Шаги: Перед вызовом worktree add выполнить git fetch origin main через тот же runner. Сохранить идемпотентность bindRepoWorktree для повторного вызова. Добавить тест agents_test.go, который проверяет порядок команд в списке вызовов runner. Не менять JavaScript-плагин.
Проверка: CI зелёный. Тест фиксирует fetch раньше worktree add. Повторный bind не создаёт вторую команду fetch для уже связанной задачи.
Приёмка: Новая ветка автоспавнера всегда срезается с обновлённого origin/main.
