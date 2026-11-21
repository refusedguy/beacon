# WIRING-1106. Bind worktree GC port in bridge composition root

Статус: todo
Фаза: P3
Тип: task
Владелец файлов: cmd/beacon/mcp.go
Зависимости: нет
Ссылка: W12-B
Агент: watch:refusedguy/beacon:1106:18a10c8dad2ef1ba8d83145bf92dc63a73013f52
Время: нет
Цель: Bind worktree GC port in bridge composition root

Контекст: Wiring для PR #1106 отложен из Watch watch:refusedguy/beacon:1106:18a10c8dad2ef1ba8d83145bf92dc63a73013f52. Причина: cibridge declares WorktreeGC but cmd/beacon/mcp.go must bind it to worktree.Collect with the production git runner and live-watch heads

Шаги: Перенести wiring в отдельный PR и обновить контракт инструментов.
Проверка: Новый инструмент и CLI-команда работают на зелёном CI.
Приёмка: Wiring для PR #1106 выполнен, карточка WIRING-1106 закрыта.
