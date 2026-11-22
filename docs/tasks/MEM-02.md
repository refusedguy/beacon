# MEM-02. Перевести beacon watch и beacon agents на общий HTTP-порт opencode
Статус: done
Фаза: P1
Тип: task
Владелец файлов: cmd/beacon/watch.go, cmd/beacon/opencode.go
Зависимости: AUTOSP-08, TUI-03
Ссылка: 
Агент: нет
Время: нет
Цель: Убрать CLI-процесс opencode api из read-only команд.
Контекст: Источник: docs/research/audit-02-memory.md, строка таблицы про CLI-вызовы. cmd/beacon/watch.go:234 вызывает c.run(c.root, "opencode", []string{"api", "session.list"}). cmd/beacon/agents.go:184 вызывает app.agents, а :187 вызывает session.list как запасной источник. Каждый вызов поднимает отдельный процесс. Готовый порт живёт в cmd/beacon/opencode.go:151, newReadOnlyOpencodePort, и уже используется в cmd/beacon/mcp.go:1715. Baseline: три вызова c.run с opencode api в двух файлах. Owner files cmd/beacon/watch.go уже закреплён за TUI-03 из audit-03, поэтому MEM-02 ждёт эту карточку.
Шаги: Построить reader через newReadOnlyOpencodePort в обеих командах. Заменить вызовы на Client.Sessions и Client.ListAgents с тем же контекстом. Сохранить fallback на пустой порт: команда пишет честную ошибку service registration, а не поднимает сервер. Оставить разбор ответа через уже существующий ParseSessions из internal/infra/opencode/client.go:163. Не трогать opencode.go и client.go.
Проверка: CI зелёный. Поиск c.run с "opencode" в watch.go и agents.go не даёт совпадений. При отсутствии регистрации сервиса команда возвращает код 2 без спавна процесса.
Приёмка: Обе команды читают сессии через один HTTP-клиент. Число CLI-процессов на команду равно нулю.

Итог: Already landed in PR #1588 (commit 5b1f470d45446f736a90cba9035b9097111750be). Fix is in origin/main; no duplicate code needed.
Доказательство: PR #1588 merged 2026-11-22T11:56:52+05:00; commit 5b1f470d45446f736a90cba9035b9097111750be; git merge-base --is-ancestor 5b1f470d45446f736a90cba9035b9097111750be origin/main exit 0; no open duplicate PR.
Причина: already landed in PR #1588 5b1f470d
Время закрытия: 2026-11-22T11:25:31Z
