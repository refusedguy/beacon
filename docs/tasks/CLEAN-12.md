# CLEAN-12. Удалить internal/tui после закрытия WIRING-493
Статус: done
baseline: перепроверка 2026-11-21, origin/main = b9937c01; WIRING-493 в статусе todo; все три её блокера сохраняются; `git grep -n 'tui.Listen(' origin/main -- internal cmd` = пусто; `git grep -n 'internal/tui' origin/main -- cmd` = только комментарии; в `layers.json` ребра cmd к internal/tui нет; в `humanDecisionSink` claim и release не обрабатываются; owner-decision по ADR 0060 и layer-ребру в дереве не зафиксирована
Фаза: P2
Тип: task
Владелец файлов: internal/tui/, docs/tasks/WIRING-493.md
Зависимости: WIRING-493
Ссылка: 
Агент: exe78
Время: 2026-11-21T17:44:33Z
Цель: Закрыть или реализовать WIRING-493 и только потом решить судьбу пяти файлов TUI.
Контекст: Источник: docs/research/deadcode-audit.md, строка про internal/tui. 1113 не-тестовых строк, 0 импортёров. WIRING-493 в статусе todo прямо фиксирует отсутствие импортов TUI. Риск: высокий. Нужен owner-decision: удаление закроет возможность запуска TUI.
Шаги: Прочитать WIRING-493 и подтвердить намерение владельца. Если проводка отменена, закрыть карточку и удалить каталог. Если нет, оставить код и вернуть карточку в работу.
Проверка: Есть решение владельца по WIRING-493. CI зелёный после удаления.
Приёмка: Код TUI не удалён до решения владельца. Карточка остаётся todo.
Подтверждение: 2026-11-21, CLEAN-12. WIRING-493 закрыть нельзя: нет owner-decision по ADR 0060 и layer-ребру cmd к internal/tui, синка claim и release живёт в cmd/beacon/mcp.go вне OwnerFiles, отмена проводки владельцем не зафиксирована. От владельца нужно одно из двух: разрешить собственный TUI-процесс с ребром cmd к internal/tui и передать cmd/beacon/mcp.go для эффекта claim и release, либо явно отменить проводку и разрешить удаление internal/tui/. Пока решения нет, каталог internal/tui/ не удалён, карточка остаётся todo.
