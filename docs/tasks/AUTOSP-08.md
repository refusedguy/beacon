# AUTOSP-08. Fetch перед worktree в Go-пути автоспавна
Статус: done
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

Итог: Already landed in #1438 (4c4714ae38b15aac02664c568eeb39eff49c3a93). No duplicate code needed.
Доказательство: PR #1438 commit 4c4714ae38b15aac02664c568eeb39eff49c3a93 merge-base --is-ancestor 4c4714ae38b15aac02664c568eeb39eff49c3a93 origin/main exit 0; no open duplicate PR.
Причина: already landed in #1438 4c4714a
Время закрытия: 2026-11-22T11:29:08Z
