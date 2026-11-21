# WIRING-1106. Bind worktree GC port in bridge composition root

Статус: done
Фаза: P3
Тип: task
Владелец файлов: cmd/beacon/mcp.go
Зависимости: нет
Ссылка: W12-B
Агент: exe84
Время: 2026-11-21T17:46:32Z
Цель: Bind worktree GC port in bridge composition root

Контекст: Wiring для PR #1106 отложен из Watch watch:refusedguy/beacon:1106:18a10c8dad2ef1ba8d83145bf92dc63a73013f52. Причина: cibridge declares WorktreeGC but cmd/beacon/mcp.go must bind it to worktree.Collect with the production git runner and live-watch heads

Шаги: Перенести wiring в отдельный PR и обновить контракт инструментов.
Проверка: Новый инструмент и CLI-команда работают на зелёном CI.
Приёмка: Wiring для PR #1106 выполнен, карточка WIRING-1106 закрыта.

Итог: Wiring уже влит в origin/main коммитом 2145ccb0df86bbaa36af5bc3276a6e47db529b79 (PR #1193, merged 2026-11-21T14:15:26Z). Функция bridgeWorktreeGC в cmd/beacon/mcp.go привязывает порт WorktreeGC к worktree.Collect с рабочим git runner и живыми головами Watch.
Доказательство: git merge-base --is-ancestor 2145ccb0df86bbaa36af5bc3276a6e47db529b79 origin/main вернул 0; PR #1193 в состоянии MERGED; отдельный PR с дублем кода не открывался.
